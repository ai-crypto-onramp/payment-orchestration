package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/audit"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/rail"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
	"github.com/google/uuid"
)

// Service wires together the store, rail registry, audit sink and idempotency
// cache, exposing handler methods that operate on *http.Request / ResponseWriter.
type Service struct {
	Store      *store.Store
	Rails      *rail.Registry
	Audit      audit.Sink
	WebhookKey []byte

	idemMu     sync.Mutex
	idemCache  map[string]idemEntry
}

type idemEntry struct {
	status int
	body   []byte
	expires time.Time
}

// NewService returns a Service ready to serve requests.
func NewService(s *store.Store, r *rail.Registry, a audit.Sink, webhookKey string) *Service {
	return &Service{
		Store:      s,
		Rails:      r,
		Audit:      a,
		WebhookKey: []byte(webhookKey),
		idemCache:  make(map[string]idemEntry),
	}
}

const idemTTL = 24 * time.Hour

// idemKey scopes an idempotency key to the endpoint so the same key used on two
// different endpoints does not collide.
type idemKey struct {
	endpoint string
	key      string
}

func (s *Service) lookupIdem(endpoint, key string) (int, []byte, bool) {
	if key == "" {
		return 0, nil, false
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	k := idemKey{endpoint, key}.String()
	e, ok := s.idemCache[k]
	if !ok {
		return 0, nil, false
	}
	if time.Now().After(e.expires) {
		delete(s.idemCache, k)
		return 0, nil, false
	}
	return e.status, e.body, true
}

func (s *Service) saveIdem(endpoint, key string, status int, body []byte) {
	if key == "" {
		return
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	s.idemCache[idemKey{endpoint, key}.String()] = idemEntry{
		status:  status,
		body:    append([]byte(nil), body...),
		expires: time.Now().Add(idemTTL),
	}
}

func (k idemKey) String() string { return k.endpoint + "\x00" + k.key }

// --- request / response helpers ---

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func (s *Service) replayOrContinue(w http.ResponseWriter, r *http.Request, endpoint string, handle func() (int, interface{})) {
	key := r.Header.Get("Idempotency-Key")
	if status, body, ok := s.lookupIdem(endpoint, key); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotency-Replay", "true")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	status, v := handle()
	var body []byte
	if v != nil {
		body, _ = json.Marshal(v)
	}
	s.saveIdem(endpoint, key, status, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_, _ = w.Write(body)
	}
}

// --- create intent ---

type createIntentReq struct {
	Rail          domain.Rail `json:"rail"`
	Amount        int64       `json:"amount"`
	Currency      string      `json:"currency"`
	PayerRef      string      `json:"payer_ref"`
	ThreeDSRequired bool      `json:"three_ds_required"`
}

// CreateIntent handles POST /v1/payments/intents.
func (s *Service) CreateIntent(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
		return
	}

	var req createIntentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !domain.IsValidRail(req.Rail) {
		writeError(w, http.StatusBadRequest, "unsupported rail")
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	if req.Currency == "" {
		writeError(w, http.StatusBadRequest, "currency is required")
		return
	}
	if req.PayerRef == "" {
		writeError(w, http.StatusBadRequest, "payer_ref is required")
		return
	}
	if req.ThreeDSRequired && req.Rail != domain.RailCard {
		writeError(w, http.StatusBadRequest, "three_ds_required only valid for card rail")
		return
	}

	s.replayOrContinue(w, r, "create", func() (int, interface{}) {
		now := time.Now().UTC()
		i := &domain.Intent{
			ID:              uuid.NewString(),
			Rail:            req.Rail,
			Amount:          req.Amount,
			Currency:        req.Currency,
			PayerRef:        req.PayerRef,
			Status:          domain.StatusIntent,
			IdempotencyKey:  r.Header.Get("Idempotency-Key"),
			ThreeDSRequired: req.ThreeDSRequired,
			CreatedAt:       now,
			UpdatedAt:       now,
			History:         []domain.Event{{Type: domain.EventCreated, At: now}},
		}

		adapter := s.Rails.For(req.Rail)

		// 3DS path: go straight to 3ds_pending; auth happens after challenge.
		if req.ThreeDSRequired {
			if err := i.Transition(domain.Status3DSPending, "3ds challenge required"); err != nil {
				return http.StatusInternalServerError, errorBody{err.Error()}
			}
			s.Store.CreateIntent(i)
			s.emitTransition(i, domain.StatusIntent, domain.Status3DSPending, "3ds required")
			return http.StatusCreated, i
		}

		// Non-3DS: authorize now.
		if err := adapter.Authorize(i); err != nil {
			_ = i.Transition(domain.StatusFailed, "authorize failed: "+err.Error())
			s.Store.CreateIntent(i)
			s.emitTransition(i, domain.StatusIntent, domain.StatusFailed, err.Error())
			return http.StatusBadGateway, errorBody{"authorize failed"}
		}
		if err := i.Transition(domain.StatusAuthorized, "authorized by rail"); err != nil {
			return http.StatusInternalServerError, errorBody{err.Error()}
		}
		s.Store.CreateIntent(i)
		s.emitTransition(i, domain.StatusIntent, domain.StatusAuthorized, "authorized")
		return http.StatusCreated, i
	})
}

// --- get intent ---

// GetIntent handles GET /v1/payments/:id.
func (s *Service) GetIntent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	i := s.Store.GetIntent(id)
	if i == nil {
		writeError(w, http.StatusNotFound, "intent not found")
		return
	}
	writeJSON(w, http.StatusOK, i)
}

// --- capture ---

type captureReq struct {
	Amount *int64 `json:"amount,omitempty"`
}

// Capture handles POST /v1/payments/:id/capture.
func (s *Service) Capture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req captureReq
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	s.replayOrContinue(w, r, "capture:"+id, func() (int, interface{}) {
		updated, err := s.Store.UpdateIntent(id, func(i *domain.Intent) error {
			if i.Rail != domain.RailCard {
				return errors.New("capture only supported for card rail")
			}
			if i.Status != domain.StatusAuthorized {
				return errors.New("intent not in authorized state")
			}
			amount := i.Amount
			if req.Amount != nil {
				if *req.Amount <= 0 {
					return errors.New("capture amount must be positive")
				}
				if *req.Amount > i.Amount-i.CapturedAmount {
					return errors.New("capture amount exceeds authorized amount")
				}
				amount = *req.Amount
			}
			if i.CapturedAmount+amount > i.Amount {
				return errors.New("capture amount exceeds authorized amount")
			}
		if err := s.Rails.For(i.Rail).Capture(i, amount); err != nil {
			return err
		}
		i.CapturedAmount += amount
		prev := i.Status
		// Stay in authorized until the full authorized amount is captured; the
		// final capture transitions to captured.
		if i.CapturedAmount >= i.Amount {
			if err := i.Transition(domain.StatusCaptured, "captured"); err != nil {
				return err
			}
			s.emitTransition(i, prev, domain.StatusCaptured, "capture")
		} else {
			i.AppendEvent(domain.EventCaptured, "partial capture")
			s.emitTransition(i, prev, prev, "partial capture")
		}
		return nil
	})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return http.StatusNotFound, errorBody{"intent not found"}
			}
			if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrTerminalState) {
				return http.StatusConflict, errorBody{err.Error()}
			}
			return http.StatusBadGateway, errorBody{err.Error()}
		}
		return http.StatusOK, updated
	})
}

// --- refund ---

type refundReq struct {
	Amount *int64 `json:"amount,omitempty"`
}

// Refund handles POST /v1/payments/:id/refund.
func (s *Service) Refund(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req refundReq
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	s.replayOrContinue(w, r, "refund:"+id, func() (int, interface{}) {
		updated, err := s.Store.UpdateIntent(id, func(i *domain.Intent) error {
			if i.Status != domain.StatusCaptured && i.Status != domain.StatusSettled {
				return domain.ErrInvalidTransition
			}
			amount := i.CapturedAmount
			if req.Amount != nil {
				if *req.Amount <= 0 {
					return errors.New("refund amount must be positive")
				}
				amount = *req.Amount
			}
			if i.RefundedAmount+amount > i.CapturedAmount {
				return errors.New("refund amount exceeds captured amount")
			}
			if err := s.Rails.For(i.Rail).Refund(i, amount); err != nil {
				return err
			}
			i.RefundedAmount += amount
			prev := i.Status
			if err := i.Transition(domain.StatusRefunding, "refund initiated"); err != nil {
				return err
			}
			if err := i.Transition(domain.StatusRefunded, "refunded"); err != nil {
				return err
			}
			s.emitTransition(i, prev, domain.StatusRefunded, "refund")
			return nil
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return http.StatusNotFound, errorBody{"intent not found"}
			}
			if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrTerminalState) {
				return http.StatusConflict, errorBody{"refund not allowed from current state"}
			}
			return http.StatusBadGateway, errorBody{err.Error()}
		}
		return http.StatusOK, updated
	})
}

// --- 3ds challenge ---

type challengeReq struct {
	ChallengeResult string `json:"challenge_result"`
}

// ThreeDSChallenge handles POST /v1/payments/:id/3ds-challenge.
func (s *Service) ThreeDSChallenge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req challengeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.replayOrContinue(w, r, "3ds:"+id, func() (int, interface{}) {
		updated, err := s.Store.UpdateIntent(id, func(i *domain.Intent) error {
			if i.Status != domain.Status3DSPending {
				return domain.ErrInvalidTransition
			}
			if err := s.Rails.For(i.Rail).Verify3DS(i, req.ChallengeResult); err != nil {
				prev := i.Status
				_ = i.Transition(domain.StatusFailed, "3ds verification failed: "+err.Error())
				s.emitTransition(i, prev, domain.StatusFailed, err.Error())
				return err
			}
			prev := i.Status
			if err := i.Transition(domain.StatusAuthorized, "3ds verified, authorized"); err != nil {
				return err
			}
			s.emitTransition(i, prev, domain.StatusAuthorized, "3ds verified")
			return nil
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return http.StatusNotFound, errorBody{"intent not found"}
			}
			if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrTerminalState) {
				return http.StatusConflict, errorBody{"3ds challenge not allowed from current state"}
			}
			if errors.Is(err, rail.Err3DSVerify) || errors.Is(err, rail.ErrUnsupported3DS) {
				return http.StatusBadGateway, errorBody{err.Error()}
			}
			return http.StatusBadGateway, errorBody{err.Error()}
		}
		return http.StatusOK, updated
	})
}

// --- webhooks ---

type webhookBody struct {
	PaymentID string `json:"payment_id"`
	Type      string `json:"type"`
}

// Webhook handles POST /v1/webhooks/:rail.
func (s *Service) Webhook(w http.ResponseWriter, r *http.Request) {
	railName := r.PathValue("rail")
	if !domain.IsValidRail(domain.Rail(railName)) {
		writeError(w, http.StatusBadRequest, "unsupported rail")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	sig := r.Header.Get("X-Webhook-Signature")
	if !verifyHMAC(s.WebhookKey, body, sig) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	var wb webhookBody
	_ = json.Unmarshal(body, &wb)

	if wb.PaymentID != "" {
		_, err := s.Store.UpdateIntent(wb.PaymentID, func(i *domain.Intent) error {
			i.AppendEvent(domain.EventWebhook, railName+":"+wb.Type)
			return nil
		})
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
}

// verifyHMAC reports whether sig is the hex-encoded HMAC-SHA256 of body with key.
func verifyHMAC(key, body []byte, sig string) bool {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if sig == "" {
		return false
	}
	// Support "sha256=<hex>" or bare "<hex>".
	sig = strings.TrimPrefix(sig, "sha256=")
	return hmac.Equal([]byte(sig), []byte(expected))
}

// --- health ---

func Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) Readyz(w http.ResponseWriter, r *http.Request) {
	// The store is in-memory and always ready.
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// emitTransition emits an audit event for a state transition.
func (s *Service) emitTransition(i *domain.Intent, from, to domain.Status, detail string) {
	if s.Audit == nil {
		return
	}
	s.Audit.Emit(audit.Event{
		IntentID:  i.ID,
		FromState: from,
		ToState:   to,
		Detail:    detail,
		At:        time.Now().UTC(),
	})
}

// NewMux builds the HTTP routing mux for the service.
func NewMux(svc *Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", Healthz)
	mux.HandleFunc("GET /readyz", svc.Readyz)
	mux.HandleFunc("POST /v1/payments/intents", svc.CreateIntent)
	mux.HandleFunc("GET /v1/payments/{id}", svc.GetIntent)
	mux.HandleFunc("POST /v1/payments/{id}/capture", svc.Capture)
	mux.HandleFunc("POST /v1/payments/{id}/refund", svc.Refund)
	mux.HandleFunc("POST /v1/payments/{id}/3ds-challenge", svc.ThreeDSChallenge)
	mux.HandleFunc("POST /v1/webhooks/{rail}", svc.Webhook)
	return mux
}

// --- helpers for tests ---

// MustJSON is a small helper used by tests to marshal without checking errors.
func MustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ReadAllBody is a thin wrapper for tests.
func ReadAllBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(r.Body)
}

// AssertBodyContains is a tiny test helper exported for the api_test package.
func AssertBodyContains(body []byte, needle string) bool {
	return bytes.Contains(body, []byte(needle))
}
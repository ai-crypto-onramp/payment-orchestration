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
	"github.com/ai-crypto-onramp/payment-orchestration/internal/config"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/fraud"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/logging"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/metrics"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/mpi"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/rail"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
	"github.com/google/uuid"
)

// Service wires together the store, rail registry, audit sink and idempotency
// cache, exposing handler methods that operate on *http.Request / ResponseWriter.
type Service struct {
	Store           *store.Store
	Rails           *rail.Registry
	Audit           audit.Sink
	WebhookKeys     map[domain.Rail][]byte
	ReplayWindow    time.Duration
	MPI             mpi.Client
	Fraud           fraud.Client
	Metrics         *metrics.Metrics
	Logger          *logging.Logger

	idemMu    sync.Mutex
	idemCache map[string]idemEntry
}

type idemEntry struct {
	status  int
	body    []byte
	expires time.Time
}

// NewService returns a Service ready to serve requests.
func NewService(s *store.Store, r *rail.Registry, a audit.Sink, webhookKey string) *Service {
	keys := map[domain.Rail][]byte{
		domain.RailCard: []byte(webhookKey),
		domain.RailACH:  []byte(webhookKey),
		domain.RailSEPA: []byte(webhookKey),
		domain.RailPIX:  []byte(webhookKey),
		domain.RailUPI:  []byte(webhookKey),
	}
	return &Service{
		Store:       s,
		Rails:       r,
		Audit:       a,
		WebhookKeys: keys,
		ReplayWindow: 5 * time.Minute,
		MPI:         mpi.NewDummy(),
		Fraud:       fraud.NewDummy(),
		Metrics:     metrics.New(),
		Logger:      logging.New(io.Discard, logging.LevelInfo),
		idemCache:   make(map[string]idemEntry),
	}
}

// ApplyConfig overrides service settings from the given config.
func (s *Service) ApplyConfig(c config.Config) {
	keys := make(map[domain.Rail][]byte, len(c.WebhookSecrets))
	for r, secret := range c.WebhookSecrets {
		if secret != "" {
			keys[r] = []byte(secret)
		}
	}
	if len(keys) > 0 {
		s.WebhookKeys = keys
	}
	s.ReplayWindow = c.WebhookReplayWindow
	if c.LogLevel != "" {
		s.Logger = logging.New(io.Discard, logging.ParseLevel(c.LogLevel))
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
	Rail            domain.Rail `json:"rail"`
	Amount          int64       `json:"amount"`
	Currency        string      `json:"currency"`
	PayerRef        string      `json:"payer_ref"`
	ThreeDSRequired bool        `json:"three_ds_required"`
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

	start := time.Now()
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
			s.Logger.Info("intent created (3ds pending)", map[string]interface{}{"intent_id": i.ID, "rail": string(i.Rail)})
			return http.StatusCreated, i
		}

		// Fraud gate before authorization.
		dec, err := s.Fraud.Score(r.Context(), i)
		if err != nil {
			_ = i.Transition(domain.StatusFailed, "fraud check failed: "+err.Error())
			s.Store.CreateIntent(i)
			s.emitTransition(i, domain.StatusIntent, domain.StatusFailed, err.Error())
			return http.StatusBadGateway, errorBody{"fraud check failed"}
		}
		if !dec.Allowed {
			_ = i.Transition(domain.StatusFailed, "blocked by fraud: "+dec.Reason)
			s.Store.CreateIntent(i)
			s.emitTransition(i, domain.StatusIntent, domain.StatusFailed, "fraud blocked")
			return http.StatusForbidden, errorBody{"intent blocked by fraud detection"}
		}

		// Instant rails (PIX, UPI) collapse auth+capture into a single Submit.
		if req.Rail.IsInstant() {
			if err := adapter.Submit(i); err != nil {
				_ = i.Transition(domain.StatusFailed, "submit failed: "+err.Error())
				s.Store.CreateIntent(i)
				s.emitTransition(i, domain.StatusIntent, domain.StatusFailed, err.Error())
				return http.StatusBadGateway, errorBody{"submit failed"}
			}
			// Submit performs auth+capture in one step: intent -> authorized -> captured.
			if err := i.Transition(domain.StatusAuthorized, "instant rail authorized via submit"); err != nil {
				return http.StatusInternalServerError, errorBody{err.Error()}
			}
			s.emitTransition(i, domain.StatusIntent, domain.StatusAuthorized, "instant authorize")
			if err := i.Transition(domain.StatusCaptured, "instant rail captured via submit"); err != nil {
				return http.StatusInternalServerError, errorBody{err.Error()}
			}
			s.Store.CreateIntent(i)
			s.Store.AddCapture(domain.Capture{
				ID:          uuid.NewString(),
				IntentID:    i.ID,
				Amount:      i.CapturedAmount,
				ExternalRef: i.ExternalID,
				CapturedAt:  time.Now().UTC(),
			})
			s.emitTransition(i, domain.StatusAuthorized, domain.StatusCaptured, "instant submit")
			s.Logger.Info("intent created (instant)", map[string]interface{}{"intent_id": i.ID, "rail": string(i.Rail)})
			return http.StatusCreated, i
		}

		// Non-3DS card / bank rails: authorize now.
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
		s.Logger.Info("intent created (authorized)", map[string]interface{}{"intent_id": i.ID, "rail": string(i.Rail)})
		return http.StatusCreated, i
	})
	s.Metrics.ObserveIntentCreation(time.Since(start))
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
		var captureRecord *domain.Capture
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
			captureRecord = &domain.Capture{
				ID:          uuid.NewString(),
				IntentID:    i.ID,
				Amount:      amount,
				ExternalRef: i.ExternalID,
				CapturedAt:  time.Now().UTC(),
			}
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
		if captureRecord != nil {
			s.Store.AddCapture(*captureRecord)
		}
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

// --- void ---

// Void handles POST /v1/payments/:id/void. It cancels a previously authorized
// intent that has not yet been captured.
func (s *Service) Void(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.replayOrContinue(w, r, "void:"+id, func() (int, interface{}) {
		updated, err := s.Store.UpdateIntent(id, func(i *domain.Intent) error {
			if i.Status != domain.StatusAuthorized {
				return domain.ErrInvalidTransition
			}
			if err := s.Rails.For(i.Rail).Void(i); err != nil {
				return err
			}
			prev := i.Status
			if err := i.Transition(domain.StatusVoided, "voided"); err != nil {
				return err
			}
			s.emitTransition(i, prev, domain.StatusVoided, "void")
			return nil
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return http.StatusNotFound, errorBody{"intent not found"}
			}
			if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrTerminalState) {
				return http.StatusConflict, errorBody{"void not allowed from current state"}
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
		var refundRecord *domain.Refund
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
			refundRecord = &domain.Refund{
				ID:          uuid.NewString(),
				IntentID:    i.ID,
				Amount:      amount,
				ExternalRef: i.ExternalID,
				RefundedAt:  time.Now().UTC(),
				State:       "refunded",
			}
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
		if refundRecord != nil {
			s.Store.AddRefund(*refundRecord)
		}
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
			if s.is3DSExpired(i) {
				prev := i.Status
				_ = i.Transition(domain.StatusFailed, "3ds challenge timed out")
				s.emitTransition(i, prev, domain.StatusFailed, "3ds timeout")
				return mpi.ErrTimeout
			}
			if err := s.MPI.Resume(i, req.ChallengeResult); err != nil {
				prev := i.Status
				_ = i.Transition(domain.StatusFailed, "3ds verification failed: "+err.Error())
				s.emitTransition(i, prev, domain.StatusFailed, err.Error())
				return err
			}
			if err := s.Rails.For(i.Rail).Verify3DS(i, req.ChallengeResult); err != nil {
				prev := i.Status
				_ = i.Transition(domain.StatusFailed, "3ds verification failed: "+err.Error())
				s.emitTransition(i, prev, domain.StatusFailed, err.Error())
				return err
			}
			if err := s.Rails.For(i.Rail).Authorize(i); err != nil {
				prev := i.Status
				_ = i.Transition(domain.StatusFailed, "authorize failed after 3ds: "+err.Error())
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
			if errors.Is(err, mpi.ErrTimeout) {
				return http.StatusConflict, errorBody{"3ds challenge timed out"}
			}
			if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrTerminalState) {
				return http.StatusConflict, errorBody{"3ds challenge not allowed from current state"}
			}
			if errors.Is(err, mpi.ErrChallengeFailed) {
				return http.StatusBadGateway, errorBody{err.Error()}
			}
			if errors.Is(err, rail.Err3DSVerify) || errors.Is(err, rail.ErrUnsupported3DS) {
				return http.StatusBadGateway, errorBody{err.Error()}
			}
			return http.StatusBadGateway, errorBody{err.Error()}
		}
		return http.StatusOK, updated
	})
}

// is3DSExpired reports whether the 3DS challenge window has expired for the
// intent. The window is bounded by the replay window config.
func (s *Service) is3DSExpired(i *domain.Intent) bool {
	if s.ReplayWindow <= 0 {
		return false
	}
	for idx := len(i.History) - 1; idx >= 0; idx-- {
		e := i.History[idx]
		if e.Type == domain.Event3DSPending {
			return time.Since(e.At) > s.ReplayWindow
		}
	}
	return false
}

// --- webhooks ---

type webhookBody struct {
	PaymentID      string `json:"payment_id"`
	Type            string `json:"type"`
	ExternalEventID string `json:"external_event_id"`
	Timestamp       int64  `json:"timestamp"`
	Amount          int64  `json:"amount"`
	Reason          string `json:"reason"`
	CaseRef         string `json:"case_ref"`
	Stage           string `json:"stage"`
	RailRef         string `json:"rail_ref"`
}

// Webhook handles POST /v1/webhooks/:rail.
func (s *Service) Webhook(w http.ResponseWriter, r *http.Request) {
	railName := r.PathValue("rail")
	railVal := domain.Rail(railName)
	if !domain.IsValidRail(railVal) {
		writeError(w, http.StatusBadRequest, "unsupported rail")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	key, ok := s.WebhookKeys[railVal]
	if !ok || len(key) == 0 {
		writeError(w, http.StatusUnauthorized, "no secret configured for rail")
		return
	}
	sig := r.Header.Get("X-Webhook-Signature")
	if !verifyHMAC(key, body, sig) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	var wb webhookBody
	_ = json.Unmarshal(body, &wb)

	// Replay-window check on signed timestamp (if provided).
	if wb.Timestamp != 0 {
		ts := time.Unix(wb.Timestamp, 0)
		if abs(time.Since(ts)) > s.ReplayWindow {
			writeError(w, http.StatusUnauthorized, "webhook timestamp outside replay window")
			return
		}
	}

	// De-duplicate by (rail, external_event_id) when an event id is present.
	if wb.ExternalEventID != "" {
		wh := domain.Webhook{
			ID:              uuid.NewString(),
			Rail:            railVal,
			ExternalEventID: wb.ExternalEventID,
			Signature:       "verified",
			ReceivedAt:      time.Now().UTC(),
		}
		if err := s.Store.RecordWebhook(wh); err != nil {
			if errors.Is(err, store.ErrDuplicateWebhook) {
				// Already processed; return success without re-applying side effects.
				writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.Metrics.IncWebhookBacklog()
		defer s.Store.MarkWebhookProcessed(railVal, wb.ExternalEventID)
		defer s.Metrics.DecWebhookBacklog()
	}

	s.processWebhook(w, railVal, wb)
}

// processWebhook applies the webhook payload to the relevant intent, mapping
// settlement and chargeback events to the state machine.
func (s *Service) processWebhook(w http.ResponseWriter, railVal domain.Rail, wb webhookBody) {
	if wb.PaymentID == "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
		return
	}

	switch {
	case wb.Type == "settlement":
		s.handleSettlementWebhook(w, railVal, wb)
	case wb.Type == "chargeback" || wb.Stage != "":
		s.handleChargebackWebhook(w, railVal, wb)
	default:
		_, err := s.Store.UpdateIntent(wb.PaymentID, func(i *domain.Intent) error {
			i.AppendEvent(domain.EventWebhook, string(railVal)+":"+wb.Type)
			return nil
		})
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
	}
}

// handleSettlementWebhook maps a settlement event to a settlements row and
// transitions the intent captured -> settled, reconciling captured vs
// settled amounts and emitting a break event on mismatch.
func (s *Service) handleSettlementWebhook(w http.ResponseWriter, railVal domain.Rail, wb webhookBody) {
	settledAmount := wb.Amount
	var settlementRecord *domain.Settlement
	_, err := s.Store.UpdateIntent(wb.PaymentID, func(i *domain.Intent) error {
		if i.Status != domain.StatusCaptured {
			i.AppendEvent(domain.EventWebhook, string(railVal)+":settlement (ignored)")
			return nil
		}
		i.SettledAmount += settledAmount
		settlementRecord = &domain.Settlement{
			ID:            uuid.NewString(),
			IntentID:      i.ID,
			SettledAmount: settledAmount,
			SettledAt:     time.Now().UTC(),
			RailRef:       wb.RailRef,
		}
		prev := i.Status
		if err := i.Transition(domain.StatusSettled, "settled via webhook"); err != nil {
			return err
		}
		s.emitTransition(i, prev, domain.StatusSettled, "settlement")
		if i.SettledAmount != i.CapturedAmount {
			i.AppendEvent(domain.EventReconciliation,
				"settled amount does not match captured amount")
			s.emitTransition(i, domain.StatusSettled, domain.StatusSettled,
				"reconciliation break: captured="+itoa(i.CapturedAmount)+" settled="+itoa(i.SettledAmount))
		}
		return nil
	})
	if settlementRecord != nil {
		s.Store.AddSettlement(*settlementRecord)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
}

// handleChargebackWebhook ingests a chargeback event, records the dispute row
// and transitions the intent / dispute stage accordingly.
func (s *Service) handleChargebackWebhook(w http.ResponseWriter, railVal domain.Rail, wb webhookBody) {
	stage := domain.ChargebackStage(wb.Stage)
	if stage == "" {
		stage = domain.StageOpened
	}
	if !domain.IsValidChargebackStage(stage) {
		writeError(w, http.StatusBadRequest, "invalid chargeback stage")
		return
	}

	if wb.CaseRef == "" {
		writeError(w, http.StatusBadRequest, "case_ref required for chargeback webhook")
		return
	}

	var existing *domain.Chargeback
	for _, c := range s.Store.ChargebacksFor(wb.PaymentID) {
		if c.CaseRef == wb.CaseRef {
			existing = &c
			break
		}
	}

	if existing == nil {
		cb := domain.Chargeback{
			ID:        uuid.NewString(),
			IntentID:  wb.PaymentID,
			Amount:    wb.Amount,
			Reason:    wb.Reason,
			Stage:     stage,
			CaseRef:   wb.CaseRef,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		s.Store.AddChargeback(cb)
		if stage == domain.StageOpened {
			_, err := s.Store.UpdateIntent(wb.PaymentID, func(i *domain.Intent) error {
				if i.Status != domain.StatusCaptured && i.Status != domain.StatusSettled {
					i.AppendEvent(domain.EventWebhook, string(railVal)+":chargeback (ignored)")
					return nil
				}
				prev := i.Status
				if err := i.Transition(domain.StatusChargedBack, "chargeback opened"); err != nil {
					return err
				}
				s.emitTransition(i, prev, domain.StatusChargedBack, "chargeback opened")
				return nil
			})
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
		return
	}

	// Dispute stage progression.
	_ = s.Store.UpdateChargeback(wb.CaseRef, func(c *domain.Chargeback) error {
		c.Stage = stage
		c.UpdatedAt = time.Now().UTC()
		return nil
	})
	if stage == domain.StageReversal {
		_, err := s.Store.UpdateIntent(wb.PaymentID, func(i *domain.Intent) error {
			if i.Status != domain.StatusChargedBack {
				return nil
			}
			outcome := domain.StatusChargebackLost
			detail := "chargeback lost"
			if wb.Amount == 0 {
				outcome = domain.StatusChargebackWon
				detail = "chargeback won"
			}
			prev := i.Status
			if err := i.Transition(outcome, detail); err != nil {
				return err
			}
			s.emitTransition(i, prev, outcome, detail)
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

// abs returns the absolute value of d.
func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// itoa is a tiny int64 to string helper to avoid pulling in strconv at hot
// paths; kept local for audit detail strings.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
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
	if s.Audit != nil {
		s.Audit.Emit(audit.Event{
			IntentID:  i.ID,
			FromState: from,
			ToState:   to,
			Detail:    detail,
			At:        time.Now().UTC(),
		})
	}
	s.Metrics.IncTransition(string(to))
}

// --- request logging middleware ---

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// RequestLogMiddleware returns middleware that logs each request with method,
// path, status, and duration, redacting any sensitive headers.
func (s *Service) RequestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.Logger.Info("http", map[string]interface{}{
			"method": r.Method,
			"path":   r.URL.Path,
			"status": sw.status,
			"ms":     time.Since(start).Milliseconds(),
		})
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
	mux.HandleFunc("POST /v1/payments/{id}/void", svc.Void)
	mux.HandleFunc("POST /v1/payments/{id}/refund", svc.Refund)
	mux.HandleFunc("POST /v1/payments/{id}/3ds-challenge", svc.ThreeDSChallenge)
	mux.HandleFunc("POST /v1/webhooks/{rail}", svc.Webhook)
	if svc.Metrics != nil {
		mux.Handle("GET /metrics", svc.Metrics.Handler())
	}
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
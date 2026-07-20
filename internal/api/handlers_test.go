package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/audit"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/fraud"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/rail"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
)

// newTestService returns a service backed by the given dummy adapter and a
// recorder that captures audit events.
func newTestService(t *testing.T, dummy *rail.DummyAdapter) (*Service, *audit.Recorder, store.Store) {
	t.Helper()
	st := store.New()
	registry := rail.NewRegistry(dummy)
	rec := audit.NewRecorder()
	svc := NewService(st, registry, rec, "dev-secret")
	return svc, rec, st
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path, idemKey string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("failed to decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestHealthzAndReadyz(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz code = %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("readyz code = %d", rec2.Code)
	}
	if m := decodeBody(t, rec2); m["status"] != "ready" {
		t.Fatalf("readyz status = %v", m["status"])
	}
}

func TestCreateIntentValidation(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	tests := []struct {
		name string
		key  string
		body map[string]interface{}
		want int
	}{
		{"missing idem key", "", map[string]interface{}{"rail": "card", "amount": 100, "currency": "USD", "payer_ref": "p1"}, http.StatusBadRequest},
		{"invalid rail", "k1", map[string]interface{}{"rail": "wire", "amount": 100, "currency": "USD", "payer_ref": "p1"}, http.StatusBadRequest},
		{"zero amount", "k2", map[string]interface{}{"rail": "card", "amount": 0, "currency": "USD", "payer_ref": "p1"}, http.StatusBadRequest},
		{"missing currency", "k3", map[string]interface{}{"rail": "card", "amount": 100, "payer_ref": "p1"}, http.StatusBadRequest},
		{"missing payer", "k4", map[string]interface{}{"rail": "card", "amount": 100, "currency": "USD"}, http.StatusBadRequest},
		{"3ds non-card", "k5", map[string]interface{}{"rail": "ach", "amount": 100, "currency": "USD", "payer_ref": "p1", "three_ds_required": true}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", tt.key, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("code = %d, want %d (body=%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestCreateIntentInvalidJSON(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/payments/intents", bytes.NewReader([]byte("not json")))
	req.Header.Set("Idempotency-Key", "k")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestCreateIntentSuccess(t *testing.T) {
	svc, rec, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "key-1", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	if rec1.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201 (body=%s)", rec1.Code, rec1.Body.String())
	}
	body := decodeBody(t, rec1)
	id, ok := body["id"].(string)
	if !ok || id == "" {
		t.Fatalf("missing id in response: %v", body)
	}
	if body["status"] != string(domain.StatusAuthorized) {
		t.Fatalf("status = %v, want authorized", body["status"])
	}

	// Idempotency replay should return the same id.
	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "key-1", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	if rec2.Code != http.StatusCreated {
		t.Fatalf("replay code = %d", rec2.Code)
	}
	if rec2.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("expected Idempotency-Replay header")
	}
	body2 := decodeBody(t, rec2)
	if body2["id"] != id {
		t.Fatalf("replay returned different id: %v vs %v", body2["id"], id)
	}

	// Audit event should have been emitted exactly once (for the first call).
	if got := len(rec.Events()); got != 1 {
		t.Fatalf("audit events = %d, want 1", got)
	}
}

func TestCreateIntentAuthorizeFailure(t *testing.T) {
	dummy := rail.NewDummy()
	dummy.FailAuthorize = true
	svc, rec, _ := newTestService(t, dummy)
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "key-f", map[string]interface{}{
		"rail": "card", "amount": 100, "currency": "USD", "payer_ref": "p1",
	})
	if rec1.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec1.Code)
	}
	// A failed-transition audit event should have been emitted.
	if got := len(rec.Events()); got != 1 {
		t.Fatalf("audit events = %d, want 1", got)
	}
}

func TestCreateIntent3DS(t *testing.T) {
	svc, rec, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "key-3ds", map[string]interface{}{
		"rail": "card", "amount": 500, "currency": "USD", "payer_ref": "p1", "three_ds_required": true,
	})
	if rec1.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201", rec1.Code)
	}
	body := decodeBody(t, rec1)
	if body["status"] != string(domain.Status3DSPending) {
		t.Fatalf("status = %v, want 3ds_pending", body["status"])
	}
	id := body["id"].(string)

	// Resume 3DS challenge.
	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/3ds-challenge", "key-res", map[string]interface{}{
		"challenge_result": "ok",
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	body2 := decodeBody(t, rec2)
	if body2["status"] != string(domain.StatusAuthorized) {
		t.Fatalf("status = %v, want authorized", body2["status"])
	}
	// Two audit events: created->3ds_pending, 3ds_pending->authorized.
	if got := len(rec.Events()); got != 2 {
		t.Fatalf("audit events = %d, want 2", got)
	}
}

func TestThreeDSChallengeFail(t *testing.T) {
	dummy := rail.NewDummy()
	dummy.Fail3DS = true
	svc, _, st := newTestService(t, dummy)
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 500, "currency": "USD", "payer_ref": "p1", "three_ds_required": true,
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/3ds-challenge", "k2", map[string]interface{}{
		"challenge_result": "ok",
	})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec2.Code)
	}
	if st.GetIntent(id).Status != domain.StatusFailed {
		t.Fatalf("status = %q, want failed", st.GetIntent(id).Status)
	}
}

func TestThreeDSChallengeInvalidState(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 500, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	if st.GetIntent(id).Status != domain.StatusAuthorized {
		t.Fatal("expected authorized")
	}

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/3ds-challenge", "k2", map[string]interface{}{
		"challenge_result": "ok",
	})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec2.Code)
	}
}

func TestThreeDSChallengeNotFound(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	rec := doJSON(t, mux, http.MethodPost, "/v1/payments/missing/3ds-challenge", "k", map[string]interface{}{"challenge_result": "ok"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestGetIntent(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 500, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/payments/"+id, nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
	body := decodeBody(t, rec2)
	if body["id"] != id {
		t.Fatalf("id mismatch")
	}
	if _, ok := body["history"].([]interface{}); !ok {
		t.Fatalf("history missing or wrong type: %T", body["history"])
	}

	// Not found.
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/v1/payments/missing", nil))
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec3.Code)
	}
}

func TestCaptureHappyPath(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	// Full capture.
	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c1", map[string]interface{}{})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d (body=%s)", rec2.Code, rec2.Body.String())
	}
	if st.GetIntent(id).Status != domain.StatusCaptured {
		t.Fatalf("status = %q", st.GetIntent(id).Status)
	}
	if st.GetIntent(id).CapturedAmount != 1000 {
		t.Fatalf("captured = %d", st.GetIntent(id).CapturedAmount)
	}

	// Idempotency replay.
	rec3 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c1", map[string]interface{}{})
	if rec3.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("expected replay")
	}
}

func TestCapturePartial(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c1", map[string]interface{}{"amount": 300})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
	if st.GetIntent(id).CapturedAmount != 300 {
		t.Fatalf("captured = %d", st.GetIntent(id).CapturedAmount)
	}

	// Capture the rest.
	rec3 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c2", map[string]interface{}{"amount": 700})
	if rec3.Code != http.StatusOK {
		t.Fatalf("code = %d", rec3.Code)
	}
	if st.GetIntent(id).CapturedAmount != 1000 {
		t.Fatalf("captured = %d", st.GetIntent(id).CapturedAmount)
	}
}

func TestCaptureExceedsAmount(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c1", map[string]interface{}{"amount": 2000})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec2.Code)
	}
}

func TestCaptureNonCard(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "ach", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c1", map[string]interface{}{})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (non-card capture)", rec2.Code)
	}
}

func TestCaptureWrongState(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1", "three_ds_required": true,
	})
	id := decodeBody(t, rec1)["id"].(string)
	if st.GetIntent(id).Status != domain.Status3DSPending {
		t.Fatal("expected 3ds_pending")
	}

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c1", map[string]interface{}{})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 (not authorized)", rec2.Code)
	}
}

func TestCaptureNotFound(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	rec := doJSON(t, mux, http.MethodPost, "/v1/payments/missing/capture", "c", map[string]interface{}{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestCaptureRailFailure(t *testing.T) {
	dummy := rail.NewDummy()
	dummy.FailCapture = true
	svc, _, st := newTestService(t, dummy)
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c1", map[string]interface{}{})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec2.Code)
	}
	if st.GetIntent(id).Status != domain.StatusAuthorized {
		t.Fatalf("status should remain authorized, got %q", st.GetIntent(id).Status)
	}
}

func TestRefundHappyPath(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/refund", "r1", map[string]interface{}{})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d (body=%s)", rec2.Code, rec2.Body.String())
	}
	if st.GetIntent(id).Status != domain.StatusRefunded {
		t.Fatalf("status = %q, want refunded", st.GetIntent(id).Status)
	}
}

func TestRefundPartial(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/refund", "r1", map[string]interface{}{"amount": 400})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d", rec2.Code)
	}
	if st.GetIntent(id).RefundedAmount != 400 {
		t.Fatalf("refunded = %d", st.GetIntent(id).RefundedAmount)
	}
	if st.GetIntent(id).Status != domain.StatusRefunded {
		t.Fatalf("status = %q, want refunded", st.GetIntent(id).Status)
	}
}

func TestRefundExceedsCaptured(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/refund", "r1", map[string]interface{}{"amount": 2000})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec2.Code)
	}
}

func TestRefundWrongState(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	// Still authorized, not captured.
	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/refund", "r1", map[string]interface{}{})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec2.Code)
	}
}

func TestRefundNotFound(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	rec := doJSON(t, mux, http.MethodPost, "/v1/payments/missing/refund", "r", map[string]interface{}{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestRefundRailFailure(t *testing.T) {
	dummy := rail.NewDummy()
	dummy.FailRefund = true
	svc, _, _ := newTestService(t, dummy)
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/refund", "r1", map[string]interface{}{})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec2.Code)
	}
}

// --- webhook tests ---

func signBody(key, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookSuccess(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	// Create an intent to attach the webhook to.
	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	payload := map[string]interface{}{"payment_id": id, "type": "settlement"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}

	// The intent should now have a webhook event in its history.
	i := st.GetIntent(id)
	found := false
	for _, e := range i.History {
		if e.Type == domain.EventWebhook {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected webhook event in history")
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	body, _ := json.Marshal(map[string]interface{}{"payment_id": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", "bad")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestWebhookMissingSignature(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	body, _ := json.Marshal(map[string]interface{}{"payment_id": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestWebhookSHA256Prefix(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	body, _ := json.Marshal(map[string]interface{}{"type": "ping"})
	sig := "sha256=" + signBody([]byte("dev-secret"), body)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", sig)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
}

func TestWebhookUnknownRail(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	body, _ := json.Marshal(map[string]interface{}{})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/wire", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestWebhookPaymentIDNotFound(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	body, _ := json.Marshal(map[string]interface{}{"payment_id": "missing", "type": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	// Unknown payment_id should still return 200 (we don't reject unknown).
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
}

func TestVerifyHMAC(t *testing.T) {
	key := []byte("secret")
	body := []byte("hello")
	sig := signBody(key, body)
	if !verifyHMAC(key, body, sig) {
		t.Fatal("expected valid HMAC")
	}
	if verifyHMAC(key, body, "bad") {
		t.Fatal("expected invalid HMAC")
	}
	if verifyHMAC(key, body, "") {
		t.Fatal("empty sig should fail")
	}
	if verifyHMAC(key, body, "sha256="+sig) {
		// prefixed form should also work
	} else {
		t.Fatal("prefixed sig should work")
	}
}

// --- helpers ---

func TestMustJSON(t *testing.T) {
	b := MustJSON(map[string]string{"k": "v"})
	if string(b) != `{"k":"v"}` {
		t.Fatalf("got %s", b)
	}
}

func TestAssertBodyContains(t *testing.T) {
	if !AssertBodyContains([]byte("hello world"), "world") {
		t.Fatal("expected true")
	}
	if AssertBodyContains([]byte("hello"), "world") {
		t.Fatal("expected false")
	}
}

// --- full lifecycle integration test ---

func TestFullLifecycleIntegration(t *testing.T) {
	svc, rec, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ts.Client()

	// Create with 3DS.
	body, _ := json.Marshal(map[string]interface{}{
		"rail": "card", "amount": 2000, "currency": "USD", "payer_ref": "p1", "three_ds_required": true,
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/payments/intents", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "life")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var intent domain.Intent
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	json.Unmarshal(respBody, &intent)
	id := intent.ID
	if intent.Status != domain.Status3DSPending {
		t.Fatalf("status = %q", intent.Status)
	}

	// 3DS challenge.
	body, _ = json.Marshal(map[string]interface{}{"challenge_result": "verified"})
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/payments/"+id+"/3ds-challenge", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "life-3ds")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("3ds status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if st.GetIntent(id).Status != domain.StatusAuthorized {
		t.Fatalf("status = %q", st.GetIntent(id).Status)
	}

	// Full capture (partial capture leaves intent authorized).
	body, _ = json.Marshal(map[string]interface{}{"amount": 2000})
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/payments/"+id+"/capture", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "life-cap")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("capture status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if st.GetIntent(id).CapturedAmount != 2000 {
		t.Fatalf("captured = %d", st.GetIntent(id).CapturedAmount)
	}
	if st.GetIntent(id).Status != domain.StatusCaptured {
		t.Fatalf("status = %q, want captured", st.GetIntent(id).Status)
	}

	// Full refund.
	body, _ = json.Marshal(map[string]interface{}{})
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/payments/"+id+"/refund", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "life-ref")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refund status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if st.GetIntent(id).Status != domain.StatusRefunded {
		t.Fatalf("final status = %q, want refunded", st.GetIntent(id).Status)
	}

	// Mutations after terminal state should be rejected.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/payments/"+id+"/refund", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "life-ref2")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("post-terminal refund status = %d, want 409", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Audit events: created->3ds_pending, 3ds_pending->authorized, authorized->captured(ish via refunding), refunded.
	// At least 4 transitions.
	if got := len(rec.Events()); got < 4 {
		t.Fatalf("audit events = %d, want >= 4", got)
	}

	// GET returns the lifecycle.
	resp, err = client.Get(ts.URL + "/v1/payments/" + id)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	var final domain.Intent
	finalBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	json.Unmarshal(finalBody, &final)
	if final.Status != domain.StatusRefunded {
		t.Fatalf("final status = %q", final.Status)
	}
	if len(final.History) < 4 {
		t.Fatalf("history len = %d, want >= 4", len(final.History))
	}
}

func TestRoutingUnknown(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

// --- void endpoint tests ---

func TestVoidHappyPath(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/void", "v1", map[string]interface{}{})
	if rec2.Code != http.StatusOK {
		t.Fatalf("code = %d (body=%s)", rec2.Code, rec2.Body.String())
	}
	if st.GetIntent(id).Status != domain.StatusVoided {
		t.Fatalf("status = %q, want voided", st.GetIntent(id).Status)
	}

	// Idempotency replay.
	rec3 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/void", "v1", map[string]interface{}{})
	if rec3.Header().Get("Idempotency-Replay") != "true" {
		t.Fatal("expected replay")
	}
}

func TestVoidWrongState(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/void", "v1", map[string]interface{}{})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 (void after capture)", rec2.Code)
	}
}

func TestVoidRailFailure(t *testing.T) {
	dummy := rail.NewDummy()
	dummy.FailVoid = true
	svc, _, _ := newTestService(t, dummy)
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)

	rec2 := doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/void", "v1", map[string]interface{}{})
	if rec2.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", rec2.Code)
	}
}

func TestVoidNotFound(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	rec := doJSON(t, mux, http.MethodPost, "/v1/payments/missing/void", "v", map[string]interface{}{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

// --- instant rail tests ---

func TestCreateIntentInstantRail(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "pix-1", map[string]interface{}{
		"rail": "pix", "amount": 500, "currency": "BRL", "payer_ref": "p1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d (body=%s)", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	id := body["id"].(string)
	if body["status"] != string(domain.StatusCaptured) {
		t.Fatalf("status = %v, want captured (instant rail collapse)", body["status"])
	}
	if st.GetIntent(id).CapturedAmount != 500 {
		t.Fatalf("captured = %d, want 500", st.GetIntent(id).CapturedAmount)
	}
	caps := st.CapturesFor(id)
	if len(caps) != 1 {
		t.Fatalf("captures = %d, want 1", len(caps))
	}
}

func TestCreateIntentUPIInstant(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	rec := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "upi-1", map[string]interface{}{
		"rail": "upi", "amount": 300, "currency": "INR", "payer_ref": "p1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d", rec.Code)
	}
	if decodeBody(t, rec)["status"] != string(domain.StatusCaptured) {
		t.Fatal("UPI should collapse to captured")
	}
}

// --- fraud gating tests ---

func TestCreateIntentFraudBlocked(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	svc.Fraud = &fraud.DummyClient{FailScore: true}
	mux := NewMux(svc)

	rec := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 500, "currency": "USD", "payer_ref": "p1",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (fraud blocked)", rec.Code)
	}
	body := decodeBody(t, rec)
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatal("expected error message in body")
	}
}

// --- settlement webhook tests ---

func TestWebhookSettlementTransition(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	payload := map[string]interface{}{
		"payment_id": id, "type": "settlement", "amount": 1000,
		"external_event_id": "evt-set-1", "rail_ref": "ref-1",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if st.GetIntent(id).Status != domain.StatusSettled {
		t.Fatalf("status = %q, want settled", st.GetIntent(id).Status)
	}
	if st.GetIntent(id).SettledAmount != 1000 {
		t.Fatalf("settled = %d", st.GetIntent(id).SettledAmount)
	}
	if len(st.SettlementsFor(id)) != 1 {
		t.Fatalf("settlements = %d, want 1", len(st.SettlementsFor(id)))
	}
}

func TestWebhookSettlementReconciliationBreak(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	payload := map[string]interface{}{
		"payment_id": id, "type": "settlement", "amount": 900,
		"external_event_id": "evt-set-break", "rail_ref": "ref-1",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	mux.ServeHTTP(httptest.NewRecorder(), req)

	i := st.GetIntent(id)
	found := false
	for _, e := range i.History {
		if e.Type == domain.EventReconciliation {
			found = true
		}
	}
	if !found {
		t.Fatal("expected reconciliation break event in history")
	}
}

// --- webhook dedup tests ---

func TestWebhookDedup(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	payload := map[string]interface{}{"type": "ping", "external_event_id": "evt-dup-1"}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first code = %d", rec.Code)
	}
	body1 := rec.Body.String()

	// Replay same event id.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req2.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second code = %d", rec2.Code)
	}
	if decodeBody(t, rec2)["status"] != "duplicate" {
		t.Fatalf("second status = %v, want duplicate", decodeBody(t, rec2)["status"])
	}
	_ = body1
}

// --- replay window tests ---

func TestWebhookReplayWindow(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	old := time.Now().Unix() - 3600 // 1h ago
	payload := map[string]interface{}{"type": "ping", "timestamp": old}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (outside replay window)", rec.Code)
	}
}

func TestWebhookWithinReplayWindow(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	now := time.Now().Unix()
	payload := map[string]interface{}{"type": "ping", "timestamp": now}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (within replay window)", rec.Code)
	}
}

// --- chargeback webhook tests ---

func TestWebhookChargebackOpened(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	payload := map[string]interface{}{
		"payment_id": id, "type": "chargeback", "amount": 1000,
		"reason": "fraud", "stage": "opened", "case_ref": "case-1",
		"external_event_id": "evt-cb-1",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if st.GetIntent(id).Status != domain.StatusChargedBack {
		t.Fatalf("status = %q, want charged_back", st.GetIntent(id).Status)
	}
	cbs := st.ChargebacksFor(id)
	if len(cbs) != 1 || cbs[0].CaseRef != "case-1" {
		t.Fatalf("chargebacks = %v", cbs)
	}
}

func TestWebhookChargebackReversalLost(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	// Open.
	open := map[string]interface{}{
		"payment_id": id, "type": "chargeback", "amount": 1000,
		"stage": "opened", "case_ref": "case-2", "external_event_id": "evt-open",
	}
	body, _ := json.Marshal(open)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	mux.ServeHTTP(httptest.NewRecorder(), req)

	// Reversal (lost, amount > 0).
	rev := map[string]interface{}{
		"payment_id": id, "type": "chargeback", "amount": 1000,
		"stage": "reversal", "case_ref": "case-2", "external_event_id": "evt-rev",
	}
	body2, _ := json.Marshal(rev)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body2))
	req2.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body2))
	mux.ServeHTTP(httptest.NewRecorder(), req2)

	if st.GetIntent(id).Status != domain.StatusChargebackLost {
		t.Fatalf("status = %q, want chargeback_lost", st.GetIntent(id).Status)
	}
}

func TestWebhookChargebackReversalWon(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec1 := doJSON(t, mux, http.MethodPost, "/v1/payments/intents", "k", map[string]interface{}{
		"rail": "card", "amount": 1000, "currency": "USD", "payer_ref": "p1",
	})
	id := decodeBody(t, rec1)["id"].(string)
	doJSON(t, mux, http.MethodPost, "/v1/payments/"+id+"/capture", "c", map[string]interface{}{})

	open := map[string]interface{}{
		"payment_id": id, "type": "chargeback", "amount": 1000,
		"stage": "opened", "case_ref": "case-3", "external_event_id": "evt-open3",
	}
	body, _ := json.Marshal(open)
	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body))
	mux.ServeHTTP(httptest.NewRecorder(), req)

	// Reversal won (amount = 0).
	rev := map[string]interface{}{
		"payment_id": id, "type": "chargeback", "amount": 0,
		"stage": "reversal", "case_ref": "case-3", "external_event_id": "evt-rev3",
	}
	body2, _ := json.Marshal(rev)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/webhooks/card", bytes.NewReader(body2))
	req2.Header.Set("X-Webhook-Signature", signBody([]byte("dev-secret"), body2))
	mux.ServeHTTP(httptest.NewRecorder(), req2)

	if st.GetIntent(id).Status != domain.StatusChargebackWon {
		t.Fatalf("status = %q, want chargeback_won", st.GetIntent(id).Status)
	}
}

// --- metrics endpoint test ---

func TestMetricsEndpoint(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !AssertBodyContains([]byte(body), "transitions") {
		t.Fatalf("metrics body missing transitions: %s", body)
	}
}

// --- request log middleware test ---

func TestRequestLogMiddleware(t *testing.T) {
	svc, _, _ := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	wrapped := svc.RequestLogMiddleware(mux)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}
func TestListPayments(t *testing.T) {
	svc, _, st := newTestService(t, rail.NewDummy())
	mux := NewMux(svc)
	st.CreateIntent(&domain.Intent{ID: "p1", Rail: domain.RailCard, Amount: 1000, Currency: "USD", PayerRef: "payer-1", Status: domain.StatusIntent, CreatedAt: time.Now()})
	st.CreateIntent(&domain.Intent{ID: "p2", Rail: domain.RailACH, Amount: 2000, Currency: "USD", PayerRef: "payer-2", Status: domain.StatusSettled, CreatedAt: time.Now()})

	rec := doJSON(t, mux, http.MethodGet, "/v1/payments", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := decodeBody(t, rec)
	payments := body["payments"].([]interface{})
	if len(payments) != 2 {
		t.Fatalf("payments=%d want 2", len(payments))
	}

	rec = doJSON(t, mux, http.MethodGet, "/v1/payments?rail=card", "", nil)
	body = decodeBody(t, rec)
	payments = body["payments"].([]interface{})
	if len(payments) != 1 {
		t.Fatalf("rail=card filter: got %d want 1", len(payments))
	}

	rec = doJSON(t, mux, http.MethodGet, "/v1/payments?status=settled", "", nil)
	body = decodeBody(t, rec)
	payments = body["payments"].([]interface{})
	if len(payments) != 1 {
		t.Fatalf("status=settled filter: got %d want 1", len(payments))
	}
}

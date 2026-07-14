package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsTransitions(t *testing.T) {
	m := New()
	m.IncTransition("captured")
	m.IncTransition("captured")
	m.IncTransition("settled")
	out := m.FormatPrometheus()
	if !strings.Contains(out, "payment_transitions_total{state=\"captured\"} 2") {
		t.Fatalf("expected captured counter=2: %s", out)
	}
	if !strings.Contains(out, "payment_transitions_total{state=\"settled\"} 1") {
		t.Fatalf("expected settled counter=1: %s", out)
	}
}

func TestMetricsWebhookBacklog(t *testing.T) {
	m := New()
	m.IncWebhookBacklog()
	m.IncWebhookBacklog()
	m.DecWebhookBacklog()
	out := m.FormatPrometheus()
	if !strings.Contains(out, "payment_webhook_backlog 1") {
		t.Fatalf("expected backlog 1: %s", out)
	}
}

func TestMetricsLatencyP99(t *testing.T) {
	m := New()
	for i := 0; i < 100; i++ {
		m.ObserveIntentCreation(time.Duration(i) * time.Millisecond)
	}
	out := m.FormatPrometheus()
	if !strings.Contains(out, "payment_intent_creation_p99_latency_seconds") {
		t.Fatalf("expected p99 line: %s", out)
	}
}

func TestMetricsHandler(t *testing.T) {
	m := New()
	m.IncTransition("authorized")
	m.SetWebhookBacklog(3)
	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "webhook_backlog") || !strings.Contains(body, "authorized") {
		t.Fatalf("unexpected metrics body: %s", body)
	}
}
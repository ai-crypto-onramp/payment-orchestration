package metrics

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics is a minimal in-memory metrics registry. It tracks transition
// counters per state, webhook backlog depth, and a rolling latency histogram
// for intent creation. It is safe for concurrent use.
type Metrics struct {
	transitions   map[string]*atomic.Int64
	webhookBacklog atomic.Int64
	mu            sync.Mutex
	latencies     []time.Duration
}

// New returns an empty Metrics registry.
func New() *Metrics {
	return &Metrics{transitions: make(map[string]*atomic.Int64)}
}

// IncTransition increments the counter for the to-state.
func (m *Metrics) IncTransition(to string) {
	m.mu.Lock()
	c, ok := m.transitions[to]
	if !ok {
		c = new(atomic.Int64)
		m.transitions[to] = c
	}
	m.mu.Unlock()
	c.Add(1)
}

// SetWebhookBacklog sets the current webhook backlog depth.
func (m *Metrics) SetWebhookBacklog(n int64) { m.webhookBacklog.Store(n) }

// IncWebhookBacklog adds one to the webhook backlog.
func (m *Metrics) IncWebhookBacklog() { m.webhookBacklog.Add(1) }

// DecWebhookBacklog subtracts one from the webhook backlog.
func (m *Metrics) DecWebhookBacklog() { m.webhookBacklog.Add(-1) }

// ObserveIntentCreation records an intent creation latency sample.
func (m *Metrics) ObserveIntentCreation(d time.Duration) {
	m.mu.Lock()
	m.latencies = append(m.latencies, d)
	if len(m.latencies) > 1000 {
		m.latencies = m.latencies[len(m.latencies)-1000:]
	}
	m.mu.Unlock()
}

// p99 returns the 99th percentile of the recorded latencies.
func (m *Metrics) p99() time.Duration {
	m.mu.Lock()
	if len(m.latencies) == 0 {
		m.mu.Unlock()
		return 0
	}
	sorted := make([]time.Duration, len(m.latencies))
	copy(sorted, m.latencies)
	m.mu.Unlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)) * 0.99)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Handler returns an http.HandlerFunc that exposes the metrics as JSON.
func (m *Metrics) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		transitions := make(map[string]int64, len(m.transitions))
		for k, v := range m.transitions {
			transitions[k] = v.Load()
		}
		m.mu.Unlock()
		out := map[string]interface{}{
			"transitions":         transitions,
			"webhook_backlog":     m.webhookBacklog.Load(),
			"intent_p99_latency": m.p99().String(),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// FormatPrometheus returns a text-based representation of the metrics in a
// Prometheus-like exposition format.
func (m *Metrics) FormatPrometheus() string {
	m.mu.Lock()
	transitions := make(map[string]int64, len(m.transitions))
	for k, v := range m.transitions {
		transitions[k] = v.Load()
	}
	m.mu.Unlock()
	var b strings.Builder
	b.WriteString("# HELP payment_transitions_total Number of state transitions by target state.\n")
	b.WriteString("# TYPE payment_transitions_total counter\n")
	for k, v := range transitions {
		b.WriteString("payment_transitions_total{state=\"" + k + "\"} " + intToStr(v) + "\n")
	}
	b.WriteString("# HELP payment_webhook_backlog Current number of unprocessed webhooks.\n")
	b.WriteString("# TYPE payment_webhook_backlog gauge\n")
	b.WriteString("payment_webhook_backlog " + intToStr(m.webhookBacklog.Load()) + "\n")
	b.WriteString("# HELP payment_intent_creation_p99_latency p99 latency of intent creation.\n")
	b.WriteString("# TYPE payment_intent_creation_p99_latency gauge\n")
	b.WriteString("payment_intent_creation_p99_latency_seconds " + floatToStr(m.p99().Seconds()) + "\n")
	return b.String()
}

func intToStr(n int64) string {
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

func floatToStr(f float64) string {
	if f == 0 {
		return "0"
	}
	const digits = "0123456789"
	var b []byte
	if f < 0 {
		b = append(b, '-')
		f = -f
	}
	whole := int64(f)
	frac := f - float64(whole)
	b = appendInt(b, whole)
	b = append(b, '.')
	for i := 0; i < 6; i++ {
		frac *= 10
		d := int64(frac)
		b = append(b, digits[d])
		frac -= float64(d)
	}
	return string(b)
}

func appendInt(b []byte, n int64) []byte {
	if n == 0 {
		return append(b, '0')
	}
	var tmp []byte
	for n > 0 {
		tmp = append([]byte{byte('0' + n%10)}, tmp...)
		n /= 10
	}
	return append(b, tmp...)
}
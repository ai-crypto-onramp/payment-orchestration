package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/segmentio/kafka-go"
)

const AuditTopic = "audit.v1"

// Event is an audit record emitted on each state transition.
type Event struct {
	IntentID  string          `json:"intent_id"`
	FromState domain.Status   `json:"from_state"`
	ToState   domain.Status   `json:"to_state"`
	Detail    string          `json:"detail,omitempty"`
	At        time.Time       `json:"at"`
}

// Sink receives audit events. Implementations must be safe for concurrent use.
type Sink interface {
	Emit(Event)
}

// Recorder is an in-memory Sink used in tests and local dev.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// NewRecorder returns an empty in-memory audit recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Emit records the event.
func (r *Recorder) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	r.events = append(r.events, e)
}

// Events returns a copy of the recorded events.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// NopSink discards all events.
type NopSink struct{}

func (NopSink) Emit(Event) {}

// KafkaSink publishes the canonical audit.v1 envelope (see
// .github/contracts/asyncapi/audit/v1/asyncapi.yaml) for every state transition.
type KafkaSink struct {
	writer *kafka.Writer
}

func NewKafkaSink(brokers []string) (*KafkaSink, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("audit kafka: no brokers provided")
	}
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        AuditTopic,
		Balancer:     &kafka.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
		RequiredAcks: kafka.RequireAll,
	}
	return &KafkaSink{writer: w}, nil
}

func (s *KafkaSink) Close() error {
	if s.writer == nil {
		return nil
	}
	return s.writer.Close()
}

func (s *KafkaSink) Emit(e Event) {
	if s.writer == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	sum := sha256.Sum256(payload)
	payloadHash := "sha256:" + hex.EncodeToString(sum[:])
	envelope := map[string]any{
		"schema_version": "1",
		"id":              fmt.Sprintf("pay-%d", e.At.UnixNano()),
		"ts":              e.At.UTC().Format(time.RFC3339Nano),
		"source_service":  "payment-orchestration",
		"actor_id":        "payment-orchestration",
		"action":          "payment.state_transition",
		"target_type":     "intent",
		"target_id":       e.IntentID,
		"payload_hash":    payloadHash,
		"payload":         json.RawMessage(payload),
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	_ = s.writer.WriteMessages(context.Background(), kafka.Message{
		Key:   []byte(e.IntentID),
		Value: body,
	})
}
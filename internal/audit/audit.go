package audit

import (
	"sync"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

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
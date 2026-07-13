package audit

import (
	"testing"
)

func TestRecorderEmitAndEvents(t *testing.T) {
	r := NewRecorder()
	r.Emit(Event{IntentID: "i1", FromState: "intent", ToState: "authorized"})
	r.Emit(Event{IntentID: "i1", FromState: "authorized", ToState: "captured", Detail: "partial"})

	got := r.Events()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ToState != "authorized" {
		t.Fatalf("first to_state = %q, want authorized", got[0].ToState)
	}
	if got[1].Detail != "partial" {
		t.Fatalf("second detail = %q, want partial", got[1].Detail)
	}
	if got[1].At.IsZero() {
		t.Fatal("At should be set")
	}

	// Events returns a copy; mutating it should not affect the recorder.
	got[0].IntentID = "mutated"
	again := r.Events()
	if again[0].IntentID == "mutated" {
		t.Fatal("Events should return a copy")
	}
}

func TestNopSink(t *testing.T) {
	NopSink{}.Emit(Event{})
}
package domain

import (
	"testing"
)

func TestIsValidRail(t *testing.T) {
	tests := []struct {
		rail Rail
		want bool
	}{
		{RailCard, true},
		{RailACH, true},
		{RailSEPA, true},
		{RailPIX, true},
		{RailUPI, true},
		{Rail("wire"), false},
		{Rail(""), false},
	}
	for _, tt := range tests {
		if got := IsValidRail(tt.rail); got != tt.want {
			t.Errorf("IsValidRail(%q) = %v, want %v", tt.rail, got, tt.want)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	terminal := []Status{StatusSettled, StatusRefunded, StatusVoided, StatusFailed}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%q should be terminal", s)
		}
	}
	nonTerminal := []Status{StatusIntent, StatusAuthorized, Status3DSPending, StatusCaptured, StatusRefunding}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%q should not be terminal", s)
		}
	}
}

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to Status
		want     bool
	}{
		{StatusIntent, StatusAuthorized, true},
		{StatusIntent, Status3DSPending, true},
		{StatusIntent, StatusFailed, true},
		{StatusIntent, StatusCaptured, false},
		{StatusAuthorized, StatusCaptured, true},
		{StatusAuthorized, Status3DSPending, true},
		{StatusAuthorized, StatusVoided, true},
		{StatusAuthorized, StatusFailed, true},
		{StatusAuthorized, StatusRefunded, false},
		{Status3DSPending, StatusAuthorized, true},
		{Status3DSPending, StatusFailed, true},
		{Status3DSPending, StatusCaptured, false},
		{StatusCaptured, StatusSettled, true},
		{StatusCaptured, StatusRefunding, true},
		{StatusCaptured, StatusRefunded, true},
		{StatusCaptured, StatusAuthorized, false},
		{StatusSettled, StatusRefunding, true},
		{StatusSettled, StatusRefunded, true},
		{StatusSettled, StatusCaptured, false},
		{StatusRefunding, StatusRefunded, true},
		{StatusRefunding, StatusFailed, true},
		{StatusRefunded, StatusAuthorized, false},
		{StatusFailed, StatusAuthorized, false},
	}
	for _, tt := range tests {
		if got := tt.from.CanTransition(tt.to); got != tt.want {
			t.Errorf("CanTransition(%q -> %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestIntentTransition(t *testing.T) {
	i := &Intent{Status: StatusIntent}
	if err := i.Transition(StatusCaptured, "skip"); err == nil {
		t.Fatal("expected invalid transition error")
	}
	if err := i.Transition(StatusAuthorized, "authorized"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if i.Status != StatusAuthorized {
		t.Fatalf("status = %q, want %q", i.Status, StatusAuthorized)
	}
	if len(i.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(i.History))
	}
	if i.History[0].Type != EventAuthorized {
		t.Fatalf("event type = %q, want %q", i.History[0].Type, EventAuthorized)
	}

	if err := i.Transition(StatusCaptured, "captured"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := i.Transition(StatusAuthorized, "from captured"); err == nil {
		t.Fatal("expected invalid transition from captured back to authorized")
	}
	if err := i.Transition(StatusSettled, "settled"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// terminal
	if err := i.Transition(StatusAuthorized, "after settled"); err != ErrTerminalState {
		t.Fatalf("expected ErrTerminalState, got %v", err)
	}
}

func TestIntentAppendEvent(t *testing.T) {
	i := &Intent{}
	before := i.UpdatedAt
	i.AppendEvent(EventCreated, "created")
	if len(i.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(i.History))
	}
	if !i.UpdatedAt.After(before) {
		t.Fatal("UpdatedAt should advance")
	}
}
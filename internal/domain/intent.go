package domain

import (
	"errors"
	"time"
)

// Status is the lifecycle state of a payment intent.
type Status string

const (
	StatusIntent          Status = "intent"
	StatusAuthorized      Status = "authorized"
	Status3DSPending      Status = "3ds_pending"
	StatusCaptured        Status = "captured"
	StatusSettled         Status = "settled"
	StatusRefunding       Status = "refunding"
	StatusRefunded        Status = "refunded"
	StatusVoided          Status = "voided"
	StatusFailed          Status = "failed"
	StatusChargedBack     Status = "charged_back"
	StatusChargebackWon   Status = "chargeback_won"
	StatusChargebackLost  Status = "chargeback_lost"
)

// IsTerminal reports whether s is a terminal state.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSettled, StatusRefunded, StatusVoided, StatusFailed,
		StatusChargebackWon, StatusChargebackLost:
		return true
	}
	return false
}

// Rail is the fiat rail used for a payment.
type Rail string

const (
	RailCard Rail = "card"
	RailACH  Rail = "ach"
	RailSEPA Rail = "sepa"
	RailPIX  Rail = "pix"
	RailUPI  Rail = "upi"
)

// IsValidRail reports whether r is a recognized rail.
func IsValidRail(r Rail) bool {
	switch r {
	case RailCard, RailACH, RailSEPA, RailPIX, RailUPI:
		return true
	}
	return false
}

// IsInstant reports whether r is an instant rail that collapses auth+capture
// into a single submit step.
func (r Rail) IsInstant() bool {
	switch r {
	case RailPIX, RailUPI:
		return true
	}
	return false
}

// EventType is the category of a lifecycle event.
type EventType string

const (
	EventCreated         EventType = "created"
	EventAuthorized      EventType = "authorized"
	Event3DSPending      EventType = "3ds_pending"
	Event3DSChallenged   EventType = "3ds_challenged"
	Event3DSVerified     EventType = "3ds_verified"
	EventCaptured        EventType = "captured"
	EventSettled         EventType = "settled"
	EventRefunded        EventType = "refunded"
	EventVoided          EventType = "voided"
	EventFailed          EventType = "failed"
	EventWebhook         EventType = "webhook"
	EventChargedBack     EventType = "charged_back"
	EventChargebackWon   EventType = "chargeback_won"
	EventChargebackLost  EventType = "chargeback_lost"
	EventReconciliation  EventType = "reconciliation_break"
)

// Event is an entry in an intent's lifecycle history.
type Event struct {
	Type   EventType `json:"type"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
	Amount int64     `json:"amount,omitempty"`
}

// Intent is a normalized payment intent.
type Intent struct {
	ID              string    `json:"id"`
	Rail            Rail      `json:"rail"`
	Amount          int64     `json:"amount"`
	Currency        string    `json:"currency"`
	PayerRef        string    `json:"payer_ref"`
	Status          Status    `json:"status"`
	CapturedAmount  int64     `json:"captured_amount,omitempty"`
	RefundedAmount  int64     `json:"refunded_amount,omitempty"`
	SettledAmount   int64     `json:"settled_amount,omitempty"`
	ExternalID      string    `json:"external_id,omitempty"`
	IdempotencyKey  string    `json:"-"`
	ThreeDSRequired bool      `json:"three_ds_required,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	History         []Event   `json:"history"`
}

// AppendEvent records a lifecycle event and updates the intent timestamp.
func (i *Intent) AppendEvent(t EventType, detail string) {
	i.History = append(i.History, Event{Type: t, Detail: detail, At: time.Now().UTC()})
	i.UpdatedAt = time.Now().UTC()
}

// State-machine validation errors.
var (
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrTerminalState     = errors.New("intent is in a terminal state")
)

// transitions maps each state to the set of states it may move to. It
// encodes the state machine from the README diagram.
var transitions = map[Status]map[Status]bool{
	StatusIntent: {
		StatusAuthorized: true,
		Status3DSPending: true,
		StatusFailed:      true,
	},
	StatusAuthorized: {
		StatusCaptured:    true,
		Status3DSPending:  true,
		StatusVoided:       true,
		StatusFailed:       true,
	},
	Status3DSPending: {
		StatusAuthorized: true,
		StatusFailed:      true,
	},
	StatusCaptured: {
		StatusSettled:    true,
		StatusRefunding:  true,
		StatusRefunded:   true,
		StatusChargedBack: true,
	},
	StatusRefunding: {
		StatusRefunded: true,
		StatusFailed:   true,
	},
	StatusSettled: {
		StatusRefunding:  true,
		StatusRefunded:   true,
		StatusChargedBack: true,
	},
	StatusChargedBack: {
		StatusChargebackWon:  true,
		StatusChargebackLost: true,
	},
}

// CanTransition reports whether transitioning from s to next is allowed.
func (s Status) CanTransition(next Status) bool {
	allowed, ok := transitions[s]
	if !ok {
		return false
	}
	return allowed[next]
}

// Transition validates and applies a state transition, recording a lifecycle event.
func (i *Intent) Transition(next Status, detail string) error {
	if i.Status.IsTerminal() {
		return ErrTerminalState
	}
	if !i.Status.CanTransition(next) {
		return ErrInvalidTransition
	}
	prev := i.Status
	i.Status = next
	i.AppendEvent(EventType(next), detail)
	_ = prev
	return nil
}
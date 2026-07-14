package domain

import "time"

// ChargebackStage is the lifecycle stage of a dispute.
type ChargebackStage string

const (
	StageOpened       ChargebackStage = "opened"
	StageEvidence     ChargebackStage = "evidence"
	StageArbitration  ChargebackStage = "arbitration"
	StageReversal     ChargebackStage = "reversal"
)

// IsValidChargebackStage reports whether s is a recognized dispute stage.
func IsValidChargebackStage(s ChargebackStage) bool {
	switch s {
	case StageOpened, StageEvidence, StageArbitration, StageReversal:
		return true
	}
	return false
}

// Chargeback is a dispute record linked to an intent.
type Chargeback struct {
	ID        string          `json:"id"`
	IntentID  string          `json:"intent_id"`
	Amount    int64           `json:"amount"`
	Reason    string          `json:"reason"`
	Stage     ChargebackStage `json:"stage"`
	CaseRef   string          `json:"case_ref"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Settlement is a settlement record linked to an intent's capture.
type Settlement struct {
	ID            string    `json:"id"`
	IntentID      string    `json:"intent_id"`
	SettledAmount int64     `json:"settled_amount"`
	SettledAt     time.Time `json:"settled_at"`
	RailRef       string    `json:"rail_ref"`
}

// Capture is a capture record linked to an intent.
type Capture struct {
	ID         string    `json:"id"`
	IntentID   string    `json:"intent_id"`
	Amount     int64     `json:"amount"`
	ExternalRef string   `json:"external_ref"`
	CapturedAt time.Time `json:"captured_at"`
}

// Refund is a refund record linked to an intent's capture.
type Refund struct {
	ID          string    `json:"id"`
	IntentID    string    `json:"intent_id"`
	Amount      int64     `json:"amount"`
	ExternalRef string    `json:"external_ref"`
	RefundedAt  time.Time `json:"refunded_at"`
	State       string    `json:"state"`
}

// Webhook is an inbound webhook record persisted before processing.
type Webhook struct {
	ID              string    `json:"id"`
	Rail            Rail      `json:"rail"`
	ExternalEventID string    `json:"external_event_id"`
	Signature       string    `json:"signature,omitempty"`
	ReceivedAt      time.Time `json:"received_at"`
	ProcessedAt     time.Time `json:"processed_at,omitempty"`
}
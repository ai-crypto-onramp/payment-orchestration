package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

type Store interface {
	CreateIntent(i *domain.Intent)
	GetIntent(id string) *domain.Intent
	ListIntents(status string, rail string) []*domain.Intent
	UpdateIntent(id string, fn func(*domain.Intent) error) (*domain.Intent, error)
	AddCapture(c domain.Capture)
	CapturesFor(intentID string) []domain.Capture
	AddSettlement(set domain.Settlement)
	SettlementsFor(intentID string) []domain.Settlement
	AddRefund(r domain.Refund)
	RefundsFor(intentID string) []domain.Refund
	AddChargeback(c domain.Chargeback)
	ChargebacksFor(intentID string) []domain.Chargeback
	UpdateChargeback(caseRef string, fn func(*domain.Chargeback) error) error
	RecordWebhook(w domain.Webhook) error
	MarkWebhookProcessed(rail domain.Rail, externalEventID string)
	WebhookExists(rail domain.Rail, externalEventID string) bool
}

type MemStore struct {
	mu          sync.RWMutex
	intents     map[string]*domain.Intent
	captures    map[string][]domain.Capture
	settlements map[string][]domain.Settlement
	refunds     map[string][]domain.Refund
	chargebacks map[string][]domain.Chargeback
	webhooks    map[string]domain.Webhook
}

func New() *MemStore {
	return &MemStore{
		intents:     make(map[string]*domain.Intent),
		captures:    make(map[string][]domain.Capture),
		settlements: make(map[string][]domain.Settlement),
		refunds:     make(map[string][]domain.Refund),
		chargebacks: make(map[string][]domain.Chargeback),
		webhooks:    make(map[string]domain.Webhook),
	}
}

// CreateIntent stores i. It does not check for duplicates.
func (s *MemStore) CreateIntent(i *domain.Intent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intents[i.ID] = i
}

// GetIntent returns a copy of the intent with id, or nil if not found.
func (s *MemStore) GetIntent(id string) *domain.Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.intents[id]
	if !ok {
		return nil
	}
	return cloneIntent(i)
}

// ListIntents returns copies of all intents, optionally filtered by status
// and rail, ordered by CreatedAt ascending.
func (s *MemStore) ListIntents(status string, rail string) []*domain.Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.Intent, 0, len(s.intents))
	for _, i := range s.intents {
		if status != "" && string(i.Status) != status {
			continue
		}
		if rail != "" && string(i.Rail) != rail {
			continue
		}
		out = append(out, cloneIntent(i))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// UpdateIntent applies fn to the intent with id while holding the write lock,
// persisting the result. Returns the updated intent and the error from fn.
// If the intent is not found, fn is not called and nil + ErrNotFound is returned.
func (s *MemStore) UpdateIntent(id string, fn func(*domain.Intent) error) (*domain.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.intents[id]
	if !ok {
		return nil, ErrNotFound
	}
	if err := fn(i); err != nil {
		return nil, err
	}
	return cloneIntent(i), nil
}

// AddCapture records a capture linked to an intent.
func (s *MemStore) AddCapture(c domain.Capture) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captures[c.IntentID] = append(s.captures[c.IntentID], c)
}

// CapturesFor returns the captures recorded against an intent.
func (s *MemStore) CapturesFor(intentID string) []domain.Capture {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Capture, len(s.captures[intentID]))
	copy(out, s.captures[intentID])
	return out
}

// AddSettlement records a settlement linked to an intent.
func (s *MemStore) AddSettlement(set domain.Settlement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlements[set.IntentID] = append(s.settlements[set.IntentID], set)
}

// SettlementsFor returns the settlements recorded against an intent.
func (s *MemStore) SettlementsFor(intentID string) []domain.Settlement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Settlement, len(s.settlements[intentID]))
	copy(out, s.settlements[intentID])
	return out
}

// AddRefund records a refund linked to an intent.
func (s *MemStore) AddRefund(r domain.Refund) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refunds[r.IntentID] = append(s.refunds[r.IntentID], r)
}

// RefundsFor returns the refunds recorded against an intent.
func (s *MemStore) RefundsFor(intentID string) []domain.Refund {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Refund, len(s.refunds[intentID]))
	copy(out, s.refunds[intentID])
	return out
}

// AddChargeback records a chargeback linked to an intent.
func (s *MemStore) AddChargeback(c domain.Chargeback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chargebacks[c.IntentID] = append(s.chargebacks[c.IntentID], c)
}

// ChargebacksFor returns the chargebacks recorded against an intent.
func (s *MemStore) ChargebacksFor(intentID string) []domain.Chargeback {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Chargeback, len(s.chargebacks[intentID]))
	copy(out, s.chargebacks[intentID])
	return out
}

// UpdateChargeback applies fn to the chargeback with the given case ref.
func (s *MemStore) UpdateChargeback(caseRef string, fn func(*domain.Chargeback) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for intentID, list := range s.chargebacks {
		for idx := range list {
			if list[idx].CaseRef == caseRef {
				return fn(&s.chargebacks[intentID][idx])
			}
		}
	}
	return ErrNotFound
}

// RecordWebhook persists a webhook record keyed by (rail, external_event_id).
// It returns ErrDuplicateWebhook if a record with the same key already exists.
func (s *MemStore) RecordWebhook(w domain.Webhook) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := webhookKey(w.Rail, w.ExternalEventID)
	if _, ok := s.webhooks[key]; ok {
		return ErrDuplicateWebhook
	}
	s.webhooks[key] = w
	return nil
}

// MarkWebhookProcessed records that the webhook with (rail, external_event_id)
// has been processed.
func (s *MemStore) MarkWebhookProcessed(rail domain.Rail, externalEventID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := webhookKey(rail, externalEventID)
	if w, ok := s.webhooks[key]; ok {
		w.ProcessedAt = time.Now().UTC()
		s.webhooks[key] = w
	}
}

// WebhookExists reports whether a webhook with the given (rail, external_event_id)
// has already been recorded.
func (s *MemStore) WebhookExists(rail domain.Rail, externalEventID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.webhooks[webhookKey(rail, externalEventID)]
	return ok
}

// ErrNotFound is returned when an intent is not present in the store.
var ErrNotFound = errNotFound{}

// ErrDuplicateWebhook is returned when a webhook with the same (rail,
// external_event_id) is recorded twice.
var ErrDuplicateWebhook = errors.New("duplicate webhook")

type errNotFound struct{}

func (errNotFound) Error() string { return "intent not found" }

func webhookKey(rail domain.Rail, externalEventID string) string {
	return string(rail) + "\x00" + externalEventID
}

// cloneIntent returns a deep copy of i safe to return to callers.
func cloneIntent(i *domain.Intent) *domain.Intent {
	out := *i
	if i.History != nil {
		out.History = append([]domain.Event(nil), i.History...)
	}
	return &out
}
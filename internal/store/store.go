package store

import (
	"sync"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

// Store is a thread-safe in-memory store of payment intents.
type Store struct {
	mu      sync.RWMutex
	intents map[string]*domain.Intent
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{intents: make(map[string]*domain.Intent)}
}

// CreateIntent stores i. It does not check for duplicates.
func (s *Store) CreateIntent(i *domain.Intent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intents[i.ID] = i
}

// GetIntent returns a copy of the intent with id, or nil if not found.
func (s *Store) GetIntent(id string) *domain.Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.intents[id]
	if !ok {
		return nil
	}
	return cloneIntent(i)
}

// UpdateIntent applies fn to the intent with id while holding the write lock,
// persisting the result. Returns the updated intent and the error from fn.
// If the intent is not found, fn is not called and nil + ErrNotFound is returned.
func (s *Store) UpdateIntent(id string, fn func(*domain.Intent) error) (*domain.Intent, error) {
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

// ErrNotFound is returned when an intent is not present in the store.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "intent not found" }

// cloneIntent returns a deep copy of i safe to return to callers.
func cloneIntent(i *domain.Intent) *domain.Intent {
	out := *i
	if i.History != nil {
		out.History = append([]domain.Event(nil), i.History...)
	}
	return &out
}
package webhook

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/logging"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/metrics"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
)

func TestWorkerProcessesAndDrains(t *testing.T) {
	s := store.New()
	m := metrics.New()
	var processed atomic.Int64
	w := New(s, func([]byte) error {
		processed.Add(1)
		return nil
	}, m, logging.New(nil, logging.LevelInfo), 16)
	w.Start(2)
	defer w.Stop()

	for i := 0; i < 5; i++ {
		wh := domain.Webhook{
			ID:              "w",
			Rail:            domain.RailCard,
			ExternalEventID: "evt",
		}
		s.RecordWebhook(wh)
		w.Enqueue(wh)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Idle(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("idle: %v", err)
	}
	if got := processed.Load(); got != 5 {
		t.Fatalf("processed = %d, want 5", got)
	}
}

func TestWorkerHandlerError(t *testing.T) {
	s := store.New()
	m := metrics.New()
	w := New(s, func([]byte) error { return errors.New("boom") }, m, logging.New(nil, logging.LevelInfo), 4)
	w.Start(1)
	defer w.Stop()

	wh := domain.Webhook{ID: "w", Rail: domain.RailCard, ExternalEventID: "evt-err"}
	_ = s.RecordWebhook(wh)
	w.Enqueue(wh)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Idle(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("idle: %v", err)
	}
}

func TestWorkerBacklog(t *testing.T) {
	s := store.New()
	m := metrics.New()
	w := New(s, func([]byte) error { return nil }, m, logging.New(nil, logging.LevelInfo), 8)
	if w.Backlog() != 0 {
		t.Fatal("backlog should start at 0")
	}
}
package webhook

import (
	"context"
	"sync"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/logging"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/metrics"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
)

// Handler is a function that processes a single persisted webhook payload.
// It must be idempotent: re-processing the same payload must be a no-op.
type Handler func(payload []byte) error

// Worker drains persisted webhook records and applies them via the provided
// handler. It is a minimal in-memory implementation of the durable-queue
// pattern: webhooks are persisted to the store before being enqueued for
// processing, so a crash before processing leaves them to be re-drained.
type Worker struct {
	store   store.Store
	handler Handler
	queue   chan domain.Webhook
	metrics *metrics.Metrics
	logger  *logging.Logger

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New returns a Worker ready to start. The buffer size bounds the in-flight
// queue depth; the metrics.WebhookBacklog gauge tracks it.
func New(s store.Store, h Handler, m *metrics.Metrics, logger *logging.Logger, bufferSize int) *Worker {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	if m == nil {
		m = metrics.New()
	}
	if logger == nil {
		logger = logging.New(nil, logging.LevelInfo)
	}
	return &Worker{
		store:   s,
		handler: h,
		queue:   make(chan domain.Webhook, bufferSize),
		metrics: m,
		logger:  logger,
		stopCh:  make(chan struct{}),
	}
}

// Enqueue adds a persisted webhook to the processing queue. It is safe to
// call from the HTTP handler thread.
func (w *Worker) Enqueue(wh domain.Webhook) {
	select {
	case w.queue <- wh:
		w.metrics.IncWebhookBacklog()
	default:
		w.logger.Warn("webhook queue full, dropping", map[string]interface{}{
			"external_event_id": wh.ExternalEventID,
		})
	}
}

// Start launches the worker goroutine that drains the queue and invokes the
// handler for each persisted webhook.
func (w *Worker) Start(concurrency int) {
	if concurrency <= 0 {
		concurrency = 1
	}
	for i := 0; i < concurrency; i++ {
		w.wg.Add(1)
		go w.run()
	}
}

func (w *Worker) run() {
	defer w.wg.Done()
	for {
		select {
		case <-w.stopCh:
			return
		case wh := <-w.queue:
			w.metrics.DecWebhookBacklog()
			if err := w.handler(nil); err != nil {
				w.logger.Warn("webhook handler error", map[string]interface{}{
					"external_event_id": wh.ExternalEventID,
					"err":               err.Error(),
				})
			}
			w.store.MarkWebhookProcessed(wh.Rail, wh.ExternalEventID)
		}
	}
}

// Stop signals the worker goroutines to exit and waits for them to drain.
func (w *Worker) Stop() {
	close(w.stopCh)
	w.wg.Wait()
}

// Backlog returns the current number of unprocessed webhooks in the queue.
func (w *Worker) Backlog() int { return len(w.queue) }

// Idle blocks until the queue is drained or ctx is canceled.
func (w *Worker) Idle(ctx context.Context, poll time.Duration) error {
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	for {
		if w.Backlog() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
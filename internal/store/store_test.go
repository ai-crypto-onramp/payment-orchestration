package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

func TestCreateAndGet(t *testing.T) {
	s := New()
	i := &domain.Intent{ID: "i1", Status: domain.StatusIntent}
	s.CreateIntent(i)

	got := s.GetIntent("i1")
	if got == nil {
		t.Fatal("expected intent, got nil")
	}
	if got.ID != "i1" {
		t.Fatalf("id = %q, want i1", got.ID)
	}

	if s.GetIntent("missing") != nil {
		t.Fatal("expected nil for missing intent")
	}
}

func TestGetIntentReturnsCopy(t *testing.T) {
	s := New()
	s.CreateIntent(&domain.Intent{ID: "i1", Status: domain.StatusIntent, History: []domain.Event{{Type: domain.EventCreated}}})

	got := s.GetIntent("i1")
	got.Status = domain.StatusFailed
	got.History[0].Detail = "mutated"

	again := s.GetIntent("i1")
	if again.Status != domain.StatusIntent {
		t.Fatalf("stored status mutated to %q", again.Status)
	}
	if again.History[0].Detail != "" {
		t.Fatalf("stored history mutated: %q", again.History[0].Detail)
	}
}

func TestUpdateIntent(t *testing.T) {
	s := New()
	s.CreateIntent(&domain.Intent{ID: "i1", Status: domain.StatusIntent})

	got, err := s.UpdateIntent("i1", func(i *domain.Intent) error {
		return i.Transition(domain.StatusAuthorized, "auth")
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != domain.StatusAuthorized {
		t.Fatalf("status = %q, want authorized", got.Status)
	}
	if s.GetIntent("i1").Status != domain.StatusAuthorized {
		t.Fatal("stored intent not updated")
	}
}

func TestUpdateIntentNotFound(t *testing.T) {
	s := New()
	_, err := s.UpdateIntent("missing", func(i *domain.Intent) error { return nil })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateIntentPropagatesError(t *testing.T) {
	s := New()
	s.CreateIntent(&domain.Intent{ID: "i1", Status: domain.StatusSettled})
	custom := errors.New("boom")
	_, err := s.UpdateIntent("i1", func(i *domain.Intent) error { return custom })
	if !errors.Is(err, custom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if s.GetIntent("i1").Status != domain.StatusSettled {
		t.Fatal("intent should be unchanged on error")
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := New()
	s.CreateIntent(&domain.Intent{ID: "i1", Status: domain.StatusIntent})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = s.UpdateIntent("i1", func(i *domain.Intent) error {
				i.AppendEvent(domain.EventCreated, "")
				return nil
			})
			_ = s.GetIntent("i1")
		}(i)
	}
	wg.Wait()

	if got := len(s.GetIntent("i1").History); got != 20 {
		t.Fatalf("history len = %d, want 20", got)
	}
}

func TestAddAndListCaptures(t *testing.T) {
	s := New()
	s.AddCapture(domain.Capture{ID: "c1", IntentID: "i1", Amount: 100})
	s.AddCapture(domain.Capture{ID: "c2", IntentID: "i1", Amount: 200})
	got := s.CapturesFor("i1")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if len(s.CapturesFor("missing")) != 0 {
		t.Fatal("expected empty for missing intent")
	}
}

func TestAddAndListSettlements(t *testing.T) {
	s := New()
	s.AddSettlement(domain.Settlement{ID: "s1", IntentID: "i1", SettledAmount: 100})
	got := s.SettlementsFor("i1")
	if len(got) != 1 || got[0].SettledAmount != 100 {
		t.Fatalf("got %v", got)
	}
}

func TestAddAndListRefunds(t *testing.T) {
	s := New()
	s.AddRefund(domain.Refund{ID: "r1", IntentID: "i1", Amount: 50})
	got := s.RefundsFor("i1")
	if len(got) != 1 || got[0].Amount != 50 {
		t.Fatalf("got %v", got)
	}
}

func TestAddAndListChargebacks(t *testing.T) {
	s := New()
	s.AddChargeback(domain.Chargeback{ID: "cb1", IntentID: "i1", CaseRef: "case-1", Stage: domain.StageOpened})
	got := s.ChargebacksFor("i1")
	if len(got) != 1 || got[0].CaseRef != "case-1" {
		t.Fatalf("got %v", got)
	}
}

func TestUpdateChargeback(t *testing.T) {
	s := New()
	s.AddChargeback(domain.Chargeback{ID: "cb1", IntentID: "i1", CaseRef: "case-1", Stage: domain.StageOpened})
	if err := s.UpdateChargeback("case-1", func(c *domain.Chargeback) error {
		c.Stage = domain.StageReversal
		return nil
	}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	got := s.ChargebacksFor("i1")
	if got[0].Stage != domain.StageReversal {
		t.Fatalf("stage = %q", got[0].Stage)
	}
	if err := s.UpdateChargeback("missing", func(c *domain.Chargeback) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRecordWebhookDedup(t *testing.T) {
	s := New()
	w := domain.Webhook{ID: "w1", Rail: domain.RailCard, ExternalEventID: "evt-1"}
	if err := s.RecordWebhook(w); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := s.RecordWebhook(w); !errors.Is(err, ErrDuplicateWebhook) {
		t.Fatalf("err = %v, want ErrDuplicateWebhook", err)
	}
	if !s.WebhookExists(domain.RailCard, "evt-1") {
		t.Fatal("webhook should exist")
	}
	if s.WebhookExists(domain.RailCard, "evt-2") {
		t.Fatal("webhook should not exist")
	}
	s.MarkWebhookProcessed(domain.RailCard, "evt-1")
}
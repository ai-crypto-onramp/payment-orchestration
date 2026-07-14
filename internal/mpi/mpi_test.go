package mpi

import (
	"errors"
	"testing"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

func TestDummyChallenge(t *testing.T) {
	c := NewDummy()
	ch, err := c.Challenge(&domain.Intent{ID: "i1", Rail: domain.RailCard})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ch.ACSURL == "" || ch.Payload == "" {
		t.Fatal("expected non-empty challenge artifact")
	}

	if _, err := c.Challenge(&domain.Intent{ID: "i2", Rail: domain.RailACH}); err == nil {
		t.Fatal("expected error for non-card rail")
	}

	c.FailChallenge = true
	if _, err := c.Challenge(&domain.Intent{ID: "i3", Rail: domain.RailCard}); !errors.Is(err, ErrChallengeFailed) {
		t.Fatalf("err = %v, want ErrChallengeFailed", err)
	}
}

func TestDummyResume(t *testing.T) {
	c := NewDummy()
	i := &domain.Intent{ID: "i1", Rail: domain.RailCard}
	if err := c.Resume(i, "ok"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := c.Resume(i, ""); !errors.Is(err, ErrChallengeFailed) {
		t.Fatalf("err = %v, want ErrChallengeFailed", err)
	}
	if err := c.Resume(i, "fail"); !errors.Is(err, ErrChallengeFailed) {
		t.Fatalf("err = %v, want ErrChallengeFailed", err)
	}
	c.FailResume = true
	if err := c.Resume(i, "ok"); !errors.Is(err, ErrChallengeFailed) {
		t.Fatalf("err = %v, want ErrChallengeFailed", err)
	}
}
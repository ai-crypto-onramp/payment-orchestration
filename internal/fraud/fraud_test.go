package fraud

import (
	"context"
	"testing"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

func TestDummyScore(t *testing.T) {
	c := NewDummy()
	dec, err := c.Score(context.Background(), &domain.Intent{ID: "i1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !dec.Allowed {
		t.Fatal("expected allowed by default")
	}
	c.FailScore = true
	dec, err = c.Score(context.Background(), &domain.Intent{ID: "i1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if dec.Allowed {
		t.Fatal("expected blocked when FailScore")
	}
}
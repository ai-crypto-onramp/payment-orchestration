package rail

import (
	"errors"
	"testing"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

func TestDummyAuthorize(t *testing.T) {
	d := NewDummy()
	i := &domain.Intent{ID: "i1", Rail: domain.RailCard}
	if err := d.Authorize(i); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if i.ExternalID == "" {
		t.Fatal("ExternalID should be set")
	}

	d.FailAuthorize = true
	if err := d.Authorize(&domain.Intent{}); !errors.Is(err, ErrAuthorize) {
		t.Fatalf("err = %v, want ErrAuthorize", err)
	}
}

func TestDummyCapture(t *testing.T) {
	d := NewDummy()
	if err := d.Capture(&domain.Intent{}, 100); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	d.FailCapture = true
	if err := d.Capture(&domain.Intent{}, 100); !errors.Is(err, ErrCapture) {
		t.Fatalf("err = %v, want ErrCapture", err)
	}
}

func TestDummyRefund(t *testing.T) {
	d := NewDummy()
	if err := d.Refund(&domain.Intent{}, 100); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	d.FailRefund = true
	if err := d.Refund(&domain.Intent{}, 100); !errors.Is(err, ErrRefund) {
		t.Fatalf("err = %v, want ErrRefund", err)
	}
}

func TestDummySubmit(t *testing.T) {
	d := NewDummy()
	i := &domain.Intent{ID: "i1", Amount: 500}
	if err := d.Submit(i); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if i.CapturedAmount != 500 {
		t.Fatalf("captured = %d, want 500", i.CapturedAmount)
	}
	if i.ExternalID == "" {
		t.Fatal("ExternalID should be set")
	}
	d.FailAuthorize = true
	if err := d.Submit(&domain.Intent{ID: "i2"}); !errors.Is(err, ErrAuthorize) {
		t.Fatalf("err = %v, want ErrAuthorize", err)
	}
}

func TestDummyVoid(t *testing.T) {
	d := NewDummy()
	if err := d.Void(&domain.Intent{}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	d.FailVoid = true
	if err := d.Void(&domain.Intent{}); !errors.Is(err, ErrVoid) {
		t.Fatalf("err = %v, want ErrVoid", err)
	}
}

func TestDummyVerify3DS(t *testing.T) {
	d := NewDummy()
	card := &domain.Intent{Rail: domain.RailCard}
	if err := d.Verify3DS(card, "ok"); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if err := d.Verify3DS(&domain.Intent{Rail: domain.RailACH}, "ok"); !errors.Is(err, ErrUnsupported3DS) {
		t.Fatalf("err = %v, want ErrUnsupported3DS", err)
	}
	if err := d.Verify3DS(card, ""); !errors.Is(err, Err3DSVerify) {
		t.Fatalf("err = %v, want Err3DSVerify", err)
	}
	if err := d.Verify3DS(card, "fail"); !errors.Is(err, Err3DSVerify) {
		t.Fatalf("err = %v, want Err3DSVerify", err)
	}
	d.Fail3DS = true
	if err := d.Verify3DS(card, "ok"); !errors.Is(err, Err3DSVerify) {
		t.Fatalf("err = %v, want Err3DSVerify", err)
	}
}

func TestRegistry(t *testing.T) {
	d := NewDummy()
	r := NewRegistry(d)
	for _, rail := range []domain.Rail{domain.RailCard, domain.RailACH, domain.RailSEPA, domain.RailPIX, domain.RailUPI} {
		if r.For(rail) == nil {
			t.Fatalf("For(%q) returned nil", rail)
		}
	}
}
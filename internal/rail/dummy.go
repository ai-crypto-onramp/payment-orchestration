package rail

import (
	"errors"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

// Adapter is the interface every rail implements. All rails in this simplified
// service are backed by DummyAdapter; the interface exists to mirror the
// production shape and to keep tests decoupled.
type Adapter interface {
	Authorize(i *domain.Intent) error
	Capture(i *domain.Intent, amount int64) error
	Refund(i *domain.Intent, amount int64) error
	Submit(i *domain.Intent) error
	Void(i *domain.Intent) error
	Verify3DS(i *domain.Intent, challengeResult string) error
}

// Sentinel errors returned by adapters.
var (
	ErrAuthorize      = errors.New("rail authorize failed")
	ErrCapture        = errors.New("rail capture failed")
	ErrRefund         = errors.New("rail refund failed")
	ErrVoid           = errors.New("rail void failed")
	Err3DSVerify      = errors.New("rail 3ds verification failed")
	ErrUnsupported3DS = errors.New("rail does not support 3ds")
)

// DummyAdapter is the single dummy rail implementation. It succeeds at every
// operation unless FailAuthorize/FailCapture/FailRefund/Fail3DS/FailVoid is set
// (useful for testing failure paths). All rails map to the same instance.
type DummyAdapter struct {
	FailAuthorize bool
	FailCapture   bool
	FailRefund    bool
	FailVoid      bool
	Fail3DS       bool
}

// NewDummy returns a DummyAdapter that succeeds at everything.
func NewDummy() *DummyAdapter { return &DummyAdapter{} }

func (d *DummyAdapter) Authorize(i *domain.Intent) error {
	if d.FailAuthorize {
		return ErrAuthorize
	}
	i.ExternalID = "ext-" + i.ID
	return nil
}

func (d *DummyAdapter) Capture(i *domain.Intent, amount int64) error {
	if d.FailCapture {
		return ErrCapture
	}
	return nil
}

func (d *DummyAdapter) Refund(i *domain.Intent, amount int64) error {
	if d.FailRefund {
		return ErrRefund
	}
	return nil
}

// Submit collapses auth+capture into a single step for instant rails (PIX, UPI)
// and bank rails (ACH, SEPA). The intent lands directly in captured.
func (d *DummyAdapter) Submit(i *domain.Intent) error {
	if d.FailAuthorize {
		return ErrAuthorize
	}
	i.ExternalID = "ext-" + i.ID
	i.CapturedAmount = i.Amount
	return nil
}

// Void cancels a previously authorized but not-yet-captured intent.
func (d *DummyAdapter) Void(i *domain.Intent) error {
	if d.FailVoid {
		return ErrVoid
	}
	return nil
}

func (d *DummyAdapter) Verify3DS(i *domain.Intent, challengeResult string) error {
	if i.Rail != domain.RailCard {
		return ErrUnsupported3DS
	}
	if d.Fail3DS || challengeResult == "" || challengeResult == "fail" {
		return Err3DSVerify
	}
	return nil
}

// Registry selects an Adapter by rail name. In this simplified service every
// rail maps to the same DummyAdapter; the indirection is kept so a real
// implementation could swap per-rail connectors later.
type Registry struct {
	dummy *DummyAdapter
}

// NewRegistry builds a Registry backed by the given dummy adapter.
func NewRegistry(d *DummyAdapter) *Registry { return &Registry{dummy: d} }

// For returns the Adapter for the given rail. Unknown rails get the dummy
// adapter too; rail validation happens at the handler layer.
func (r *Registry) For(_ domain.Rail) Adapter { return r.dummy }
package mpi

import (
	"errors"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

// Challenge is the artifact returned to the client to perform a 3DS challenge.
type Challenge struct {
	ACSURL  string `json:"acs_url,omitempty"`
	Payload string `json:"payload,omitempty"`
}

// Client is the 3DS MPI interface used to initiate and resume card challenges.
type Client interface {
	Challenge(i *domain.Intent) (Challenge, error)
	Resume(i *domain.Intent, assertion string) error
}

// ErrChallengeFailed is returned when the 3DS challenge result is invalid.
var ErrChallengeFailed = errors.New("3ds challenge failed")

// ErrTimeout is returned when the 3DS challenge has expired.
var ErrTimeout = errors.New("3ds challenge timed out")

// DummyClient is a no-network 3DS MPI implementation. It succeeds for any
// non-empty assertion and fails for empty/"fail" assertions, mirroring the
// rail.DummyAdapter behavior so existing tests keep passing.
type DummyClient struct {
	FailChallenge bool
	FailResume    bool
}

// NewDummy returns a DummyClient that succeeds at everything.
func NewDummy() *DummyClient { return &DummyClient{} }

// Challenge returns a challenge artifact for the intent.
func (d *DummyClient) Challenge(i *domain.Intent) (Challenge, error) {
	if i.Rail != domain.RailCard {
		return Challenge{}, errors.New("3ds only supported for card rail")
	}
	if d.FailChallenge {
		return Challenge{}, ErrChallengeFailed
	}
	return Challenge{
		ACSURL:  "https://acs.example/3ds/" + i.ID,
		Payload: "payload-" + i.ID,
	}, nil
}

// Resume verifies the assertion returned by the client after the challenge.
func (d *DummyClient) Resume(i *domain.Intent, assertion string) error {
	if d.FailResume || assertion == "" || assertion == "fail" {
		return ErrChallengeFailed
	}
	return nil
}
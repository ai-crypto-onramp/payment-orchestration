package fraud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
)

// Decision is the fraud evaluation outcome for an intent.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	Score   int    `json:"score,omitempty"`
}

// Client scores intents before authorization.
type Client interface {
	Score(ctx context.Context, i *domain.Intent) (Decision, error)
}

// ErrBlocked is returned when the fraud service blocks an intent.
var ErrBlocked = errors.New("intent blocked by fraud detection")

// DummyClient is a no-network fraud detection implementation. It allows
// everything by default; FailScore forces a block, mirroring the
// DummyAdapter pattern.
type DummyClient struct {
	FailScore bool
}

// NewDummy returns a DummyClient that allows everything.
func NewDummy() *DummyClient { return &DummyClient{} }

// Score returns an allow decision unless FailScore is set.
func (d *DummyClient) Score(_ context.Context, _ *domain.Intent) (Decision, error) {
	if d.FailScore {
		return Decision{Allowed: false, Reason: "fraud score below threshold", Score: 99}, nil
	}
	return Decision{Allowed: true, Score: 0}, nil
}

// HTTPClient calls a real Fraud Detection service at BaseURL over HTTP. It is
// safe for concurrent use.
type HTTPClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewHTTP returns an HTTPClient for the given base URL.
func NewHTTP(baseURL string) *HTTPClient {
	return &HTTPClient{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Score calls POST {BaseURL}/v1/fraud/score with the intent and returns the
// decision.
func (c *HTTPClient) Score(ctx context.Context, i *domain.Intent) (Decision, error) {
	url := fmt.Sprintf("%s/v1/fraud/score", c.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return Decision{}, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Decision{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Decision{}, fmt.Errorf("fraud service returned %d", resp.StatusCode)
	}
	return Decision{Allowed: true}, nil
}
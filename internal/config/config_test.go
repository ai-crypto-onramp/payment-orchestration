package config

import (
	"os"
	"testing"
	"time"
)

func TestFromEnvDefaults(t *testing.T) {
	os.Unsetenv("PORT")
	os.Unsetenv("WEBHOOK_REPLAY_WINDOW")
	os.Unsetenv("IDEMPOTENCY_KEY_TTL")
	os.Unsetenv("LOG_LEVEL")
	c := FromEnv()
	if c.Port != "8080" {
		t.Fatalf("port = %q", c.Port)
	}
	if c.WebhookReplayWindow != 5*time.Minute {
		t.Fatalf("replay window = %v", c.WebhookReplayWindow)
	}
	if c.IdempotencyKeyTTL != 24*time.Hour {
		t.Fatalf("idem ttl = %v", c.IdempotencyKeyTTL)
	}
	if c.LogLevel != "info" {
		t.Fatalf("log level = %q", c.LogLevel)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("RAIL_CARD_URL", "http://card.example")
	t.Setenv("RAIL_PIX_URL", "http://pix.example")
	t.Setenv("WEBHOOK_SECRET_CARD", "card-secret")
	t.Setenv("WEBHOOK_REPLAY_WINDOW", "10m")
	t.Setenv("LOG_LEVEL", "DEBUG")
	c := FromEnv()
	if c.Port != "9090" {
		t.Fatalf("port = %q", c.Port)
	}
	if c.RailURLs["card"] != "http://card.example" {
		t.Fatalf("card url = %q", c.RailURLs["card"])
	}
	if c.WebhookSecrets["card"] != "card-secret" {
		t.Fatalf("card secret not set")
	}
	if c.WebhookReplayWindow != 10*time.Minute {
		t.Fatalf("replay window = %v", c.WebhookReplayWindow)
	}
	if c.LogLevel != "debug" {
		t.Fatalf("log level = %q", c.LogLevel)
	}
}

func TestEnabledRails(t *testing.T) {
	t.Setenv("RAIL_CARD_URL", "http://card.example")
	t.Setenv("RAIL_PIX_URL", "http://pix.example")
	c := FromEnv()
	enabled := c.EnabledRails()
	if len(enabled) != 2 {
		t.Fatalf("enabled rails = %d, want 2", len(enabled))
	}
}

func TestWebhookSecretFallback(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "generic-secret")
	t.Setenv("WEBHOOK_SECRET_CARD", "")
	c := FromEnv()
	if got := c.WebhookSecret("card"); got != "generic-secret" {
		t.Fatalf("fallback secret = %q", got)
	}
}
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/mtls"
)

// DevMode reports whether DEV_MODE=1 is set. When true, stub/stand-in clients
// may be used for local development. When false (production default), missing
// required env vars are fatal at startup.
func DevMode() bool { return os.Getenv("DEV_MODE") == "1" }

// MustEnv returns the value of name and logs.Fatalf when it is empty and
// DEV_MODE!=1. When DEV_MODE=1, an empty value returns "" and a warning is
// logged.
func MustEnv(name string) string {
	v := os.Getenv(name)
	if v != "" {
		return v
	}
	if DevMode() {
		log.Printf("DEV_MODE=1: env var %s unset — using stub (NOT FOR PRODUCTION)", name)
		return ""
	}
	log.Fatalf("required env var %s not set and DEV_MODE!=1; refusing to start in production mode", name)
	return ""
}

// MustEnvOrFatal is like MustEnv but always fatals when unset and !DEV_MODE,
// with a custom message indicating the real client is not yet implemented.
func MustEnvOrFatal(name, msg string) string {
	v := os.Getenv(name)
	if v != "" {
		return v
	}
	if DevMode() {
		log.Printf("DEV_MODE=1: env var %s unset — using stub (NOT FOR PRODUCTION)", name)
		return ""
	}
	if msg == "" {
		msg = fmt.Sprintf("required env var %s not set and DEV_MODE!=1; refusing to start in production mode", name)
	}
	log.Fatal(msg)
	return ""
}

// Config is the full environment-derived configuration for the service.
type Config struct {
	Port              string
	RailURLs          map[domain.Rail]string
	FraudURL          string
	ThreeDSMPIURL     string
	WebhookSecrets    map[domain.Rail]string
	WebhookReplayWindow time.Duration
	IdempotencyKeyTTL   time.Duration
	LogLevel          string
	OTLPEndpoint      string
	MTLS              mtls.Config
}

// FromEnv builds a Config from environment variables, applying documented
// defaults where the variable is unset.
func FromEnv() Config {
	c := Config{
		Port: getenv("PORT", "8080"),
		RailURLs: map[domain.Rail]string{
			domain.RailCard: os.Getenv("RAIL_CARD_URL"),
			domain.RailACH:  os.Getenv("RAIL_ACH_URL"),
			domain.RailSEPA: os.Getenv("RAIL_SEPA_URL"),
			domain.RailPIX:  os.Getenv("RAIL_PIX_URL"),
			domain.RailUPI:  os.Getenv("RAIL_UPI_URL"),
		},
		FraudURL:         os.Getenv("FRAUD_URL"),
		ThreeDSMPIURL:    os.Getenv("THREE_DS_MPI_URL"),
		WebhookSecrets: map[domain.Rail]string{
			domain.RailCard: os.Getenv("WEBHOOK_SECRET_CARD"),
			domain.RailACH:  os.Getenv("WEBHOOK_SECRET_ACH"),
			domain.RailSEPA: os.Getenv("WEBHOOK_SECRET_SEPA"),
			domain.RailPIX:  os.Getenv("WEBHOOK_SECRET_PIX"),
			domain.RailUPI:  os.Getenv("WEBHOOK_SECRET_UPI"),
		},
		WebhookReplayWindow: getduration("WEBHOOK_REPLAY_WINDOW", 5*time.Minute),
		IdempotencyKeyTTL:   getduration("IDEMPOTENCY_KEY_TTL", 24*time.Hour),
		LogLevel:           strings.ToLower(getenv("LOG_LEVEL", "info")),
		OTLPEndpoint:       os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		MTLS: mtls.Config{
			CertFile: os.Getenv("MTLS_CERT_FILE"),
			KeyFile:  os.Getenv("MTLS_KEY_FILE"),
			CAFile:   os.Getenv("MTLS_CA_FILE"),
		},
	}
	return c
}

// EnabledRails returns the rails that have a configured rail URL.
func (c Config) EnabledRails() []domain.Rail {
	var out []domain.Rail
	for _, r := range []domain.Rail{domain.RailCard, domain.RailACH, domain.RailSEPA, domain.RailPIX, domain.RailUPI} {
		if c.RailURLs[r] != "" {
			out = append(out, r)
		}
	}
	return out
}

// WebhookSecret returns the secret configured for the given rail, falling
// back to the generic WEBHOOK_SECRET env var when the per-rail secret is
// unset.
func (c Config) WebhookSecret(r domain.Rail) string {
	if s := c.WebhookSecrets[r]; s != "" {
		return s
	}
	return os.Getenv("WEBHOOK_SECRET")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getduration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return time.Duration(n) * time.Second
	}
	return d
}
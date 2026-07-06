package store

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the store configuration read from the environment.
type Config struct {
	DBURL            string
	MaxConns         int
	MinConns         int
	MaxConnLifetime  time.Duration
	MaxConnIdleTime  time.Duration
}

// LoadConfig reads store configuration from environment variables using the
// defaults documented in README.md.
func LoadConfig() Config {
	cfg := Config{
		DBURL:           os.Getenv("DB_URL"),
		MaxConns:        envInt("DB_MAX_CONNS", 25),
		MinConns:        envInt("DB_MIN_CONNS", 2),
		MaxConnLifetime: envDuration("DB_CONN_MAX_LIFETIME_SECONDS", 300*time.Second),
		MaxConnIdleTime: envDuration("DB_CONN_MAX_IDLE_SECONDS", 60*time.Second),
	}
	return cfg
}

// Open opens a pooled Postgres connection, pings it, and applies all migrations.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	if cfg.DBURL == "" {
		return nil, fmt.Errorf("DB_URL is required")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DBURL)
	if err != nil {
		return nil, fmt.Errorf("parse DB_URL: %w", err)
	}
	poolCfg.MaxConns = int32(cfg.MaxConns)
	poolCfg.MinConns = int32(cfg.MinConns)
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return pool, nil
}

// HealthChecker runs a liveness probe against the pool for the /healthz handler.
type HealthChecker struct {
	pool *pgxpool.Pool
}

// NewHealthChecker returns a HealthChecker for the given pool.
func NewHealthChecker(pool *pgxpool.Pool) *HealthChecker {
	return &HealthChecker{pool: pool}
}

// Check returns nil if the pool is reachable, otherwise an error describing the
// failure.
func (h *HealthChecker) Check(ctx context.Context) error {
	if h.pool != nil {
		if err := h.pool.Ping(ctx); err != nil {
			return fmt.Errorf("db: %w", err)
		}
	}
	return nil
}

// Repository bundles per-table repository structs sharing a single pool.
type Repository struct {
	Pool       *pgxpool.Pool
	Intents    *IntentRepository
	Captures   *CaptureRepository
	Settlements *SettlementRepository
	Refunds    *RefundRepository
	Chargebacks *ChargebackRepository
	Webhooks   *WebhookRepository
	Transitions *TransitionRepository
}

// NewRepository builds a Repository over pool with all per-table structs wired.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		Pool:        pool,
		Intents:     &IntentRepository{pool: pool},
		Captures:    &CaptureRepository{pool: pool},
		Settlements: &SettlementRepository{pool: pool},
		Refunds:     &RefundRepository{pool: pool},
		Chargebacks: &ChargebackRepository{pool: pool},
		Webhooks:    &WebhookRepository{pool: pool},
		Transitions: &TransitionRepository{pool: pool},
	}
}

// IntentRepository reads and writes payment_intents rows.
type IntentRepository struct{ pool *pgxpool.Pool }

// CaptureRepository reads and writes captures rows.
type CaptureRepository struct{ pool *pgxpool.Pool }

// SettlementRepository reads and writes settlements rows.
type SettlementRepository struct{ pool *pgxpool.Pool }

// RefundRepository reads and writes refunds rows.
type RefundRepository struct{ pool *pgxpool.Pool }

// ChargebackRepository reads and writes chargebacks rows.
type ChargebackRepository struct{ pool *pgxpool.Pool }

// WebhookRepository reads and writes webhooks rows.
type WebhookRepository struct{ pool *pgxpool.Pool }

// TransitionRepository appends rows to payment_transitions. Updates and deletes
// are rejected by a database trigger (see migration 0007) and should never be
// issued from this repository.
type TransitionRepository struct{ pool *pgxpool.Pool }

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if secs, err := parseSeconds(v); err == nil {
			return secs
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func parseSeconds(s string) (time.Duration, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return time.Duration(n) * time.Second, nil
}
package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func skipIfNoDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DB_URL")
	if url == "" {
		t.Skip("DB_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := LoadConfig()
	pool, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMigrateIntegrationTablesExist(t *testing.T) {
	pool := skipIfNoDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	want := []string{
		"payment_intents",
		"captures",
		"settlements",
		"refunds",
		"chargebacks",
		"webhooks",
		"payment_transitions",
	}
	for _, table := range want {
		var exists bool
		err := pool.QueryRow(ctx, `
SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s does not exist after migrations", table)
		}
	}
}

func TestMigrateIntegrationWebhookUniqueConstraint(t *testing.T) {
	pool := skipIfNoDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `DELETE FROM webhooks WHERE rail = 'card' AND external_event_id = 'evt_uniq_test'`)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO webhooks (rail, external_event_id, raw_payload)
VALUES ('card', 'evt_uniq_test', '\x7b7d')`)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO webhooks (rail, external_event_id, raw_payload)
VALUES ('card', 'evt_uniq_test', '\x7b7d')`)
	if err == nil {
		t.Fatal("expected duplicate webhook insert to fail, but it succeeded")
	}

	_, _ = pool.Exec(ctx, `DELETE FROM webhooks WHERE rail = 'card' AND external_event_id = 'evt_uniq_test'`)
}

func TestMigrateIntegrationTransitionRejectsUpdateAndDelete(t *testing.T) {
	pool := skipIfNoDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `
INSERT INTO payment_intents (id, rail, amount, currency, payer_ref, state, idempotency_key)
VALUES ('11111111-1111-1111-1111-111111111111', 'card', 100, 'USD', 'payer_t1', 'intent', 'idem_t1')
ON CONFLICT (idempotency_key) DO NOTHING`)
	if err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM payment_intents WHERE idempotency_key = 'idem_t1'`)
	})

	_, err = pool.Exec(ctx, `
INSERT INTO payment_transitions (intent_id, from_state, to_state, reason, actor)
VALUES ('11111111-1111-1111-1111-111111111111', 'intent', 'authorized', 'auth_ok', 'test')`)
	if err != nil {
		t.Fatalf("insert transition: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE payment_transitions SET to_state = 'captured' WHERE intent_id = '11111111-1111-1111-1111-111111111111'`); err == nil {
		t.Fatal("expected UPDATE on payment_transitions to be rejected, but it succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM payment_transitions WHERE intent_id = '11111111-1111-1111-1111-111111111111'`); err == nil {
		t.Fatal("expected DELETE on payment_transitions to be rejected, but it succeeded")
	}
}
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ai-crypto-onramp/payment-orchestration/internal/domain"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store"
	"github.com/ai-crypto-onramp/payment-orchestration/internal/store/migrations"
)

type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	runner := migrations.NewRunner(
		func(c context.Context, q string, args ...any) error {
			_, err := pool.Exec(c, q, args...)
			return err
		},
		func(c context.Context, version string) (bool, error) {
			var exists bool
			err := pool.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists)
			return exists, err
		},
	)
	if err := runner.Up(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() error {
	d.pool.Close()
	return nil
}

func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

func (d *DB) CreateIntent(i *domain.Intent) {
	ctx := context.Background()
	if i.CreatedAt.IsZero() {
		i.CreatedAt = time.Now().UTC()
	}
	if i.UpdatedAt.IsZero() {
		i.UpdatedAt = i.CreatedAt
	}
	hist, _ := json.Marshal(i.History)
	_, _ = d.pool.Exec(ctx, `INSERT INTO intents
	(id, rail, amount, currency, payer_ref, status, captured_amount, refunded_amount, settled_amount,
	 external_id, idempotency_key, three_ds_required, created_at, updated_at, history)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	ON CONFLICT (id) DO NOTHING`,
		i.ID, string(i.Rail), i.Amount, i.Currency, i.PayerRef, string(i.Status),
		i.CapturedAmount, i.RefundedAmount, i.SettledAmount, i.ExternalID, i.IdempotencyKey,
		i.ThreeDSRequired, i.CreatedAt, i.UpdatedAt, hist)
}

func (d *DB) GetIntent(id string) *domain.Intent {
	ctx := context.Background()
	i, err := d.scanIntent(ctx, d.pool.QueryRow(ctx, intentSelectSQL()+` WHERE id=$1`, id))
	if err != nil {
		return nil
	}
	return i
}

func (d *DB) ListIntents(status, rail string) []*domain.Intent {
	ctx := context.Background()
	q := intentSelectSQL()
	args := []any{}
	conds := []string{}
	if status != "" {
		args = append(args, status)
		conds = append(conds, fmt.Sprintf("status=$%d", len(args)))
	}
	if rail != "" {
		args = append(args, rail)
		conds = append(conds, fmt.Sprintf("rail=$%d", len(args)))
	}
	if len(conds) > 0 {
		q += " WHERE " + joinAnd(conds)
	}
	q += " ORDER BY created_at ASC"
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []*domain.Intent{}
	for rows.Next() {
		i, err := scanIntentRow(rows)
		if err != nil {
			return nil
		}
		out = append(out, i)
	}
	return out
}

func (d *DB) UpdateIntent(id string, fn func(*domain.Intent) error) (*domain.Intent, error) {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	i, err := d.scanIntent(ctx, tx.QueryRow(ctx, intentSelectSQL()+` WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if err := fn(i); err != nil {
		return nil, err
	}
	i.UpdatedAt = time.Now().UTC()
	hist, _ := json.Marshal(i.History)
	if _, err := tx.Exec(ctx, `UPDATE intents SET rail=$2, amount=$3, currency=$4, payer_ref=$5, status=$6,
		captured_amount=$7, refunded_amount=$8, settled_amount=$9, external_id=$10, idempotency_key=$11,
		three_ds_required=$12, updated_at=$13, history=$14 WHERE id=$1`,
		id, string(i.Rail), i.Amount, i.Currency, i.PayerRef, string(i.Status),
		i.CapturedAmount, i.RefundedAmount, i.SettledAmount, i.ExternalID, i.IdempotencyKey,
		i.ThreeDSRequired, i.UpdatedAt, hist); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return i, nil
}

func (d *DB) AddCapture(c domain.Capture) {
	ctx := context.Background()
	if c.CapturedAt.IsZero() {
		c.CapturedAt = time.Now().UTC()
	}
	_, _ = d.pool.Exec(ctx, `INSERT INTO captures (id, intent_id, amount, external_ref, captured_at)
	VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		c.ID, c.IntentID, c.Amount, c.ExternalRef, c.CapturedAt)
}

func (d *DB) CapturesFor(intentID string) []domain.Capture {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT id, intent_id, amount, external_ref, captured_at FROM captures WHERE intent_id=$1 ORDER BY captured_at ASC`, intentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []domain.Capture{}
	for rows.Next() {
		var c domain.Capture
		if err := rows.Scan(&c.ID, &c.IntentID, &c.Amount, &c.ExternalRef, &c.CapturedAt); err != nil {
			return nil
		}
		out = append(out, c)
	}
	return out
}

func (d *DB) AddSettlement(s domain.Settlement) {
	ctx := context.Background()
	if s.SettledAt.IsZero() {
		s.SettledAt = time.Now().UTC()
	}
	_, _ = d.pool.Exec(ctx, `INSERT INTO settlements (id, intent_id, settled_amount, settled_at, rail_ref)
	VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		s.ID, s.IntentID, s.SettledAmount, s.SettledAt, s.RailRef)
}

func (d *DB) SettlementsFor(intentID string) []domain.Settlement {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT id, intent_id, settled_amount, settled_at, rail_ref FROM settlements WHERE intent_id=$1 ORDER BY settled_at ASC`, intentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []domain.Settlement{}
	for rows.Next() {
		var s domain.Settlement
		if err := rows.Scan(&s.ID, &s.IntentID, &s.SettledAmount, &s.SettledAt, &s.RailRef); err != nil {
			return nil
		}
		out = append(out, s)
	}
	return out
}

func (d *DB) AddRefund(r domain.Refund) {
	ctx := context.Background()
	if r.RefundedAt.IsZero() {
		r.RefundedAt = time.Now().UTC()
	}
	_, _ = d.pool.Exec(ctx, `INSERT INTO refunds (id, intent_id, amount, external_ref, refunded_at, state)
	VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`,
		r.ID, r.IntentID, r.Amount, r.ExternalRef, r.RefundedAt, r.State)
}

func (d *DB) RefundsFor(intentID string) []domain.Refund {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT id, intent_id, amount, external_ref, refunded_at, state FROM refunds WHERE intent_id=$1 ORDER BY refunded_at ASC`, intentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []domain.Refund{}
	for rows.Next() {
		var r domain.Refund
		if err := rows.Scan(&r.ID, &r.IntentID, &r.Amount, &r.ExternalRef, &r.RefundedAt, &r.State); err != nil {
			return nil
		}
		out = append(out, r)
	}
	return out
}

func (d *DB) AddChargeback(c domain.Chargeback) {
	ctx := context.Background()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.UpdatedAt = time.Now().UTC()
	_, _ = d.pool.Exec(ctx, `INSERT INTO chargebacks (id, intent_id, amount, reason, stage, case_ref, created_at, updated_at)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (case_ref) DO NOTHING`,
		c.ID, c.IntentID, c.Amount, c.Reason, string(c.Stage), c.CaseRef, c.CreatedAt, c.UpdatedAt)
}

func (d *DB) ChargebacksFor(intentID string) []domain.Chargeback {
	ctx := context.Background()
	rows, err := d.pool.Query(ctx, `SELECT id, intent_id, amount, reason, stage, case_ref, created_at, updated_at FROM chargebacks WHERE intent_id=$1 ORDER BY created_at ASC`, intentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []domain.Chargeback{}
	for rows.Next() {
		var c domain.Chargeback
		var stage string
		if err := rows.Scan(&c.ID, &c.IntentID, &c.Amount, &c.Reason, &stage, &c.CaseRef, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil
		}
		c.Stage = domain.ChargebackStage(stage)
		out = append(out, c)
	}
	return out
}

func (d *DB) UpdateChargeback(caseRef string, fn func(*domain.Chargeback) error) error {
	ctx := context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var c domain.Chargeback
	var stage string
	err = tx.QueryRow(ctx, `SELECT id, intent_id, amount, reason, stage, case_ref, created_at, updated_at FROM chargebacks WHERE case_ref=$1 FOR UPDATE`, caseRef).
		Scan(&c.ID, &c.IntentID, &c.Amount, &c.Reason, &stage, &c.CaseRef, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	c.Stage = domain.ChargebackStage(stage)
	if err := fn(&c); err != nil {
		return err
	}
	c.UpdatedAt = time.Now().UTC()
	if _, err := tx.Exec(ctx, `UPDATE chargebacks SET amount=$2, reason=$3, stage=$4, updated_at=$5 WHERE case_ref=$1`,
		caseRef, c.Amount, c.Reason, string(c.Stage), c.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d *DB) RecordWebhook(w domain.Webhook) error {
	ctx := context.Background()
	if w.ReceivedAt.IsZero() {
		w.ReceivedAt = time.Now().UTC()
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO webhooks (id, rail, external_event_id, signature, received_at)
	VALUES ($1,$2,$3,$4,$5) ON CONFLICT (rail, external_event_id) DO NOTHING`,
		w.ID, string(w.Rail), w.ExternalEventID, w.Signature, w.ReceivedAt)
	if err != nil {
		return err
	}
	return nil
}

func (d *DB) MarkWebhookProcessed(rail domain.Rail, externalEventID string) {
	ctx := context.Background()
	_, _ = d.pool.Exec(ctx, `UPDATE webhooks SET processed_at=now(), updated_at=now() WHERE rail=$1 AND external_event_id=$2`,
		string(rail), externalEventID)
}

func (d *DB) WebhookExists(rail domain.Rail, externalEventID string) bool {
	ctx := context.Background()
	var exists bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM webhooks WHERE rail=$1 AND external_event_id=$2)`,
		string(rail), externalEventID).Scan(&exists)
	if err != nil {
		return false
	}
	return exists
}

func intentSelectSQL() string {
	return `SELECT id, rail, amount, currency, payer_ref, status, captured_amount, refunded_amount, settled_amount,
	external_id, idempotency_key, three_ds_required, created_at, updated_at, history FROM intents`
}

func (d *DB) scanIntent(ctx context.Context, row pgx.Row) (*domain.Intent, error) {
	var i domain.Intent
	var rail, status string
	var hist []byte
	if err := row.Scan(&i.ID, &rail, &i.Amount, &i.Currency, &i.PayerRef, &status,
		&i.CapturedAmount, &i.RefundedAmount, &i.SettledAmount, &i.ExternalID, &i.IdempotencyKey,
		&i.ThreeDSRequired, &i.CreatedAt, &i.UpdatedAt, &hist); err != nil {
		return nil, err
	}
	i.Rail = domain.Rail(rail)
	i.Status = domain.Status(status)
	if len(hist) > 0 {
		_ = json.Unmarshal(hist, &i.History)
	}
	if i.History == nil {
		i.History = []domain.Event{}
	}
	return &i, nil
}

func scanIntentRow(rows pgx.Rows) (*domain.Intent, error) {
	var i domain.Intent
	var rail, status string
	var hist []byte
	if err := rows.Scan(&i.ID, &rail, &i.Amount, &i.Currency, &i.PayerRef, &status,
		&i.CapturedAmount, &i.RefundedAmount, &i.SettledAmount, &i.ExternalID, &i.IdempotencyKey,
		&i.ThreeDSRequired, &i.CreatedAt, &i.UpdatedAt, &hist); err != nil {
		return nil, err
	}
	i.Rail = domain.Rail(rail)
	i.Status = domain.Status(status)
	if len(hist) > 0 {
		_ = json.Unmarshal(hist, &i.History)
	}
	if i.History == nil {
		i.History = []domain.Event{}
	}
	return &i, nil
}

func joinAnd(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " AND "
		}
		out += s
	}
	return out
}

var _ store.Store = (*DB)(nil)
var _ = sort.Slice
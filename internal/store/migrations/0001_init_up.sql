CREATE TABLE IF NOT EXISTS intents (
    id               TEXT        PRIMARY KEY,
    rail             TEXT        NOT NULL,
    amount           BIGINT      NOT NULL,
    currency         TEXT        NOT NULL,
    payer_ref        TEXT        NOT NULL DEFAULT '',
    status           TEXT        NOT NULL,
    captured_amount  BIGINT      NOT NULL DEFAULT 0,
    refunded_amount  BIGINT      NOT NULL DEFAULT 0,
    settled_amount   BIGINT      NOT NULL DEFAULT 0,
    external_id      TEXT        NOT NULL DEFAULT '',
    idempotency_key  TEXT        NOT NULL DEFAULT '',
    three_ds_required BOOLEAN     NOT NULL DEFAULT false,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    history          JSONB       NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS intents_status_idx ON intents (status);
CREATE INDEX IF NOT EXISTS intents_rail_idx   ON intents (rail);
CREATE INDEX IF NOT EXISTS intents_created_at_idx ON intents (created_at);

CREATE TABLE IF NOT EXISTS captures (
    id           TEXT        PRIMARY KEY,
    intent_id    TEXT        NOT NULL,
    amount       BIGINT      NOT NULL,
    external_ref TEXT        NOT NULL DEFAULT '',
    captured_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS captures_intent_id_idx ON captures (intent_id);

CREATE TABLE IF NOT EXISTS settlements (
    id             TEXT        PRIMARY KEY,
    intent_id      TEXT        NOT NULL,
    settled_amount BIGINT      NOT NULL,
    settled_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    rail_ref       TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS settlements_intent_id_idx ON settlements (intent_id);

CREATE TABLE IF NOT EXISTS refunds (
    id           TEXT        PRIMARY KEY,
    intent_id    TEXT        NOT NULL,
    amount       BIGINT      NOT NULL,
    external_ref TEXT        NOT NULL DEFAULT '',
    refunded_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    state        TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS refunds_intent_id_idx ON refunds (intent_id);

CREATE TABLE IF NOT EXISTS chargebacks (
    id         TEXT        PRIMARY KEY,
    intent_id  TEXT        NOT NULL,
    amount     BIGINT      NOT NULL,
    reason     TEXT        NOT NULL DEFAULT '',
    stage      TEXT        NOT NULL,
    case_ref   TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (case_ref)
);
CREATE INDEX IF NOT EXISTS chargebacks_intent_id_idx ON chargebacks (intent_id);

CREATE TABLE IF NOT EXISTS webhooks (
    id                TEXT        PRIMARY KEY,
    rail              TEXT        NOT NULL,
    external_event_id TEXT        NOT NULL,
    signature         TEXT        NOT NULL DEFAULT '',
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (rail, external_event_id)
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key        TEXT        PRIMARY KEY,
    response   JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idempotency_keys_expires_at_idx ON idempotency_keys (expires_at);
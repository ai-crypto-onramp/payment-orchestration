CREATE TABLE IF NOT EXISTS payment_intents (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    rail            TEXT         NOT NULL,
    amount          NUMERIC(20, 8) NOT NULL,
    currency        TEXT         NOT NULL,
    payer_ref       TEXT         NOT NULL,
    state           TEXT         NOT NULL,
    idempotency_key TEXT         NOT NULL,
    external_id     TEXT,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT payment_intents_rail_check
        CHECK (rail IN ('card', 'ach', 'sepa', 'pix', 'upi'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_intents_idempotency_key
    ON payment_intents (idempotency_key);
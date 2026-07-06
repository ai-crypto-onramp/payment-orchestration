CREATE TABLE IF NOT EXISTS settlements (
    id             UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id      UUID         NOT NULL,
    capture_id     UUID,
    settled_amount NUMERIC(20, 8) NOT NULL,
    settled_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    rail_ref       TEXT,
    CONSTRAINT settlements_intent_fk
        FOREIGN KEY (intent_id) REFERENCES payment_intents (id) ON DELETE CASCADE,
    CONSTRAINT settlements_capture_fk
        FOREIGN KEY (capture_id) REFERENCES captures (id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_settlements_intent_id
    ON settlements (intent_id);

CREATE INDEX IF NOT EXISTS idx_settlements_capture_id
    ON settlements (capture_id);
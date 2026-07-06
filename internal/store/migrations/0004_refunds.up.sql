CREATE TABLE IF NOT EXISTS refunds (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id    UUID         NOT NULL,
    capture_id   UUID,
    amount       NUMERIC(20, 8) NOT NULL,
    external_ref TEXT,
    refunded_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    state        TEXT         NOT NULL DEFAULT 'pending',
    CONSTRAINT refunds_intent_fk
        FOREIGN KEY (intent_id) REFERENCES payment_intents (id) ON DELETE CASCADE,
    CONSTRAINT refunds_capture_fk
        FOREIGN KEY (capture_id) REFERENCES captures (id) ON DELETE SET NULL,
    CONSTRAINT refunds_state_check
        CHECK (state IN ('pending', 'succeeded', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_refunds_intent_id
    ON refunds (intent_id);

CREATE INDEX IF NOT EXISTS idx_refunds_capture_id
    ON refunds (capture_id);
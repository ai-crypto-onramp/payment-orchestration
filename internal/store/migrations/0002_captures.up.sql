CREATE TABLE IF NOT EXISTS captures (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id    UUID         NOT NULL,
    amount       NUMERIC(20, 8) NOT NULL,
    external_ref TEXT,
    captured_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT captures_intent_fk
        FOREIGN KEY (intent_id) REFERENCES payment_intents (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_captures_intent_id
    ON captures (intent_id);
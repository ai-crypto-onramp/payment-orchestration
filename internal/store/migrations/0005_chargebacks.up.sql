CREATE TABLE IF NOT EXISTS chargebacks (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id  UUID         NOT NULL,
    amount     NUMERIC(20, 8) NOT NULL,
    reason     TEXT,
    stage      TEXT         NOT NULL DEFAULT 'opened',
    case_ref   TEXT,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT chargebacks_intent_fk
        FOREIGN KEY (intent_id) REFERENCES payment_intents (id) ON DELETE CASCADE,
    CONSTRAINT chargebacks_stage_check
        CHECK (stage IN ('opened', 'evidence', 'arbitration', 'reversal'))
);

CREATE INDEX IF NOT EXISTS idx_chargebacks_intent_id
    ON chargebacks (intent_id);
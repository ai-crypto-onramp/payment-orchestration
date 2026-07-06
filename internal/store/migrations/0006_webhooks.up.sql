CREATE TABLE IF NOT EXISTS webhooks (
    id                 UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    rail               TEXT         NOT NULL,
    external_event_id  TEXT         NOT NULL,
    raw_payload        BYTEA        NOT NULL,
    signature          TEXT,
    processed_at       TIMESTAMPTZ,
    idempotency_token  TEXT,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT webhooks_rail_check
        CHECK (rail IN ('card', 'ach', 'sepa', 'pix', 'upi')),
    CONSTRAINT webhooks_rail_external_event_uniq
        UNIQUE (rail, external_event_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_webhooks_rail_external_event_id
    ON webhooks (rail, external_event_id);
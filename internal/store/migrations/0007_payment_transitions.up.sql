CREATE TABLE IF NOT EXISTS payment_transitions (
    id         BIGSERIAL    PRIMARY KEY,
    intent_id  UUID         NOT NULL,
    from_state TEXT         NOT NULL,
    to_state   TEXT         NOT NULL,
    reason     TEXT,
    actor      TEXT         NOT NULL,
    at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT payment_transitions_intent_fk
        FOREIGN KEY (intent_id) REFERENCES payment_intents (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_payment_transitions_intent_id
    ON payment_transitions (intent_id);

-- Append-only guard: payment_transitions is an audit trail. Rows must never be
-- UPDATEd or DELETEd. We enforce this at the database layer with two measures:
--   1. REVOKE UPDATE/DELETE privileges on the table from PUBLIC.
--   2. A trigger that raises an exception on any UPDATE or DELETE attempt,
--      providing a second line of defense for any role that retains the rights
--      (e.g. the table owner, migrations role, or superusers).
REVOKE UPDATE, DELETE ON payment_transitions FROM PUBLIC;

CREATE OR REPLACE FUNCTION guard_payment_transitions_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'payment_transitions is append-only: UPDATE and DELETE are not allowed';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_payment_transitions_no_update ON payment_transitions;
CREATE TRIGGER trg_payment_transitions_no_update
    BEFORE UPDATE ON payment_transitions
    FOR EACH ROW
    EXECUTE FUNCTION guard_payment_transitions_append_only();

DROP TRIGGER IF EXISTS trg_payment_transitions_no_delete ON payment_transitions;
CREATE TRIGGER trg_payment_transitions_no_delete
    BEFORE DELETE ON payment_transitions
    FOR EACH ROW
    EXECUTE FUNCTION guard_payment_transitions_append_only();
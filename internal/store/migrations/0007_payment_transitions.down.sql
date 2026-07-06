DROP TRIGGER IF EXISTS trg_payment_transitions_no_update ON payment_transitions;
DROP TRIGGER IF EXISTS trg_payment_transitions_no_delete ON payment_transitions;
DROP FUNCTION IF EXISTS guard_payment_transitions_append_only();
DROP TABLE IF EXISTS payment_transitions;
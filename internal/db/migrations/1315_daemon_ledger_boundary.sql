-- SPDX-License-Identifier: AGPL-3.0-only
-- The tenant requirement is independent of each computer's enrolment. Old
-- writers omit these nullable columns and retain their pre-activation behaviour.
ALTER TABLE account_use_rules ADD COLUMN ledger_mode_at timestamptz;
ALTER TABLE agent_pairing_computers ADD COLUMN ledger_generation text;
ALTER TABLE agent_pairing_computers ADD COLUMN ledger_enrolled_at timestamptz;

CREATE FUNCTION aeon_ledger_mode_monotonic() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.ledger_mode_at IS NOT NULL THEN
    NEW.ledger_mode_at := OLD.ledger_mode_at;
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER account_use_ledger_mode_monotonic
BEFORE UPDATE OF ledger_mode_at ON account_use_rules
FOR EACH ROW EXECUTE FUNCTION aeon_ledger_mode_monotonic();

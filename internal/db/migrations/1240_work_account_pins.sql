-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-648: retain and edit account/group pins after the one-work-kind upgrade.
-- Historical migrations and wire names remain unchanged.
SET LOCAL lock_timeout = '5s';

CREATE OR REPLACE FUNCTION aeon_account_ticket_pin() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM nodes n
        JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
        WHERE n.tenant_id = NEW.tenant_id AND n.id = NEW.ticket_id
          AND k.slug IN ('work', 'ticket') AND n.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'pin target must be a ticket' USING ERRCODE = '23514';
    END IF;
    IF NEW.account_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM agent_accounts a
        WHERE a.tenant_id = NEW.tenant_id AND a.id = NEW.account_id AND a.harness = NEW.harness
    ) THEN
        RAISE EXCEPTION 'pin account harness mismatch' USING ERRCODE = '23514';
    END IF;
    IF NEW.group_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM account_groups g
        WHERE g.tenant_id = NEW.tenant_id AND g.id = NEW.group_id AND g.harness = NEW.harness
    ) THEN
        RAISE EXCEPTION 'pin group harness mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

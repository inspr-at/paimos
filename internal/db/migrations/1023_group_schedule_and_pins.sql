-- SPDX-License-Identifier: AGPL-3.0-only
-- Group schedule scope, ticket pins, and queued-run group targets (AEON-389).
-- A run's account pin stays on agent_runs.requested_account_id. Group targets
-- live here so the agent_runs scan list does not grow.
SET LOCAL lock_timeout = '5s';

DO $$
DECLARE
    cname text;
BEGIN
    SELECT con.conname INTO cname
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    WHERE rel.relname = 'account_capacity_schedules' AND con.contype = 'c'
      AND pg_get_constraintdef(con.oid) LIKE '%user%'
      AND pg_get_constraintdef(con.oid) LIKE '%pool%'
      AND pg_get_constraintdef(con.oid) LIKE '%account%'
      AND pg_get_constraintdef(con.oid) NOT LIKE '%account_id%';
    IF cname IS NULL THEN
        RAISE EXCEPTION 'account capacity schedule scope check not found';
    END IF;
    EXECUTE format('ALTER TABLE account_capacity_schedules DROP CONSTRAINT %I', cname);
END $$;
ALTER TABLE account_capacity_schedules ADD CONSTRAINT account_capacity_schedules_scope_check
    CHECK (scope IN ('user', 'pool', 'account', 'group'));

CREATE FUNCTION aeon_capacity_schedule_group() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.scope = 'group' AND NOT EXISTS (
        SELECT 1 FROM account_groups g
        WHERE g.tenant_id = NEW.tenant_id AND g.id::text = NEW.scope_key
    ) THEN
        RAISE EXCEPTION 'invalid group schedule' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER account_capacity_schedules_group
    BEFORE INSERT OR UPDATE ON account_capacity_schedules
    FOR EACH ROW EXECUTE FUNCTION aeon_capacity_schedule_group();

CREATE TABLE account_ticket_pins (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    ticket_id uuid NOT NULL,
    harness text NOT NULL CHECK (harness IN ('codex', 'claude', 'pi', 'cursor', 'grok')),
    account_id uuid,
    group_id uuid,
    PRIMARY KEY (tenant_id, ticket_id, harness),
    FOREIGN KEY (tenant_id, ticket_id) REFERENCES nodes(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, account_id) REFERENCES agent_accounts(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, group_id) REFERENCES account_groups(tenant_id, id) ON DELETE CASCADE,
    CHECK ((account_id IS NULL) <> (group_id IS NULL))
);
ALTER TABLE account_ticket_pins ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_ticket_pins FORCE ROW LEVEL SECURITY;
CREATE POLICY account_ticket_pins_tenant ON account_ticket_pins
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE FUNCTION aeon_account_ticket_pin() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM nodes n
        JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
        WHERE n.tenant_id = NEW.tenant_id AND n.id = NEW.ticket_id
          AND k.slug = 'ticket' AND n.deleted_at IS NULL
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
CREATE TRIGGER account_ticket_pins_guard
    BEFORE INSERT OR UPDATE ON account_ticket_pins
    FOR EACH ROW EXECUTE FUNCTION aeon_account_ticket_pin();

CREATE TABLE account_run_targets (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    run_id uuid NOT NULL,
    group_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, run_id),
    FOREIGN KEY (tenant_id, run_id) REFERENCES agent_runs(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, group_id) REFERENCES account_groups(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE account_run_targets ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_run_targets FORCE ROW LEVEL SECURITY;
CREATE POLICY account_run_targets_tenant ON account_run_targets
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

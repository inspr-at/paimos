-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1142: a new workspace starts with Epic (layers) / Ticket (ticket) as its
-- levels and Task (task) as the leaf, saved as vocabulary revision 1.
-- New workspaces only: no backfill. Existing workspaces without a saved
-- vocabulary keep the code fallbacks (internal/nodes/work_shape.go).
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_seed_new_tenant_work_vocabulary() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
    -- Row-level security is forced on work_vocabulary: act as the new tenant.
    -- An existing row is kept. Only this transaction can see the new tenant,
    -- so no concurrent writer can add one between the check and the insert.
    PERFORM set_config('aeon.tenant_id', NEW.id::text, true);
    INSERT INTO work_vocabulary (tenant_id, revision, vocabulary)
    SELECT NEW.id, 1, '{"leaf":{"name":"Task","icon":"task"},"levels":[{"name":"Epic","icon":"layers"},{"name":"Ticket","icon":"ticket"}]}'::jsonb
    WHERE NOT EXISTS (SELECT 1 FROM work_vocabulary WHERE tenant_id = NEW.id);
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
    RAISE;
END;
$$;
CREATE TRIGGER tenants_seed_work_vocabulary
AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION aeon_seed_new_tenant_work_vocabulary();

-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-502 C: seed fresh tenants, including EnsureTenant's startup path.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_seed_new_tenant_work_kinds() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
    PERFORM set_config('aeon.tenant_id', NEW.id::text, true);
    PERFORM aeon_seed_work_kinds(NEW.id);
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
    RAISE;
END;
$$;
CREATE TRIGGER tenants_seed_work_kinds
AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION aeon_seed_new_tenant_work_kinds();

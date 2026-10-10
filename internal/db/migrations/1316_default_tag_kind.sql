-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-826: agents create tag nodes using the existing node authority.
-- Seed only a missing tag definition; tenant customizations remain untouched.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE FUNCTION aeon_seed_tag_kind(p_tenant_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF p_tenant_id IS DISTINCT FROM NULLIF(current_setting('aeon.tenant_id', true), '')::uuid THEN
        RAISE EXCEPTION 'tenant setting does not match tag-kind tenant';
    END IF;
    INSERT INTO node_kinds (tenant_id, slug, label, short_prefix, icon, field_schema)
    VALUES (p_tenant_id, 'tag', 'Tag', 'TAG', 'tag', '{}'::jsonb)
    ON CONFLICT (tenant_id, slug) DO NOTHING;
END;
$$;

CREATE FUNCTION aeon_seed_new_tenant_tag_kind() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
    PERFORM set_config('aeon.tenant_id', NEW.id::text, true);
    PERFORM aeon_seed_tag_kind(NEW.id);
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
    RAISE;
END;
$$;
CREATE TRIGGER tenants_seed_tag_kind
AFTER INSERT ON tenants FOR EACH ROW EXECUTE FUNCTION aeon_seed_new_tenant_tag_kind();

-- Backfill only tags, without restoring any deleted starter kinds.
DO $$
DECLARE
    t record;
    prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
    FOR t IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM set_config('aeon.tenant_id', t.id::text, true);
        PERFORM aeon_seed_tag_kind(t.id);
    END LOOP;
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
END;
$$;

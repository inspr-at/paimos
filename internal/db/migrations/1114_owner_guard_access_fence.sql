-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-558: binding removal must not upgrade an access writer's tenant fence
-- to FOR UPDATE. A node writer can already hold event_counters and still need
-- the tenant FK key-share lock. NO KEY UPDATE serializes owner removals and
-- deactivations (and conflicts with membership FOR UPDATE) without that cycle.
CREATE OR REPLACE FUNCTION aeon_protect_last_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_role uuid;
DECLARE remaining integer;
BEGIN
    PERFORM 1 FROM tenants WHERE id=OLD.tenant_id FOR NO KEY UPDATE;
    SELECT id INTO owner_role FROM roles WHERE tenant_id=OLD.tenant_id AND key='owner';
    IF TG_TABLE_NAME='role_bindings' THEN
        IF OLD.role_id IS DISTINCT FROM owner_role OR OLD.scope_type <> 'workspace' OR
           (TG_OP='UPDATE' AND NEW.role_id=owner_role AND NEW.scope_type='workspace') THEN
            RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
        END IF;
    ELSE
        IF OLD.status <> 'active' OR NEW.status='active' THEN RETURN NEW; END IF;
        IF NOT EXISTS (SELECT 1 FROM role_bindings WHERE tenant_id=OLD.tenant_id AND principal_id=OLD.id AND role_id=owner_role AND scope_type='workspace') THEN
            RETURN NEW;
        END IF;
    END IF;
    SELECT count(*) INTO remaining FROM role_bindings b
      JOIN principals p ON p.tenant_id=b.tenant_id AND p.id=b.principal_id
      WHERE b.tenant_id=OLD.tenant_id AND b.role_id=owner_role
        AND b.scope_type='workspace' AND p.status='active'
        AND (TG_TABLE_NAME <> 'principals' OR p.id <> OLD.id)
        AND (TG_TABLE_NAME <> 'role_bindings' OR b.id <> OLD.id);
    IF remaining=0 THEN RAISE EXCEPTION 'cannot remove the last active owner' USING ERRCODE='23514'; END IF;
    RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END;
$$;

-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-336: one System principal per tenant. Background jobs that write nodes
-- (the method-learning tagger first) attribute the event to that row.
SET LOCAL lock_timeout = '5s';

DO $$
DECLARE
    duplicates int;
BEGIN
    SELECT count(*) INTO duplicates FROM (
        SELECT tenant_id FROM principals
        WHERE kind = 'agent' AND name = 'System' AND roles @> ARRAY['system']::text[]
        GROUP BY tenant_id
        HAVING count(*) > 1
    ) extra;
    IF duplicates > 0 THEN
        RAISE EXCEPTION 'AEON-336: % tenants already have more than one System principal', duplicates;
    END IF;
END $$;

CREATE UNIQUE INDEX principals_system_actor ON principals (tenant_id)
    WHERE kind = 'agent' AND name = 'System' AND roles @> ARRAY['system']::text[];

-- Same actor as migration 0810, now serialized so two jobs cannot insert two.
CREATE OR REPLACE FUNCTION aeon_authz_system_actor(target_tenant uuid) RETURNS uuid
LANGUAGE plpgsql AS $$
DECLARE actor uuid;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('aeon-system-actor:' || target_tenant::text, 0));
    SELECT id INTO actor FROM principals
    WHERE tenant_id = target_tenant AND kind = 'agent' AND name = 'System' AND roles @> ARRAY['system']
    ORDER BY created_at, id LIMIT 1;
    IF actor IS NULL THEN
        INSERT INTO principals(tenant_id, kind, name, roles)
        VALUES (target_tenant, 'agent', 'System', ARRAY['system']) RETURNING id INTO actor;
        INSERT INTO events(tenant_id, actor_principal_id, type, after)
        VALUES (target_tenant, actor, 'principal.created',
                jsonb_build_object('id', actor, 'kind', 'agent', 'name', 'System', 'roles', ARRAY['system']));
    END IF;
    RETURN actor;
END;
$$;

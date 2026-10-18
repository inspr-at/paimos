-- SPDX-License-Identifier: AGPL-3.0-only
-- aeon:contract-phase AEON-1148 expanded-in=v261010123154.0.0 expansion-migration=1309_account_use_matrix.sql
SET LOCAL lock_timeout = '5s';

-- The two settings pages use allow/deny. Preserve the existing off mirror,
-- advance the revision to reject pre-upgrade forms, and audit only changed rows.
-- FORCE RLS remains enabled. Fence every tenant before resources, and finish
-- all resource writes before taking any event-counter locks.
DO $$
DECLARE
    tenant uuid;
    actor uuid;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_visible text := current_setting('aeon.visible_projects',true);
    before_rule jsonb;
    after_rule jsonb;
    changes jsonb := '[]'::jsonb;
    change jsonb;
BEGIN
    PERFORM id FROM tenants ORDER BY id FOR NO KEY UPDATE;
    FOR tenant IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM pg_advisory_xact_lock(hashtextextended(tenant::text,0));
        PERFORM pg_advisory_xact_lock(hashtextextended('aeon-model-registry:'||tenant::text,0));
        PERFORM pg_advisory_xact_lock(hashtextextended('aeon-account-use:'||tenant::text,0));
    END LOOP;
    PERFORM set_config('aeon.visible_projects','*',true);
    FOR tenant IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM set_config('aeon.tenant_id',tenant::text,true);
        SELECT to_jsonb(r) INTO before_rule FROM account_use_rules r
            WHERE tenant_id=tenant AND new_models='shipped_only' FOR UPDATE;
        IF before_rule IS NULL THEN CONTINUE; END IF;
        SELECT id INTO STRICT actor FROM principals WHERE tenant_id=tenant
            AND kind='agent' AND name='System' AND roles @> ARRAY['system']::text[];
        UPDATE account_use_rules SET new_models='deny',revision=revision+1
            WHERE tenant_id=tenant;
        SELECT to_jsonb(r) INTO after_rule FROM account_use_rules r WHERE tenant_id=tenant;
        changes := changes || jsonb_build_array(jsonb_build_object(
            'tenant',tenant,'actor',actor,'before',before_rule,'after',after_rule));
    END LOOP;
    FOR change IN SELECT value FROM jsonb_array_elements(changes) LOOP
        PERFORM set_config('aeon.tenant_id',change->>'tenant',true);
        INSERT INTO events(tenant_id,actor_principal_id,type,before,after,metadata)
            VALUES((change->>'tenant')::uuid,(change->>'actor')::uuid,
                'account_use.rules_changed',change->'before',change->'after',
                jsonb_build_object('migration','1329','ticket','AEON-1148'));
    END LOOP;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
END $$;

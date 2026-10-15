-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- New immutable revisions of the three previously shipped pi pins. Retain
-- every old profile, display, account grant and historical run reference.
-- The activation trigger still enforces each person's new-model policy.
CREATE FUNCTION aeon_upgrade_anthropic_55() RETURNS SETOF model_profiles
LANGUAGE plpgsql AS $$
DECLARE
    tenant uuid;
    pin record;
    next_id uuid;
    next_enabled boolean;
    actor uuid;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_visible text := current_setting('aeon.visible_projects',true);
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
        FOR pin IN
            SELECT DISTINCT ON (p.slug) p.*,coalesce(p.registered_effort_level,d.effort_level) AS native_level,
                p.enabled AND (rules.enforced_at IS NULL OR rules.new_models IN ('allow','shipped_only')) AS activate
            FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
            JOIN account_use_rules rules ON rules.tenant_id=p.tenant_id
            WHERE p.tenant_id=tenant AND p.harness='pi' AND p.family='anthropic'
                AND ((p.slug IN ('pi-anthropic-opus-high','pi-anthropic-opus-xhigh') AND p.model='anthropic/claude-opus-5')
                    OR (p.slug='pi-anthropic-sonnet-high' AND p.model='anthropic/claude-sonnet-5'))
                AND NOT EXISTS(SELECT 1 FROM model_profile_retirements r WHERE r.tenant_id=p.tenant_id AND r.profile_id=p.id)
            ORDER BY p.slug,p.created_at DESC,p.id
        LOOP
            PERFORM set_config('aeon.model_activation_cause','shipped_catalog',true);
            INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled,display_overrides,source,note,registered_effort_level)
            -- Apply the live policy under the catalog/matrix fences before
            -- insert. Denied pins are explicitly inactive, like discoveries;
            -- no deferred activation audit may outlive this tenant's scope.
            SELECT tenant,pin.slug,'aeon-1409','pi','anthropic',pin.model||'.5',pin.effort,pin.tier,pin.activate,
                jsonb_build_object('display_name',CASE WHEN pin.slug='pi-anthropic-sonnet-high' THEN 'Claude Sonnet' ELSE 'Claude Opus' END,
                    'short_name',CASE WHEN pin.slug='pi-anthropic-sonnet-high' THEN 'Sonnet' ELSE 'Opus' END,
                    'model_version','5.5','origin','shipped'),pin.source,pin.note,pin.native_level
            WHERE NOT EXISTS(SELECT 1 FROM model_profiles WHERE tenant_id=tenant AND slug=pin.slug AND version='aeon-1409')
            RETURNING id,enabled INTO next_id,next_enabled;
            PERFORM set_config('aeon.model_activation_cause','',true);
            IF next_id IS NULL THEN CONTINUE; END IF;
            INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by,set_at)
                SELECT tenant_id,harness,pin.model||'.5',until_at,reason,set_by,set_at
                FROM model_pauses WHERE tenant_id=tenant AND harness='pi' AND model=pin.model
                    AND NOT EXISTS(SELECT 1 FROM model_pauses WHERE tenant_id=tenant AND harness='pi' AND model=pin.model||'.5');
            SELECT aeon_authz_system_actor(tenant) INTO actor;
            changes := changes || jsonb_build_array(jsonb_build_object(
                'tenant',tenant,'actor',actor,'from_profile',pin.id,'profile_id',next_id,'enabled',next_enabled));
        END LOOP;
    END LOOP;
    -- Finish all resource writes before the first event-counter lock.
    FOR change IN SELECT value FROM jsonb_array_elements(changes) LOOP
        PERFORM set_config('aeon.tenant_id',change->>'tenant',true);
        INSERT INTO events(tenant_id,actor_principal_id,type,after,metadata)
            VALUES((change->>'tenant')::uuid,(change->>'actor')::uuid,'model.profile_upgraded',
                change-'tenant'-'actor',jsonb_build_object('migration','1357','ticket','AEON-1409'));
    END LOOP;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.visible_projects',coalesce(prior_visible,''),true);
    RETURN;
END $$;

-- The invoker function completes its writes under FORCE RLS and emits no rows.
INSERT INTO model_profiles SELECT * FROM aeon_upgrade_anthropic_55();

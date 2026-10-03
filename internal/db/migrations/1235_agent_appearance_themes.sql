-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-643: retain every physical person's appearance and private ownership.
-- Legacy preference rows are kept for rollback/old binaries, never deleted.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- Fence legacy appearance writers, identity links and theme writes throughout
-- the one-time backfill. Acquire all resource locks before the event counter.
LOCK TABLE tenants, principals, user_preferences, themes, theme_selections IN SHARE ROW EXCLUSIVE MODE;
CREATE TEMP TABLE agent_theme_audit (tenant_id uuid NOT NULL, actor_id uuid NOT NULL,
    type text NOT NULL, before jsonb, after jsonb NOT NULL, audience uuid NOT NULL) ON COMMIT DROP;

-- Snapshot explicit effective choices before any backfill writes. A canonical
-- account inherits a saved alias choice (including explicit default); creating
-- a saved canonical or lower-UUID alias selection would silently displace it.
-- Backfills in those groups retain lower-priority physical selections: they
-- become effective after unlink without replacing the explicit linked choice.
CREATE TEMP TABLE agent_theme_explicit_groups (tenant_id uuid NOT NULL, canonical_id uuid NOT NULL, PRIMARY KEY(tenant_id,canonical_id)) ON COMMIT DROP;

-- The normal guard requires a canonical owner at creation. A migrated alias
-- must keep its own immutable owner so unlink restores its own choices. Only
-- this locked migration bypasses that trigger; all constraints/RLS remain on.
ALTER TABLE themes DISABLE TRIGGER themes_guard;
DO $$
DECLARE
    target uuid;
    person record;
    seeded themes%ROWTYPE;
    chosen theme_selections%ROWTYPE;
    prior_choice jsonb;
    indicator jsonb;
    states jsonb;
    agent_config jsonb;
    default_config jsonb;
    style text;
    palette text;
    actor uuid;
    inherited_explicit boolean;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_system text := current_setting('aeon.system',true);
BEGIN
    PERFORM set_config('aeon.system','on',true);
    FOR target IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM set_config('aeon.tenant_id',target::text,true);
        SELECT config INTO STRICT default_config FROM themes WHERE tenant_id=target AND scope='default';
        actor := aeon_authz_system_actor(target);
        INSERT INTO agent_theme_explicit_groups(tenant_id,canonical_id)
            SELECT DISTINCT p.tenant_id, coalesce(p.linked_to,p.id) FROM principals p
            JOIN theme_selections s ON s.tenant_id=p.tenant_id AND s.principal_id=p.id
            WHERE p.tenant_id=target AND p.kind='person' AND p.status='active'
                AND s.unsaved_revision IS DISTINCT FROM s.revision;
        FOR person IN SELECT p.id, coalesce(p.linked_to,p.id) AS canonical_id FROM principals p WHERE p.tenant_id=target AND p.kind='person'
            AND EXISTS (SELECT 1 FROM user_preferences u WHERE u.tenant_id=target AND u.principal_id=p.id
                AND ((u.key='agent-indicator' AND u.value ?| ARRAY['style','ring','hovering','size'])
                  OR (u.key='agent-state' AND u.value ?| ARRAY['palette','dimInactive','inactiveOpacity'])))
            ORDER BY p.id LOOP
            SELECT * INTO chosen FROM theme_selections WHERE tenant_id=target AND principal_id=person.id;
            -- A saved theme choice already made through the additive API wins.
            IF FOUND AND chosen.unsaved_revision IS DISTINCT FROM chosen.revision THEN CONTINUE; END IF;
            inherited_explicit := EXISTS (SELECT 1 FROM agent_theme_explicit_groups WHERE tenant_id=target AND canonical_id=person.canonical_id);
            prior_choice := jsonb_build_object('principal_id',person.id,'theme_id',chosen.theme_id,
                'revision',coalesce(chosen.revision,0),'unsaved',true);
            SELECT value INTO indicator FROM user_preferences WHERE tenant_id=target AND principal_id=person.id AND key='agent-indicator';
            SELECT value INTO states FROM user_preferences WHERE tenant_id=target AND principal_id=person.id AND key='agent-state';
            indicator := coalesce(indicator,'{}'::jsonb); states := coalesce(states,'{}'::jsonb);
            -- Mirror the shipped client normalization, including legacy names
            -- and native (null) geometry. A saved palette alone kept Robot 1.
            style := CASE indicator->>'style' WHEN 'calm' THEN 'robot-1' WHEN 'playful' THEN 'robot-5' ELSE indicator->>'style' END;
            IF style IS NULL OR style NOT IN ('pulse','robot-1','robot-2','robot-3','robot-4','robot-5','orbit','quill','sprite') THEN style := 'robot-1'; END IF;
            palette := CASE states->>'palette' WHEN 'colour-blind' THEN 'deutan' ELSE states->>'palette' END;
            IF palette IS NULL OR palette NOT IN ('standard','protan','deutan','tritan','monochrome') THEN palette := 'standard'; END IF;
            agent_config := jsonb_build_object('avatar',style,'ring',CASE WHEN indicator->>'ring' IN ('moving','still','off') THEN indicator->>'ring' ELSE NULL END,
                'hover',coalesce(indicator->'hovering'='true'::jsonb,false),
                'size',CASE WHEN jsonb_typeof(indicator->'size')='number' THEN greatest(30,least(100,floor((indicator->>'size')::numeric+0.5))) ELSE NULL END,
                'palette',palette,'dim_inactive',coalesce(states->'dimInactive'<>'false'::jsonb,true),
                'inactive_opacity',CASE WHEN jsonb_typeof(states->'inactiveOpacity')='number' THEN greatest(40,least(80,floor((states->>'inactiveOpacity')::numeric+0.5))) ELSE 55 END);
            INSERT INTO themes(tenant_id,name,scope,owner_principal_id,config)
                VALUES(target,'My agent appearance','personal',person.id,jsonb_set(default_config,'{agents}',agent_config)) RETURNING * INTO seeded;
            INSERT INTO theme_selections(tenant_id,principal_id,theme_id,revision,unsaved_revision)
                VALUES(target,person.id,seeded.id,coalesce(chosen.revision,0)+1,
                    CASE WHEN inherited_explicit THEN coalesce(chosen.revision,0)+1 ELSE NULL END)
                ON CONFLICT(tenant_id,principal_id) DO UPDATE SET theme_id=EXCLUDED.theme_id,revision=EXCLUDED.revision,unsaved_revision=EXCLUDED.unsaved_revision RETURNING * INTO chosen;
            INSERT INTO agent_theme_audit VALUES(target,actor,'theme.created',NULL,
                jsonb_build_object('id',seeded.id,'tenant_id',target,'name',seeded.name,'scope',seeded.scope,
                    'owner_principal_id',seeded.owner_principal_id,'values',seeded.config,'revision',seeded.revision,
                    'created_at',seeded.created_at,'updated_at',seeded.updated_at),person.id);
            INSERT INTO agent_theme_audit VALUES(target,actor,'theme.selected',prior_choice,
                jsonb_build_object('principal_id',person.id,'theme_id',chosen.theme_id,'revision',chosen.revision)
                    || CASE WHEN inherited_explicit THEN '{"unsaved":true}'::jsonb ELSE '{}'::jsonb END,person.id);
        END LOOP;
    END LOOP;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.system',coalesce(prior_system,''),true);
END;
$$;
ALTER TABLE themes ENABLE TRIGGER themes_guard;
-- Restore the creation guard before any append. All data is final and no
-- resource lock is acquired after the first tenant event-counter allocation.
DO $$
DECLARE
    change record;
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_system text := current_setting('aeon.system',true);
BEGIN
    PERFORM set_config('aeon.system','on',true);
    FOR change IN SELECT * FROM agent_theme_audit ORDER BY tenant_id,audience,type LOOP
        PERFORM set_config('aeon.tenant_id',change.tenant_id::text,true);
        INSERT INTO events(tenant_id,actor_principal_id,type,before,after,metadata)
            VALUES(change.tenant_id,change.actor_id,change.type,change.before,change.after,
                jsonb_build_object('audience_principal_id',change.audience,'migration','AEON-643'));
    END LOOP;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.system',coalesce(prior_system,''),true);
END;
$$;

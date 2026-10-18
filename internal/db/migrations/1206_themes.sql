-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-641: workspace and personal themes; reversible tombstones preserve
-- choices without unbounded user-list snapshots or overwriting later choices.
SET LOCAL lock_timeout = '5s';

CREATE TABLE themes (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80 AND name=btrim(name)),
    scope text NOT NULL CHECK (scope IN ('default','workspace','personal')),
    owner_principal_id uuid,
    config jsonb NOT NULL CHECK (jsonb_typeof(config)='object' AND octet_length(config::text)<=4096),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,
    PRIMARY KEY (tenant_id,id),
    FOREIGN KEY (tenant_id,owner_principal_id) REFERENCES principals(tenant_id,id),
    CHECK ((scope='personal') = (owner_principal_id IS NOT NULL)),
    CHECK (scope<>'default' OR deleted_at IS NULL)
);
CREATE UNIQUE INDEX themes_workspace_default ON themes(tenant_id) WHERE scope='default';
CREATE INDEX themes_visible_page ON themes(tenant_id,id) WHERE deleted_at IS NULL;

CREATE TABLE theme_selections (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    principal_id uuid NOT NULL,
    theme_id uuid,
    revision bigint NOT NULL CHECK (revision>0),
    PRIMARY KEY (tenant_id,principal_id),
    FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id),
    FOREIGN KEY (tenant_id,theme_id) REFERENCES themes(tenant_id,id)
);

ALTER TABLE themes ENABLE ROW LEVEL SECURITY;
ALTER TABLE themes FORCE ROW LEVEL SECURITY;
CREATE POLICY themes_tenant ON themes
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY themes_owner ON themes AS RESTRICTIVE
    USING (scope<>'personal' OR (SELECT aeon_visibility_system())
        OR owner_principal_id=ANY((SELECT aeon_current_principals())::uuid[]))
    WITH CHECK (scope<>'personal' OR (SELECT aeon_visibility_system())
        OR owner_principal_id=ANY((SELECT aeon_current_principals())::uuid[]));
ALTER TABLE theme_selections ENABLE ROW LEVEL SECURITY;
ALTER TABLE theme_selections FORCE ROW LEVEL SECURITY;
CREATE POLICY theme_selections_tenant ON theme_selections
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE POLICY theme_selections_owner ON theme_selections AS RESTRICTIVE
    USING ((SELECT aeon_visibility_system()) OR principal_id=ANY((SELECT aeon_current_principals())::uuid[]))
    WITH CHECK ((SELECT aeon_visibility_system()) OR principal_id=ANY((SELECT aeon_current_principals())::uuid[]));

-- Privacy also covers generic history and SSE, which otherwise expose
-- workspace events to managers. Agents never inherit a key creator's themes.
CREATE POLICY events_theme_audience ON events AS RESTRICTIVE FOR SELECT
    USING (type NOT LIKE 'theme.%' OR (SELECT aeon_visibility_system())
        OR coalesce(after->>'scope',before->>'scope') IN ('default','workspace')
        OR metadata->>'audience_principal_id'=ANY((SELECT aeon_current_principals())::text[]));

CREATE FUNCTION aeon_theme_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        IF OLD.scope='default' THEN
            RAISE EXCEPTION 'workspace default cannot be deleted' USING ERRCODE='23514';
        END IF;
        RETURN OLD;
    END IF;
    IF TG_OP='UPDATE' AND (NEW.tenant_id,NEW.id,NEW.scope,NEW.owner_principal_id,NEW.created_at)
        IS DISTINCT FROM (OLD.tenant_id,OLD.id,OLD.scope,OLD.owner_principal_id,OLD.created_at) THEN
        RAISE EXCEPTION 'theme identity, scope and ownership are immutable' USING ERRCODE='23514';
    END IF;
    IF NEW.scope='personal' AND NOT EXISTS (SELECT 1 FROM principals
        WHERE tenant_id=NEW.tenant_id AND id=NEW.owner_principal_id AND kind='person' AND linked_to IS NULL) THEN
        RAISE EXCEPTION 'personal theme owner must be a canonical person' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER themes_guard BEFORE INSERT OR UPDATE OR DELETE ON themes
    FOR EACH ROW EXECUTE FUNCTION aeon_theme_guard();

CREATE FUNCTION aeon_theme_selection_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM principals WHERE tenant_id=NEW.tenant_id
        AND id=NEW.principal_id AND kind='person' AND linked_to IS NULL) THEN
        RAISE EXCEPTION 'theme choice belongs to a canonical person' USING ERRCODE='23514';
    END IF;
    -- A tombstone is valid for fallback and undo, but a different person's
    -- theme is never selectable, even by a direct SQL or system writer.
    IF NEW.theme_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM themes
        WHERE tenant_id=NEW.tenant_id AND id=NEW.theme_id
        AND (scope<>'personal' OR owner_principal_id=NEW.principal_id)) THEN
        RAISE EXCEPTION 'theme choice must be visible to its owner' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER theme_selections_guard BEFORE INSERT OR UPDATE ON theme_selections
    FOR EACH ROW EXECUTE FUNCTION aeon_theme_selection_guard();

-- Fixed seed only, for both existing tenants and new tenant bootstrap. Restore
-- the calling RLS context, and acquire resource locks before any event append.
CREATE FUNCTION aeon_seed_theme(target uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE
    prior_tenant text := current_setting('aeon.tenant_id',true);
    prior_system text := current_setting('aeon.system',true);
    seeded themes%ROWTYPE;
    actor uuid;
    snapshot jsonb;
BEGIN
    PERFORM set_config('aeon.tenant_id',target::text,true);
    PERFORM set_config('aeon.system','on',true);
    INSERT INTO themes(tenant_id,name,scope,config)
    VALUES(target,'Porcelain','default','{"primary":{"light":"#0e6f6c","dark":"#a4e5df"},"secondary":{"light":"#d69b31","dark":"#e2b45a"},"recurring_marker":{"source":"secondary","custom":null},"agents":{"avatar":"robot-1","ring":null,"hover":false,"size":null,"palette":"standard"}}'::jsonb)
    ON CONFLICT DO NOTHING RETURNING * INTO seeded;
    IF seeded.id IS NOT NULL THEN
        snapshot := jsonb_build_object('id',seeded.id,'tenant_id',target,'name',seeded.name,
            'scope',seeded.scope,'owner_principal_id',null,'values',seeded.config,
            'revision',seeded.revision,'created_at',seeded.created_at,'updated_at',seeded.updated_at);
        actor := aeon_authz_system_actor(target);
        INSERT INTO events(tenant_id,actor_principal_id,type,after)
        VALUES(target,actor,'theme.created',snapshot);
    END IF;
    PERFORM set_config('aeon.tenant_id',coalesce(prior_tenant,''),true);
    PERFORM set_config('aeon.system',coalesce(prior_system,''),true);
END;
$$;
REVOKE EXECUTE ON FUNCTION aeon_seed_theme(uuid) FROM PUBLIC;
CREATE FUNCTION aeon_seed_theme_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
BEGIN
    PERFORM aeon_seed_theme(NEW.id);
    RETURN NEW;
END;
$$;
REVOKE EXECUTE ON FUNCTION aeon_seed_theme_trigger() FROM PUBLIC;
CREATE TRIGGER tenants_seed_theme AFTER INSERT ON tenants
    FOR EACH ROW EXECUTE FUNCTION aeon_seed_theme_trigger();
DO $$
DECLARE target uuid;
BEGIN
    FOR target IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM aeon_seed_theme(target);
    END LOOP;
END;
$$;

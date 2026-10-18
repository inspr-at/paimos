-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-511C. Stored, additive display metadata; never change immutable pins.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_model_effort_level(family text, effort text) RETURNS smallint
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE e text := lower(btrim(effort)); budget numeric;
BEGIN
    IF family IN ('openai','anthropic','xai') THEN
        RETURN CASE e WHEN 'minimal' THEN CASE WHEN family='openai' THEN 0 END
            WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'xhigh' THEN 4
            WHEN 'max' THEN CASE WHEN family='anthropic' THEN 5 END END;
    END IF;
    IF family='google' THEN
        IF e='off' THEN RETURN 0; END IF;
        -- Budgets are token counts, with binary k boundaries; auto/-1 is unknown.
        IF e ~ '^[0-9]+$' THEN
            budget := e::numeric;
            RETURN CASE WHEN budget=0 THEN 0 WHEN budget<=1024 THEN 1
                WHEN budget<=4096 THEN 2 WHEN budget<=16384 THEN 3
                WHEN budget<=32768 THEN 4 ELSE 5 END;
        END IF;
    END IF;
    RETURN NULL;
END $$;

CREATE FUNCTION aeon_model_display(harness text, model text, overrides jsonb DEFAULT '{}') RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE name text; short text; ver text := ''; m text[]; raw text := btrim(model);
BEGIN
    -- Extract only explicit, recognised model IDs. Aliases never acquire a version.
    IF harness='codex' THEN
        m := regexp_match(raw, '^gpt-([0-9]+(?:[.][0-9]+)*)-(astra|sol|terra|luna)$');
        IF m IS NOT NULL THEN short := initcap(m[2]); name := 'Codex '||short; ver := m[1]; END IF;
    END IF;
    IF harness IN ('claude','pi') THEN
        m := regexp_match(raw, '^(?:anthropic/)?(?:claude-)?(opus|sonnet|haiku|fable)(?:-([0-9]+)(?:[-.]([0-9]{1,2}))?)?$');
        IF m IS NOT NULL THEN
            short := initcap(m[1]); name := 'Claude '||short;
            ver := coalesce(m[2],'')||CASE WHEN m[3] IS NOT NULL THEN '.'||m[3] ELSE '' END;
        END IF;
    END IF;
    m := regexp_match(raw, '^grok-([0-9]+(?:[.][0-9]+)*)(?:-(?:low|medium|high|xhigh))?(-fast)?$');
    IF m IS NOT NULL THEN short := 'Grok'||CASE WHEN m[2] IS NOT NULL THEN ' fast' ELSE '' END; name := short; ver := m[1]; END IF;
    IF harness='cursor' THEN
        m := regexp_match(raw, '^composer-([0-9]+(?:[.][0-9]+)*)(-fast)?$');
        IF m IS NOT NULL THEN short := 'Composer'||CASE WHEN m[2] IS NOT NULL THEN ' fast' ELSE '' END; name := 'Cursor '||short; ver := m[1]; END IF;
    END IF;
    m := regexp_match(raw, '^(?:google/)?gemini-([0-9]+(?:[.][0-9]+)*)(?:-(pro|flash|flash-lite))?$');
    IF m IS NOT NULL THEN short := 'Gemini'||CASE WHEN m[2] IS NOT NULL THEN ' '||initcap(replace(m[2],'-',' ')) ELSE '' END; name := short; ver := m[1]; END IF;
    RETURN jsonb_build_object('display_name',coalesce(overrides->>'display_name',name,initcap(harness)||' '||raw),
        'short_name',coalesce(overrides->>'short_name',short,raw),
        'model_version',coalesce(overrides->>'model_version',ver));
END $$;

-- Existing family constraints and immutable pins remain intact. Pi's explicit
-- registered Gemini IDs carry Google presentation metadata independently.
CREATE FUNCTION aeon_model_provider(family text, harness text, model text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN family='unknown' AND harness='pi'
        AND model ~ '^(google/)?gemini-[0-9]+([.][0-9]+)*(-(pro|flash|flash-lite))?$'
        THEN 'google' ELSE family END;
$$;

ALTER TABLE model_profiles
    ADD COLUMN display_overrides jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(display_overrides)='object');

CREATE TABLE model_profile_display (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    profile_id uuid NOT NULL,
    model_display jsonb NOT NULL CHECK (jsonb_typeof(model_display)='object'),
    provider text NOT NULL,
    effort_level smallint CHECK (effort_level BETWEEN 0 AND 5),
    PRIMARY KEY (tenant_id, profile_id),
    FOREIGN KEY (tenant_id, profile_id) REFERENCES model_profiles(tenant_id,id)
);
ALTER TABLE model_profile_display ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_profile_display FORCE ROW LEVEL SECURITY;
CREATE POLICY model_profile_display_tenant ON model_profile_display
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE TRIGGER model_profile_display_immutable BEFORE UPDATE OR DELETE ON model_profile_display
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();

CREATE FUNCTION aeon_store_model_profile_display() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE provider text := aeon_model_provider(NEW.family,NEW.harness,NEW.model);
BEGIN
    INSERT INTO model_profile_display(tenant_id,profile_id,model_display,provider,effort_level)
    VALUES(NEW.tenant_id,NEW.id,aeon_model_display(NEW.harness,NEW.model,NEW.display_overrides),
        provider,aeon_model_effort_level(provider,NEW.effort));
    RETURN NEW;
END $$;
CREATE TRIGGER model_profile_display_insert AFTER INSERT ON model_profiles
    FOR EACH ROW EXECUTE FUNCTION aeon_store_model_profile_display();

-- Called for every tenant by the migration runner, under ordinary FORCE RLS.
-- Insert-only and idempotent: historical profile IDs and pin revisions survive.
CREATE FUNCTION aeon_backfill_model_profile_display() RETURNS bigint
LANGUAGE sql VOLATILE AS $$
    WITH inserted AS (
        INSERT INTO model_profile_display(tenant_id,profile_id,model_display,provider,effort_level)
        SELECT p.tenant_id,p.id,aeon_model_display(p.harness,p.model,p.display_overrides),
            aeon_model_provider(p.family,p.harness,p.model),
            aeon_model_effort_level(aeon_model_provider(p.family,p.harness,p.model),p.effort)
        FROM model_profiles p
        WHERE p.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
            AND NOT EXISTS (SELECT 1 FROM model_profile_display d WHERE d.tenant_id=p.tenant_id AND d.profile_id=p.id)
        RETURNING profile_id
    ) SELECT count(*) FROM inserted;
$$;

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
        m := regexp_match(raw, '^gpt-([0-9]+(?:\.[0-9]+)*)-(astra|sol|terra|luna)$');
        IF m IS NOT NULL THEN short := initcap(m[2]); name := 'Codex '||short; ver := m[1]; END IF;
    END IF;
    IF harness IN ('claude','pi') THEN
        m := regexp_match(raw, '^(?:anthropic/)?(?:claude-)?(opus|sonnet|haiku|fable)(?:-([0-9]+)(?:[-.]([0-9]{1,2}))?)?$');
        IF m IS NOT NULL THEN
            short := initcap(m[1]); name := 'Claude '||short;
            ver := coalesce(m[2],'')||CASE WHEN m[3] IS NOT NULL THEN '.'||m[3] ELSE '' END;
        END IF;
    END IF;
    m := regexp_match(raw, '^grok-([0-9]+(?:\.[0-9]+)*)(?:-(?:low|medium|high|xhigh))?(-fast)?$');
    IF m IS NOT NULL THEN short := 'Grok'||CASE WHEN m[2] IS NOT NULL THEN ' fast' ELSE '' END; name := short; ver := m[1]; END IF;
    IF harness='cursor' THEN
        m := regexp_match(raw, '^composer-([0-9]+(?:\.[0-9]+)*)(-fast)?$');
        IF m IS NOT NULL THEN short := 'Composer'||CASE WHEN m[2] IS NOT NULL THEN ' fast' ELSE '' END; name := 'Cursor '||short; ver := m[1]; END IF;
    END IF;
    RETURN jsonb_build_object('display_name',coalesce(overrides->>'display_name',name,initcap(harness)||' '||raw),
        'short_name',coalesce(overrides->>'short_name',short,raw),
        'model_version',coalesce(overrides->>'model_version',ver));
END $$;

ALTER TABLE model_profiles DROP CONSTRAINT model_profiles_family_check;
ALTER TABLE model_profiles ADD CONSTRAINT model_profiles_family_check
    CHECK (family IN ('openai','anthropic','xai','cursor','google') OR (harness='pi' AND family='unknown'));
ALTER TABLE model_profiles
    ADD COLUMN display_overrides jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(display_overrides)='object'),
    ADD COLUMN model_display jsonb GENERATED ALWAYS AS (aeon_model_display(harness,model,display_overrides)) STORED,
    ADD COLUMN effort_level smallint GENERATED ALWAYS AS (aeon_model_effort_level(family,effort)) STORED;

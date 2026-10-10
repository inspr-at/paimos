-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Immutable model IDs determine a line, never editable presentation metadata
-- or the unrelated registry revision. Keep in step with ProfileLine.
CREATE FUNCTION aeon_model_line(harness text, model text) RETURNS text[]
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE raw text := model; m text[]; suffix text := '';
BEGIN
    IF harness='codex' THEN
        m := regexp_match(raw, '^gpt-([0-9]+(?:[.][0-9]+)*)-(.+)$');
        IF m IS NOT NULL THEN RETURN ARRAY[m[2],m[1]]; END IF;
        RETURN ARRAY[raw,''];
    END IF;
    IF harness='opencode' THEN
        raw := regexp_replace(raw, '^(google/|ollama/)', '');
    END IF;
    IF harness='pi' THEN raw := regexp_replace(raw, '^anthropic/', ''); END IF;
    IF harness IN ('claude','pi') THEN
        raw := regexp_replace(raw, '^claude-', '');
        IF raw IN ('opus','sonnet','haiku','fable') THEN RETURN ARRAY[raw,'alias']; END IF;
    END IF;
    IF harness='cursor' THEN
        m := regexp_match(raw, '^(.+?)(-(low|medium|high|xhigh|max|ultra))?(-fast)?$');
        IF m IS NOT NULL THEN raw := m[1]; suffix := coalesce(m[4],''); END IF;
    END IF;
    m := regexp_match(raw, '^([a-z][a-z-]*?)-?([0-9]+(?:[.-][0-9]+)*)(.*)$');
    IF m IS NOT NULL THEN RETURN ARRAY[m[1]||m[3]||suffix,replace(m[2],'-','.')]; END IF;
    RETURN ARRAY[model,''];
END $$;

CREATE FUNCTION aeon_model_version_newer(candidate text, pinned text) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE a numeric[]; b numeric[];
BEGIN
    IF candidate=pinned OR candidate='' OR pinned='' OR pinned='alias' THEN RETURN false; END IF;
    IF candidate='alias' THEN RETURN true; END IF;
    IF candidate !~ '^[0-9]+([.][0-9]+)*$' OR pinned !~ '^[0-9]+([.][0-9]+)*$' THEN RETURN false; END IF;
    a := string_to_array(candidate,'.')::numeric[];
    b := string_to_array(pinned,'.')::numeric[];
    WHILE cardinality(a)>1 AND a[cardinality(a)]=0 LOOP a := a[1:cardinality(a)-1]; END LOOP;
    WHILE cardinality(b)>1 AND b[cardinality(b)]=0 LOOP b := b[1:cardinality(b)-1]; END LOOP;
    RETURN a>b;
END $$;

-- NULL allows the harness catalog; an explicit empty list still denies all.
-- Only registered, same-tenant/family/harness successors inherit a pin. Equal
-- versions do not acquire extra efforts, and different provider namespaces,
-- lines and variants cannot borrow its authority. Enablement and admission
-- remain separate live checks at every caller.
CREATE FUNCTION aeon_account_allows_profile(account_harness text, allowed uuid[], target uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM model_profiles p
        WHERE p.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
          AND p.id=target AND p.harness=account_harness
          AND (allowed IS NULL OR p.id=ANY(allowed) OR EXISTS (
              SELECT 1 FROM model_profiles pin
              WHERE pin.tenant_id=p.tenant_id AND pin.id=ANY(allowed)
                AND pin.harness=p.harness AND pin.family=p.family
                AND (aeon_model_line(pin.harness,pin.model))[1]=(aeon_model_line(p.harness,p.model))[1]
                AND aeon_model_version_newer((aeon_model_line(p.harness,p.model))[2],(aeon_model_line(pin.harness,pin.model))[2])
          ))
    );
$$;

CREATE FUNCTION aeon_account_allowed_profile_ids(account_harness text, allowed uuid[]) RETURNS uuid[]
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN allowed IS NULL THEN NULL ELSE ARRAY(
        SELECT id FROM (
            SELECT unnest(allowed) AS id
            UNION
            SELECT p.id FROM model_profiles p
            WHERE p.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
              AND p.harness=account_harness AND aeon_account_allows_profile(account_harness,allowed,p.id)
        ) effective ORDER BY id
    ) END;
$$;

-- Widen the effective policy without rewriting immutable enrollment pins.
-- Old binaries can roll back safely using the original exact-profile grants.
-- The runner invokes this under each tenant's FORCE RLS in the migration
-- transaction, after acquiring all tenant fences and before event counters.
CREATE FUNCTION aeon_backfill_account_model_successors() RETURNS bigint
LANGUAGE sql VOLATILE AS $$
    WITH recorded AS (
        INSERT INTO events(tenant_id,actor_principal_id,type,before,after,metadata)
        SELECT a.tenant_id,a.registered_by_principal_id,'account.model_successors_enabled',
            jsonb_build_object('account_id',a.id,'allowed_model_profile_ids',a.allowed_model_profile_ids),
            jsonb_build_object('account_id',a.id,'effective_model_profile_ids',aeon_account_allowed_profile_ids(a.harness,a.allowed_model_profile_ids)),
            jsonb_build_object('migration','1296','policy','same_line_successors')
        FROM agent_accounts a
        WHERE a.tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid
          AND cardinality(a.allowed_model_profile_ids)>0
          AND NOT EXISTS (SELECT 1 FROM events e WHERE e.tenant_id=a.tenant_id
              AND e.type='account.model_successors_enabled' AND e.after->>'account_id'=a.id::text)
        ORDER BY a.id RETURNING id
    ) SELECT count(*) FROM recorded;
$$;

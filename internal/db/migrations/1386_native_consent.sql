-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1346: approval-native facts expansion. Existing approval writers leave
-- native_consent NULL. No old permission, request or grant is reinterpreted.
ALTER TABLE approval_requests ADD COLUMN native_consent jsonb;

CREATE TABLE native_session_consents (
    tenant_id uuid,
    grant_id uuid,
    owner_person_id uuid,
    agent_principal_id uuid,
    project_id uuid,
    computer_id uuid,
    account_id uuid,
    workspace_id text,
    workspace_root text,
    model_profile_id uuid,
    tool_set jsonb,
    settings_profile text,
    local_history text,
    retain_finals boolean,
    revision bigint,
    maximum_runtime_seconds bigint,
    maximum_turns bigint,
    maximum_tokens bigint,
    created_at timestamptz,
    PRIMARY KEY (tenant_id, grant_id)
);
ALTER TABLE native_session_consents ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_consents FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_consents_tenant ON native_session_consents
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Facts are copied only from the immutable proposal belonging to this person's
-- approved grant. The trigger also covers non-HTTP approval writers.
CREATE FUNCTION aeon_native_consent_from_grant() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE facts jsonb; person uuid; project uuid;
BEGIN
    IF NEW.scope NOT IN ('harness.control.native.session','harness.control.native.retention') THEN
        RETURN NEW;
    END IF;
    SELECT r.native_consent, d.decided_by_principal_id, r.resource_id
      INTO facts, person, project
      FROM approval_requests r JOIN approval_decisions d
        ON d.tenant_id=r.tenant_id AND d.request_id=r.id
      JOIN principals p ON p.tenant_id=d.tenant_id AND p.id=d.decided_by_principal_id
     WHERE r.tenant_id=NEW.tenant_id AND r.id=NEW.approval_request_id
       AND d.decision='approved' AND p.kind='person' AND p.status='active'
       AND r.resource_kind='node' AND r.run_id IS NULL;
    IF facts IS NULL OR jsonb_typeof(facts) <> 'object' OR (facts->>'revision')::bigint IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'native consent requires person-approved facts' USING ERRCODE='23514';
    END IF;
    INSERT INTO native_session_consents (
        tenant_id,grant_id,owner_person_id,agent_principal_id,project_id,
        computer_id,account_id,workspace_id,workspace_root,model_profile_id,
        tool_set,settings_profile,local_history,retain_finals,revision,
        maximum_runtime_seconds,maximum_turns,maximum_tokens,created_at)
    VALUES (NEW.tenant_id,NEW.approval_request_id,person,NEW.agent_principal_id,project,
        (facts->>'computer_id')::uuid,(facts->>'account_id')::uuid,facts->>'workspace_id',
        facts->>'workspace_root',(facts->>'model_profile_id')::uuid,facts->'tool_set',
        facts->>'settings_profile',facts->>'local_history',(facts->>'retain_finals')::boolean,
        (facts->>'revision')::bigint,(facts->>'maximum_runtime_seconds')::bigint,
        (facts->>'maximum_turns')::bigint,(facts->>'maximum_tokens')::bigint,now());
    RETURN NEW;
END;
$$;
CREATE TRIGGER native_consent_from_grant AFTER INSERT ON agent_permission_grants
    FOR EACH ROW EXECUTE FUNCTION aeon_native_consent_from_grant();

-- Direct inserts cannot turn a denied/missing grant into consent. This includes
-- the trigger's insert; only the immutable request's exact projection is valid.
CREATE FUNCTION aeon_native_consent_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM agent_permission_grants g
        JOIN approval_requests r ON r.tenant_id=g.tenant_id AND r.id=g.approval_request_id
        JOIN approval_decisions d ON d.tenant_id=r.tenant_id AND d.request_id=r.id
        JOIN principals p ON p.tenant_id=d.tenant_id AND p.id=d.decided_by_principal_id
        WHERE g.tenant_id=NEW.tenant_id AND g.approval_request_id=NEW.grant_id
          AND g.scope IN ('harness.control.native.session','harness.control.native.retention')
          AND d.decision='approved' AND p.kind='person' AND p.status='active'
          AND NEW.owner_person_id=d.decided_by_principal_id
          AND NEW.agent_principal_id=g.agent_principal_id AND NEW.project_id=r.resource_id
          AND (to_jsonb(NEW) - ARRAY['tenant_id','grant_id','owner_person_id','agent_principal_id','project_id','created_at'])=r.native_consent
    ) THEN
        RAISE EXCEPTION 'native consent must match a person-approved grant' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER native_consent_guard BEFORE INSERT ON native_session_consents
    FOR EACH ROW EXECUTE FUNCTION aeon_native_consent_guard();
CREATE TRIGGER native_consent_immutable BEFORE UPDATE OR DELETE ON native_session_consents
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();

-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1267: isolated native intent/binding sidecars. Older writers do not
-- write these tables. No credentials, paths, transcripts or vendor IDs.
CREATE TABLE native_session_intents (
    tenant_id uuid,
    id uuid,
    request_id uuid,
    owned_session_id uuid,
    project_id uuid,
    owner_person_id uuid,
    actor_principal_id uuid,
    actor_kind text,
    actor_key_id uuid,
    actor_key_ceiling text[],
    actor_authority jsonb,
    actor_grant_id uuid,
    actor_grant_revision bigint,
    request_digest bytea,
    intent jsonb,
    policy jsonb,
    launch_config jsonb,
    created_at timestamptz,
    claim_request_id uuid,
    claim_digest bytea,
    claim_expires_at timestamptz,
    PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX native_session_intent_request ON native_session_intents(tenant_id,request_id);
CREATE UNIQUE INDEX native_session_intent_session ON native_session_intents(tenant_id,owned_session_id);

CREATE TABLE native_session_bindings (
    tenant_id uuid,
    id uuid,
    project_id uuid,
    owner_person_id uuid,
    actor_principal_id uuid,
    role text,
    harness text,
    account_id uuid,
    computer_id uuid,
    paired_principal_id uuid,
    workspace_id text,
    model_profile_id uuid,
    work_id uuid,
    parent_harness_session_id uuid,
    policy_id uuid,
    run_id uuid,
    current_harness_session_id uuid,
    runtime_epoch uuid,
    daemon_id text,
    daemon_generation text,
    ledger_generation text,
    binding_revision bigint,
    view jsonb,
    terminal_viewer_url text,
    created_at timestamptz,
    PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX native_session_current_harness ON native_session_bindings(tenant_id,current_harness_session_id);
CREATE INDEX native_session_project ON native_session_bindings(tenant_id,project_id,id);

-- Only coordinator roots occupy this sidecar. Workers keep agent_runs and
-- their existing reservations; the run_id IS NULL predicate is intentional.
CREATE TABLE native_session_roots (
    tenant_id uuid,
    id uuid,
    owned_session_id uuid,
    project_id uuid,
    owner_person_id uuid,
    account_id uuid,
    computer_id uuid,
    run_id uuid,
    runtime_epoch uuid,
    hold_state text,
    budget_holds jsonb,
    account_limit_estimates jsonb,
    created_at timestamptz,
    released_at timestamptz,
    PRIMARY KEY (tenant_id,id)
);
CREATE UNIQUE INDEX native_session_root_session ON native_session_roots(tenant_id,owned_session_id);
CREATE INDEX native_session_root_account ON native_session_roots(tenant_id,account_id) WHERE run_id IS NULL AND released_at IS NULL;
CREATE INDEX native_session_root_computer ON native_session_roots(tenant_id,computer_id) WHERE run_id IS NULL AND released_at IS NULL;

ALTER TABLE native_session_intents ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_intents FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_intents_tenant ON native_session_intents
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE native_session_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_bindings_tenant ON native_session_bindings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE native_session_roots ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_session_roots FORCE ROW LEVEL SECURITY;
CREATE POLICY native_session_roots_tenant ON native_session_roots
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE FUNCTION aeon_native_binding_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(NEW) - ARRAY['current_harness_session_id','runtime_epoch','daemon_generation','ledger_generation','binding_revision','view','terminal_viewer_url'])
       IS DISTINCT FROM
       (to_jsonb(OLD) - ARRAY['current_harness_session_id','runtime_epoch','daemon_generation','ledger_generation','binding_revision','view','terminal_viewer_url']) THEN
        RAISE EXCEPTION 'native session binding is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER native_session_binding_immutable BEFORE UPDATE ON native_session_bindings
    FOR EACH ROW EXECUTE FUNCTION aeon_native_binding_immutable();

CREATE FUNCTION aeon_native_intent_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(NEW) - ARRAY['claim_request_id','claim_digest','claim_expires_at']) IS DISTINCT FROM
       (to_jsonb(OLD) - ARRAY['claim_request_id','claim_digest','claim_expires_at']) THEN
        RAISE EXCEPTION 'native session intent is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER native_session_intent_immutable BEFORE UPDATE ON native_session_intents
    FOR EACH ROW EXECUTE FUNCTION aeon_native_intent_immutable();

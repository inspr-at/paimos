-- SPDX-License-Identifier: AGPL-3.0-only
-- Each public request carries an explicit tenant before entering RLS. No global
-- credential directory, person impersonation, or tenantless pairing rows.
ALTER TABLE agent_keys ADD CONSTRAINT agent_keys_tenant_id_unique UNIQUE (tenant_id,id);
CREATE TABLE agent_pairing_requests (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL,
    user_code text NOT NULL CHECK (user_code ~ '^[0-9]{9}$'),
    device_hash text NOT NULL CHECK (device_hash ~ '^[0-9a-f]{64}$'),
    runtime_hash text NOT NULL CHECK (runtime_hash ~ '^[0-9a-f]{64}$'),
    lifecycle_hash text NOT NULL CHECK (lifecycle_hash ~ '^[0-9a-f]{64}$'),
    details jsonb NOT NULL,
    request_digest text NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','denied','expired','redeemed','revoked')),
    verification text CHECK (verification IN ('one_per_harness','connect_only')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes',
    verification_expires_at timestamptz NOT NULL DEFAULT now()+interval '30 minutes',
    approved_by uuid,
    selected_account_keys text[],
    computer_id uuid,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 10),
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,user_code),
    UNIQUE (tenant_id,device_hash),
    FOREIGN KEY (tenant_id,approved_by) REFERENCES principals(tenant_id,id)
);
CREATE TABLE agent_pairing_computers (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL,
    request_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    key_id uuid NOT NULL,
    verification_project_id uuid,
    daemon_id text NOT NULL,
    lifecycle_hash text NOT NULL CHECK (lifecycle_hash ~ '^[0-9a-f]{64}$'),
    state text NOT NULL DEFAULT 'connected' CHECK (state IN ('connected','draining','revoked')),
    local_cleanup text NOT NULL DEFAULT 'pending' CHECK (local_cleanup IN ('pending','confirmed')),
    local_processes text NOT NULL DEFAULT 'unconfirmed' CHECK (local_processes IN ('unconfirmed','drained')),
    setup_state text NOT NULL DEFAULT 'approved' CHECK (setup_state IN ('approved','provisioning','login_required','service_conflict','connected','setup_failed')),
    setup_error text NOT NULL DEFAULT '' CHECK (setup_error IN ('','login_required','service_conflict','unsupported_platform','managed_installation','connectivity_failed','private_storage_failed','installation_failed')),
    last_seen_at timestamptz,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,principal_id),
    UNIQUE (tenant_id,daemon_id),
    FOREIGN KEY (tenant_id,request_id) REFERENCES agent_pairing_requests(tenant_id,id),
    FOREIGN KEY (tenant_id,principal_id) REFERENCES principals(tenant_id,id),
    FOREIGN KEY (tenant_id,key_id) REFERENCES agent_keys(tenant_id,id),
    FOREIGN KEY (tenant_id,verification_project_id) REFERENCES nodes(tenant_id,id)
);
ALTER TABLE agent_pairing_requests ADD FOREIGN KEY (tenant_id,computer_id) REFERENCES agent_pairing_computers(tenant_id,id);
CREATE TABLE agent_pairing_enrollments (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    computer_id uuid NOT NULL,
    request_id uuid NOT NULL,
    model_profile_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'connected' CHECK (state IN ('connected','draining','revoked')),
    local_cleanup text NOT NULL DEFAULT 'pending' CHECK (local_cleanup IN ('pending','confirmed')),
    ongoing_approved_at timestamptz,
    verification_run_id uuid,
    verification_claimed_at timestamptz,
    verification_expires_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,account_id),
    UNIQUE (tenant_id,verification_run_id),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
    FOREIGN KEY (tenant_id,computer_id) REFERENCES agent_pairing_computers(tenant_id,id),
    FOREIGN KEY (tenant_id,request_id) REFERENCES agent_pairing_requests(tenant_id,id),
    FOREIGN KEY (tenant_id,model_profile_id) REFERENCES model_profiles(tenant_id,id),
    FOREIGN KEY (tenant_id,verification_run_id) REFERENCES agent_runs(tenant_id,id)
);
CREATE INDEX agent_pairing_enrollments_computer ON agent_pairing_enrollments(tenant_id,computer_id);
-- Finite per-tenant counters persist across restarts and multiple server workers.
CREATE TABLE agent_pairing_limits (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    bucket text NOT NULL CHECK (bucket IN ('device','lookup','proof')),
    starts_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id,bucket)
);
DO $$
DECLARE n text;
BEGIN
    FOREACH n IN ARRAY ARRAY['agent_pairing_requests','agent_pairing_computers','agent_pairing_enrollments','agent_pairing_limits'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',n);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',n);
        EXECUTE format('CREATE POLICY %I ON %I USING (tenant_id = NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting(''aeon.tenant_id'',true),'''')::uuid)',n||'_tenant',n);
    END LOOP;
END;
$$;
ALTER TABLE account_allowance_windows ADD COLUMN pairing_verification boolean NOT NULL DEFAULT false;
-- A queued cancellation has an end but never had a process start. Preserve that fact.
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_end_has_start CHECK (ended_at IS NULL OR started_at IS NOT NULL OR status='cancelled');
ALTER TABLE agent_runs ADD COLUMN purpose text NOT NULL DEFAULT 'managed' CHECK (purpose IN ('managed','pairing_verification'));
-- The enrollment row is the permanent fence. A manual Resume, registration or
-- metadata update must not detach or resurrect a disconnected enrollment.
CREATE FUNCTION aeon_guard_pairing_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_pairing_enrollments e WHERE e.tenant_id=OLD.tenant_id AND e.account_id=OLD.id) THEN
        IF NEW.registered_by_principal_id IS DISTINCT FROM OLD.registered_by_principal_id OR
           NEW.daemon_id IS DISTINCT FROM OLD.daemon_id OR NEW.harness IS DISTINCT FROM OLD.harness OR
           NEW.account_key IS DISTINCT FROM OLD.account_key OR NEW.max_parallel_runs <> 1 OR
           NEW.allowed_model_profile_ids IS DISTINCT FROM OLD.allowed_model_profile_ids THEN
            RAISE EXCEPTION 'paired account binding is immutable' USING ERRCODE='23514';
        END IF;
        IF NEW.state='available' AND EXISTS (SELECT 1 FROM agent_pairing_enrollments e
            WHERE e.tenant_id=OLD.tenant_id AND e.account_id=OLD.id AND e.state<>'connected') THEN
            RAISE EXCEPTION 'pairing enrollment is disconnected' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER agent_pairing_account_guard BEFORE UPDATE ON agent_accounts FOR EACH ROW EXECUTE FUNCTION aeon_guard_pairing_account();

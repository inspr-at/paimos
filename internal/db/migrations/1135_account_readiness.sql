-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-478 package A: additive storage; existing allowance/reservation history
-- and published CHECK constraints remain unchanged. Rollback disables callers,
-- retaining these facts and receipts for reconciliation.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_accounts ADD COLUMN share_usage boolean NOT NULL DEFAULT false;

CREATE TABLE account_readiness_resources (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('subscription_quota','key_cap','shared_balance','endpoint_concurrency')),
    identity_kind text NOT NULL CHECK (identity_kind IN ('account','verified','person_confirmed','unresolved')),
    identity_key text NOT NULL CHECK (identity_key ~ '^[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,kind,identity_kind,identity_key)
);
-- identity_key is an opaque digest, NEVER a credential, key hash, person ID,
-- local path or unverified assertion of a shared balance. Unresolved resources
-- are tenant/provider/endpoint buckets, shared across people, never summed.
CREATE TABLE account_readiness_memberships (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    resource_id uuid NOT NULL,
    binding_revision bigint NOT NULL CHECK (binding_revision >= 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id,account_id,resource_id),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
    FOREIGN KEY (tenant_id,resource_id) REFERENCES account_readiness_resources(tenant_id,id)
);
CREATE INDEX account_readiness_memberships_resource ON account_readiness_memberships(tenant_id,resource_id,account_id);

CREATE TABLE account_readiness_facts (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    resource_id uuid NOT NULL,
    window_key text NOT NULL CHECK (window_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]*$' AND length(window_key)<=128),
    reported_by_account_id uuid NOT NULL,
    binding_revision bigint NOT NULL CHECK (binding_revision>=0),
    source text NOT NULL CHECK (source IN ('agentd','harness','provider')),
    observed_at timestamptz NOT NULL,
    resets_at timestamptz,
    reading_at timestamptz,
    used_percent double precision CHECK (used_percent>=0 AND used_percent<=100),
    remaining double precision CHECK (remaining>=0 AND remaining<'Infinity'::float8),
    credit_state text NOT NULL DEFAULT 'unknown' CHECK (credit_state IN ('unknown','available','exhausted')),
    reading_error text NOT NULL DEFAULT '' CHECK (reading_error IN ('','unsupported','timeout','protocol','launch_failed','identity_mismatch','authentication_failed')),
    failure_count integer NOT NULL DEFAULT 0 CHECK (failure_count>=0),
    check_next_attempt_at timestamptz,
    stop_kind text NOT NULL DEFAULT 'none' CHECK (stop_kind IN ('none','named_reset','unnamed','money_402')),
    denial_reason text NOT NULL DEFAULT '' CHECK (denial_reason IN ('','vendor_denied','quota_exhausted','key_cap_exhausted','money_exhausted')),
    backoff_step integer NOT NULL DEFAULT 0 CHECK (backoff_step BETWEEN 0 AND 3),
    next_attempt_at timestamptz,
    wait_id uuid,
    early_recovery_used boolean NOT NULL DEFAULT false,
    recovery_run_id uuid,
    recovery_check_id uuid,
    PRIMARY KEY (tenant_id,resource_id,window_key),
    FOREIGN KEY (tenant_id,resource_id) REFERENCES account_readiness_resources(tenant_id,id),
    FOREIGN KEY (tenant_id,reported_by_account_id) REFERENCES agent_accounts(tenant_id,id),
    FOREIGN KEY (tenant_id,recovery_run_id) REFERENCES agent_runs(tenant_id,id),
    CHECK (stop_kind<>'named_reset' OR resets_at IS NOT NULL),
    CHECK (stop_kind NOT IN ('unnamed','money_402') OR (wait_id IS NOT NULL AND next_attempt_at IS NOT NULL)),
    CHECK (stop_kind<>'money_402' OR resets_at IS NULL)
);
CREATE TABLE account_readiness_checks (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    actor_principal_id uuid NOT NULL,
    binding_revision bigint NOT NULL CHECK (binding_revision>=0),
    daemon_generation text CHECK (length(daemon_generation) BETWEEN 1 AND 128),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','completed','invalidated')),
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    result text CHECK (result IN ('success','unsupported','timeout','protocol','launch_failed','identity_mismatch','authentication_failed')),
    early_recovery_requested boolean NOT NULL DEFAULT false,
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,account_id,id,binding_revision),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
    FOREIGN KEY (tenant_id,actor_principal_id) REFERENCES principals(tenant_id,id),
    CHECK ((state='completed') = (completed_at IS NOT NULL AND result IS NOT NULL))
);
CREATE UNIQUE INDEX account_readiness_check_pending ON account_readiness_checks(tenant_id,account_id) WHERE state='pending';
CREATE INDEX account_readiness_check_gap ON account_readiness_checks(tenant_id,account_id,requested_at DESC);
ALTER TABLE account_readiness_facts ADD CONSTRAINT account_readiness_recovery_check_fk FOREIGN KEY (tenant_id,recovery_check_id) REFERENCES account_readiness_checks(tenant_id,id) NOT VALID;
ALTER TABLE account_readiness_facts VALIDATE CONSTRAINT account_readiness_recovery_check_fk;
-- A pending check can have many idempotency keys, all pointing to one capture.
CREATE TABLE account_readiness_check_keys (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]*$' AND length(idempotency_key)<=128),
    binding_revision bigint NOT NULL CHECK (binding_revision>=0),
    check_id uuid NOT NULL,
    actor_principal_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id,account_id,idempotency_key),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
    FOREIGN KEY (tenant_id,account_id,check_id,binding_revision) REFERENCES account_readiness_checks(tenant_id,account_id,id,binding_revision),
    FOREIGN KEY (tenant_id,actor_principal_id) REFERENCES principals(tenant_id,id)
);

CREATE INDEX account_readiness_check_keys_check ON account_readiness_check_keys(tenant_id,check_id);

-- Freeze the waits present when a person requested recovery. A later vendor
-- stop cannot inherit an earlier click. B consumes the facts' canonical wait
-- marker, shared with expiry recovery, never the check request itself.
CREATE TABLE account_readiness_check_waits (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    check_id uuid NOT NULL,
    resource_id uuid NOT NULL,
    window_key text NOT NULL,
    wait_id uuid NOT NULL,
    PRIMARY KEY (tenant_id,check_id,resource_id,window_key),
    FOREIGN KEY (tenant_id,check_id) REFERENCES account_readiness_checks(tenant_id,id),
    FOREIGN KEY (tenant_id,resource_id,window_key) REFERENCES account_readiness_facts(tenant_id,resource_id,window_key)
);
ALTER TABLE account_readiness_check_waits ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_readiness_check_waits FORCE ROW LEVEL SECURITY;
CREATE POLICY account_readiness_check_waits_tenant ON account_readiness_check_waits USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

ALTER TABLE account_readiness_resources ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_readiness_resources FORCE ROW LEVEL SECURITY;
CREATE POLICY account_readiness_resources_tenant ON account_readiness_resources USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE account_readiness_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_readiness_memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY account_readiness_memberships_tenant ON account_readiness_memberships USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE account_readiness_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_readiness_facts FORCE ROW LEVEL SECURITY;
CREATE POLICY account_readiness_facts_tenant ON account_readiness_facts USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE account_readiness_checks ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_readiness_checks FORCE ROW LEVEL SECURITY;
CREATE POLICY account_readiness_checks_tenant ON account_readiness_checks USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE account_readiness_check_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_readiness_check_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY account_readiness_check_keys_tenant ON account_readiness_check_keys USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

CREATE FUNCTION aeon_invalidate_readiness_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.owner_person_id IS DISTINCT FROM OLD.owner_person_id OR NEW.link_revision<>OLD.link_revision THEN
        NEW.share_usage := false;
        UPDATE account_readiness_memberships m SET binding_revision=NEW.link_revision
          FROM account_readiness_resources r WHERE m.tenant_id=NEW.tenant_id AND m.account_id=NEW.id
          AND r.tenant_id=m.tenant_id AND r.id=m.resource_id AND r.identity_kind='account';
    END IF;
    IF NEW.owner_person_id IS DISTINCT FROM OLD.owner_person_id OR NEW.link_revision<>OLD.link_revision
       OR NEW.archived_at IS DISTINCT FROM OLD.archived_at OR NEW.registered_by_principal_id<>OLD.registered_by_principal_id
       OR NEW.last_daemon_generation IS DISTINCT FROM OLD.last_daemon_generation THEN
        UPDATE account_readiness_checks SET state='invalidated' WHERE tenant_id=NEW.tenant_id AND account_id=NEW.id AND state='pending';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER account_readiness_invalidate BEFORE UPDATE OF owner_person_id,link_revision,archived_at,registered_by_principal_id,last_daemon_generation ON agent_accounts FOR EACH ROW EXECUTE FUNCTION aeon_invalidate_readiness_account();
CREATE FUNCTION aeon_invalidate_readiness_enrollment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state<>'connected' OR NEW.ongoing_approved_at IS NULL THEN
        UPDATE account_readiness_checks SET state='invalidated' WHERE tenant_id=NEW.tenant_id AND account_id=NEW.account_id AND state='pending';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER account_readiness_enrollment_invalidate AFTER UPDATE OF state,ongoing_approved_at ON agent_pairing_enrollments FOR EACH ROW EXECUTE FUNCTION aeon_invalidate_readiness_enrollment();
CREATE FUNCTION aeon_invalidate_readiness_computer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state<>'connected' THEN
        UPDATE account_readiness_checks SET state='invalidated' WHERE tenant_id=NEW.tenant_id AND state='pending' AND account_id IN (SELECT account_id FROM agent_pairing_enrollments WHERE tenant_id=NEW.tenant_id AND computer_id=NEW.id);
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER account_readiness_computer_invalidate AFTER UPDATE OF state ON agent_pairing_computers FOR EACH ROW EXECUTE FUNCTION aeon_invalidate_readiness_computer();

-- Local resources require no inferred shared identity. Package B may reconcile
-- person-confirmed pools under the same locks without changing existing holds.
CREATE FUNCTION aeon_seed_account_readiness() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource uuid; resource_kind text;
BEGIN
    resource_kind := CASE WHEN NEW.harness='pi' THEN 'key_cap' ELSE 'subscription_quota' END;
    INSERT INTO account_readiness_resources(tenant_id,kind,identity_kind,identity_key)
      VALUES(NEW.tenant_id,resource_kind,'account',encode(sha256(convert_to('account:'||NEW.id::text||':'||resource_kind,'UTF8')),'hex'))
      RETURNING id INTO resource;
    INSERT INTO account_readiness_memberships(tenant_id,account_id,resource_id,binding_revision) VALUES(NEW.tenant_id,NEW.id,resource,NEW.link_revision);
    RETURN NEW;
END;
$$;
CREATE TRIGGER account_readiness_seed AFTER INSERT ON agent_accounts FOR EACH ROW EXECUTE FUNCTION aeon_seed_account_readiness();

-- Existing accounts acquire their local resource at their next fenced probe.
-- Old binaries continue to use their unchanged account/allowance contracts.

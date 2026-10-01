-- SPDX-License-Identifier: AGPL-3.0-only
-- Person ownership is separate from machine pairing and spending approval.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_accounts ADD COLUMN owner_person_id uuid,
    ADD CONSTRAINT agent_accounts_person_fk FOREIGN KEY (tenant_id,owner_person_id) REFERENCES principals(tenant_id,id);
ALTER TABLE agent_accounts ADD COLUMN linked_at timestamptz;
ALTER TABLE agent_accounts ADD COLUMN link_revision bigint NOT NULL DEFAULT 0 CHECK (link_revision >= 0);

ALTER TABLE agent_accounts ADD CONSTRAINT agent_accounts_person_link_consistency CHECK ((owner_person_id IS NULL) = (linked_at IS NULL)) NOT VALID;
CREATE FUNCTION aeon_guard_account_person() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.owner_person_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM principals WHERE tenant_id=NEW.tenant_id AND id=NEW.owner_person_id AND kind='person' AND status='active'
    ) THEN
        RAISE EXCEPTION 'account owner must be an active person' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER agent_accounts_person_guard BEFORE INSERT OR UPDATE OF owner_person_id ON agent_accounts
    FOR EACH ROW EXECUTE FUNCTION aeon_guard_account_person();

CREATE TABLE account_person_link_requests (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    user_code text NOT NULL CHECK (user_code ~ '^[0-9]{6}$'),
    account_revision bigint NOT NULL CHECK (account_revision >= 0),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','linked','expired','revoked')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '10 minutes',
    person_id uuid,
    result_reported_at timestamptz,
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,user_code),
    FOREIGN KEY (tenant_id,account_id) REFERENCES agent_accounts(tenant_id,id),
    FOREIGN KEY (tenant_id,person_id) REFERENCES principals(tenant_id,id),
    CHECK ((state='linked') = (person_id IS NOT NULL)),
    CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX account_person_link_pending ON account_person_link_requests(tenant_id,account_id) WHERE state='pending';
ALTER TABLE account_person_link_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_person_link_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY account_person_link_tenant ON account_person_link_requests
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

-- Reuse the existing persistent limiter implementation without widening a
-- published pairing CHECK constraint; old binaries continue to use their table.
CREATE TABLE account_person_link_limits (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    bucket text NOT NULL CHECK (bucket IN ('link_offer','link_poll','link_lookup','link_approve')),
    starts_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    attempts integer NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id,bucket)
);
ALTER TABLE account_person_link_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_person_link_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY account_person_link_limits_tenant ON account_person_link_limits
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

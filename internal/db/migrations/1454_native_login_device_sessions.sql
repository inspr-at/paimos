-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1731 expansion: new tables only; existing session and agent writers unchanged.
SET LOCAL lock_timeout = '5s';

CREATE TABLE native_login_transactions (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    tenant_slug text NOT NULL,
    challenge_app text NOT NULL,
    redirect_uri text NOT NULL,
    issuing_origin text NOT NULL,
    state_app text,
    state_hash bytea NOT NULL,
    person_id uuid,
    identity_id uuid REFERENCES identities(id),
    code_hash bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    code_expires_at timestamptz,
    callback_at timestamptz,
    used_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES principals(tenant_id, id)
);
CREATE UNIQUE INDEX native_login_code ON native_login_transactions (tenant_id, code_hash);
CREATE INDEX native_login_expiry ON native_login_transactions (tenant_id, expires_at);
ALTER TABLE native_login_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_login_transactions FORCE ROW LEVEL SECURITY;
CREATE POLICY native_login_transactions_tenant ON native_login_transactions
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- id is the stable family identifier. B1b consumes the access hash/expiry;
-- B1c adds rotation/lifecycle against this same family row.
CREATE TABLE device_sessions (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    person_id uuid NOT NULL,
    device_label text NOT NULL,
    access_token_hash bytea NOT NULL,
    refresh_token_hash bytea NOT NULL,
    access_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES principals(tenant_id, id)
);
CREATE UNIQUE INDEX device_sessions_access ON device_sessions (tenant_id, access_token_hash);
CREATE UNIQUE INDEX device_sessions_refresh ON device_sessions (tenant_id, refresh_token_hash);
CREATE INDEX device_sessions_person ON device_sessions (tenant_id, person_id, created_at, id);
ALTER TABLE device_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE device_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY device_sessions_tenant ON device_sessions
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

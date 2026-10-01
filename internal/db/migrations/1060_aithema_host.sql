-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

CREATE TABLE aithema_host_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    settings jsonb NOT NULL,
    service_credential bytea,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE aithema_host_sessions (
    tenant_id uuid NOT NULL,
    sid uuid NOT NULL,
    issuer text NOT NULL,
    requester uuid NOT NULL,
    event_cursor bigint NOT NULL DEFAULT 0 CHECK (event_cursor >= 0),
    PRIMARY KEY (tenant_id, sid),
    FOREIGN KEY (tenant_id, sid) REFERENCES aithema_sessions(tenant_id, sid) ON DELETE CASCADE
);
CREATE INDEX aithema_host_subject ON aithema_host_sessions(tenant_id, issuer, requester);
CREATE TABLE aithema_deprovisioned_subjects (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    issuer text NOT NULL,
    subject uuid NOT NULL,
    PRIMARY KEY (tenant_id, issuer, subject)
);
CREATE TABLE aithema_callbacks (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id uuid NOT NULL,
    idempotency_key text NOT NULL,
    sid uuid,
    operation text NOT NULL CHECK (operation IN ('create','host-event','suspend','resume','purge','deprovision','revoke')),
    payload jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','delivered','failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 6),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    acknowledgement jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, sid) REFERENCES aithema_sessions(tenant_id, sid) ON DELETE CASCADE
);
CREATE INDEX aithema_callbacks_due ON aithema_callbacks(tenant_id, next_attempt_at) WHERE state = 'pending';

ALTER TABLE aithema_host_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_host_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_host_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
ALTER TABLE aithema_host_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_host_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_host_sessions
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
ALTER TABLE aithema_deprovisioned_subjects ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_deprovisioned_subjects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_deprovisioned_subjects
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
ALTER TABLE aithema_callbacks ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_callbacks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_callbacks
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

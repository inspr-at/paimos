-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1709. Expansion: no change to sessions or older writers.
SET LOCAL lock_timeout = '5s';
CREATE TABLE desk_instance_connections (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    principal_id uuid NOT NULL,
    origin text NOT NULL,
    instance_tag text NOT NULL,
    token_hash bytea NOT NULL,
    state_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '2160 hours'),
    last_used_at timestamptz,
    revoked_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, state_hash),
    FOREIGN KEY (tenant_id, principal_id) REFERENCES principals(tenant_id, id)
);
CREATE INDEX desk_instance_connections_person ON desk_instance_connections (tenant_id, principal_id, expires_at) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX desk_instance_connections_token ON desk_instance_connections (tenant_id, token_hash);
ALTER TABLE desk_instance_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE desk_instance_connections FORCE ROW LEVEL SECURITY;
CREATE POLICY desk_instance_connections_tenant ON desk_instance_connections
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY desk_instance_connections_person ON desk_instance_connections AS RESTRICTIVE
    USING ((principal_id = ANY(aeon_current_principals())
       AND NOT EXISTS (SELECT 1 FROM principals p WHERE p.tenant_id=desk_instance_connections.tenant_id AND p.id=ANY(aeon_current_principals()) AND p.kind='agent'))
       OR aeon_visibility_system());

-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

CREATE TABLE workspace_model_provider (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    settings jsonb NOT NULL,
    credential bytea,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE workspace_model_provider ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_model_provider FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON workspace_model_provider
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Separate from immutable pins and account grants. One bounded latest request
-- per account; old writers need not populate this new table.
CREATE TABLE model_harness_probes (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    binding_revision bigint NOT NULL,
    request_id uuid NOT NULL,
    requested_at timestamptz NOT NULL,
    lease_until timestamptz,
    daemon_generation text,
    completed_at timestamptz,
    state text NOT NULL,
    model_count integer NOT NULL DEFAULT 0,
    report_digest text,
    PRIMARY KEY (tenant_id, account_id)
);
ALTER TABLE model_harness_probes ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_harness_probes FORCE ROW LEVEL SECURITY;
CREATE POLICY model_harness_probes_tenant ON model_harness_probes
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

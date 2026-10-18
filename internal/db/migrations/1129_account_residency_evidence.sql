-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- One current host attestation per account. Prior versions remain in events.
-- Binding snapshots prevent reusing evidence after ownership/provider changes.
CREATE TABLE agent_account_residency_evidence (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    account_id uuid NOT NULL,
    evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    binding jsonb NOT NULL CHECK (jsonb_typeof(binding) = 'object'),
    recorded_by uuid NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, account_id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES agent_accounts(tenant_id, id),
    FOREIGN KEY (tenant_id, recorded_by) REFERENCES principals(tenant_id, id)
);
ALTER TABLE agent_account_residency_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_account_residency_evidence FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_account_residency_evidence_tenant ON agent_account_residency_evidence
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

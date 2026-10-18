-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

-- Latest normalized samples survive inactivity. The binding revision is part
-- of the snapshot; a new owner never inherits a previous binding's readings.
ALTER TABLE agent_accounts ADD COLUMN quota_latest jsonb;

-- One row per account and UTC hour, independent of probe frequency. Only
-- allowlisted percentages, window identities, sources and times enter samples.
CREATE TABLE account_quota_hourly (
 tenant_id uuid NOT NULL REFERENCES tenants(id),
 account_id uuid NOT NULL,
 hour_at timestamptz NOT NULL,
 binding_revision bigint NOT NULL,
 samples jsonb NOT NULL,
 PRIMARY KEY (tenant_id, account_id, hour_at),
 FOREIGN KEY (tenant_id, account_id) REFERENCES agent_accounts(tenant_id, id),
 CHECK (jsonb_typeof(samples) = 'array' AND jsonb_array_length(samples) <= 32)
);
ALTER TABLE account_quota_hourly ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_quota_hourly FORCE ROW LEVEL SECURITY;
CREATE POLICY account_quota_hourly_tenant ON account_quota_hourly
 USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
 WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

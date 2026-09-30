-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';
CREATE TABLE tenant_quota_keys (
 tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
 key bytea NOT NULL CHECK (octet_length(key)=32)
);
ALTER TABLE tenant_quota_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_quota_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_quota_keys_tenant ON tenant_quota_keys
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

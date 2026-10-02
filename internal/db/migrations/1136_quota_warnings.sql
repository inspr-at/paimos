-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-613: resource-scoped receipts retain deduplication after recovery.
SET LOCAL lock_timeout = '5s';
CREATE TABLE quota_warning_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    early_percent integer NOT NULL DEFAULT 10 CHECK (early_percent BETWEEN 1 AND 50),
    urgent_percent integer NOT NULL DEFAULT 3 CHECK (urgent_percent BETWEEN 1 AND 50),
    CHECK (urgent_percent < early_percent)
);
CREATE TABLE account_quota_warnings (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    resource_id uuid NOT NULL,
    window_key text NOT NULL CHECK (length(window_key) BETWEEN 1 AND 128),
    reset_key text NOT NULL CHECK (length(reset_key) BETWEEN 1 AND 64),
    threshold_percent integer NOT NULL CHECK (threshold_percent BETWEEN 1 AND 50),
    severity text NOT NULL CHECK (severity IN ('early','urgent')),
    suppressed boolean NOT NULL DEFAULT false,
    reading_at timestamptz NOT NULL,
    remaining_percent double precision NOT NULL CHECK (remaining_percent BETWEEN 0 AND 100),
    resets_at timestamptz,
    recovered_at timestamptz,
    PRIMARY KEY (tenant_id,id),
    UNIQUE (tenant_id,resource_id,window_key,reset_key,threshold_percent),
    FOREIGN KEY (tenant_id,resource_id) REFERENCES account_readiness_resources(tenant_id,id)
);
CREATE INDEX account_quota_warnings_active ON account_quota_warnings(tenant_id,resource_id,window_key)
    WHERE recovered_at IS NULL AND NOT suppressed;
ALTER TABLE quota_warning_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE quota_warning_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY quota_warning_settings_tenant ON quota_warning_settings
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
ALTER TABLE account_quota_warnings ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_quota_warnings FORCE ROW LEVEL SECURITY;
CREATE POLICY account_quota_warnings_tenant ON account_quota_warnings
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

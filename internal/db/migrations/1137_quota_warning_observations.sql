-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-613: current measured availability and its quota/window watermark are
-- independent of reset/threshold notification deduplication receipts (1136).
-- Retain historical receipts; do not reconstruct healthy readings from them.
SET LOCAL lock_timeout = '5s';
CREATE TABLE account_quota_warning_observations (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    quota_key text NOT NULL CHECK (length(quota_key) BETWEEN 1 AND 128),
    window_key text NOT NULL CHECK (length(window_key) BETWEEN 1 AND 128),
    resource_id uuid NOT NULL,
    reset_key text NOT NULL CHECK (length(reset_key) BETWEEN 1 AND 64),
    reading_at timestamptz NOT NULL,
    -- NULL is a historical recovery watermark whose healthy balance was not
    -- retained by the previous binary. It cannot project a percentage warning.
    remaining_percent double precision CHECK (remaining_percent BETWEEN 0 AND 100),
    resets_at timestamptz,
    PRIMARY KEY (tenant_id,quota_key,window_key),
    FOREIGN KEY (tenant_id,resource_id) REFERENCES account_readiness_resources(tenant_id,id)
);
ALTER TABLE account_quota_warning_observations ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_quota_warning_observations FORCE ROW LEVEL SECURITY;
CREATE POLICY account_quota_warning_observations_tenant ON account_quota_warning_observations
    USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
    WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);

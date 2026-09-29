-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-291. An unmanaged session that stops heartbeating is closed by the
-- server as heartbeat_lost ("Lost contact") after a tenant-configurable number
-- of minutes. A later heartbeat with the same worker proof revives it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE harness_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    heartbeat_lost_minutes integer NOT NULL DEFAULT 15 CHECK (heartbeat_lost_minutes BETWEEN 5 AND 1440),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE harness_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_settings_tenant ON harness_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- The sweeper scans live unmanaged generations by their last sign of life.
CREATE INDEX harness_sessions_unmanaged_live ON harness_sessions (tenant_id, (coalesce(heartbeat_at, created_at)))
    WHERE stopped_at IS NULL AND management = 'unmanaged';

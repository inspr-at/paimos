-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-524: defaults and leaving deadlines are scoped to a person and tenant.
SET LOCAL lock_timeout = '5s';
CREATE TABLE person_pause_settings (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    person_id uuid NOT NULL,
    default_level text NOT NULL DEFAULT 'pause'
        CHECK (default_level IN ('stop_now','pause_quickly','pause','wrap_up')),
    leaving_request_id uuid,
    leaving_at timestamptz,
    leaving_scope jsonb NOT NULL DEFAULT '{"hosts":"all"}',
    PRIMARY KEY (tenant_id, person_id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES principals(tenant_id, id),
    CHECK ((leaving_request_id IS NULL) = (leaving_at IS NULL))
);
ALTER TABLE person_pause_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_pause_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY person_pause_settings_tenant ON person_pause_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY person_pause_settings_owner ON person_pause_settings AS RESTRICTIVE
    USING ((SELECT session_read_marker_owned(tenant_id, person_id)))
    WITH CHECK ((SELECT session_read_marker_owned(tenant_id, person_id)));

-- Optional planning snapshots use the session's existing tenant/owner fences.
ALTER TABLE harness_sessions ADD COLUMN pause_progress jsonb;

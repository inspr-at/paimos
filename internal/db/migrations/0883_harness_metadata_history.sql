-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-222: bounded, tenant-scoped changes to public session metadata.
CREATE TABLE harness_metadata_changes (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id bigint GENERATED ALWAYS AS IDENTITY,
    session_id uuid NOT NULL,
    field text NOT NULL CHECK (field IN ('display_label', 'model', 'reasoning_effort')),
    previous_value text,
    value text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id)
);
CREATE INDEX harness_metadata_changes_recent ON harness_metadata_changes (tenant_id, session_id, id DESC);
ALTER TABLE harness_metadata_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_metadata_changes FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_metadata_changes_tenant ON harness_metadata_changes
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

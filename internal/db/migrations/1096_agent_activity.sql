-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-525: additive workspace policy and bounded, tenant-scoped activity.
SET LOCAL lock_timeout = '5s';

ALTER TABLE harness_settings ADD COLUMN agent_activity_mode text NOT NULL DEFAULT 'agent_summary'
    CHECK (agent_activity_mode IN ('off','tool_activity','agent_summary'));
ALTER TABLE harness_sessions ADD COLUMN doing text;
ALTER TABLE harness_sessions ADD COLUMN doing_at timestamptz;
ALTER TABLE harness_sessions ADD COLUMN tool_activity text;
ALTER TABLE harness_sessions ADD COLUMN tool_activity_at timestamptz;

CREATE TABLE harness_current_activity (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    session_id uuid NOT NULL REFERENCES harness_sessions(id) ON DELETE CASCADE,
    text text NOT NULL CHECK (char_length(text) BETWEEN 1 AND 60),
    source text NOT NULL CHECK (source IN ('agent','auto')),
    at timestamptz NOT NULL
);
ALTER TABLE harness_current_activity ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_current_activity FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_current_activity_tenant ON harness_current_activity
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE INDEX harness_current_activity_session ON harness_current_activity (tenant_id,session_id,id DESC);

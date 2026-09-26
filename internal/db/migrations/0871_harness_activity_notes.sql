-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-192: the current step and a bounded recent history belong to the session.
ALTER TABLE harness_sessions ADD COLUMN activity_note text
    CHECK (activity_note IS NULL OR (char_length(activity_note) BETWEEN 1 AND 120
        AND activity_note = btrim(activity_note) AND activity_note !~ '[[:cntrl:]]'));

CREATE TABLE harness_activity_notes (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    id bigint GENERATED ALWAYS AS IDENTITY,
    session_id uuid NOT NULL,
    note text NOT NULL CHECK (char_length(note) BETWEEN 1 AND 120
        AND note = btrim(note) AND note !~ '[[:cntrl:]]'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id)
);
CREATE INDEX harness_activity_notes_recent ON harness_activity_notes (tenant_id, session_id, id DESC);
ALTER TABLE harness_activity_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE harness_activity_notes FORCE ROW LEVEL SECURITY;
CREATE POLICY harness_activity_notes_tenant ON harness_activity_notes
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

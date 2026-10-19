-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1494: the workspace status note on Projects › Goals. One note per
-- tenant (the workspace), written by agents through the goals write API; the
-- latest write wins. Its history is the event log (goal.status_note_changed).
-- Expansion only: a new table with no rows, so no existing writer is affected.
SET LOCAL lock_timeout = '5s';

CREATE TABLE goal_status_notes (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    body text NOT NULL CHECK (length(btrim(body)) > 0 AND char_length(body) <= 2000),
    set_by uuid NOT NULL,
    set_at timestamptz NOT NULL,
    FOREIGN KEY (tenant_id, set_by) REFERENCES principals(tenant_id, id)
);
ALTER TABLE goal_status_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_status_notes FORCE ROW LEVEL SECURITY;
CREATE POLICY goal_status_notes_tenant ON goal_status_notes
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

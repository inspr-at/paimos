-- SPDX-License-Identifier: AGPL-3.0-only
-- Per-person chat read marker (AEON-276). One row per person per harness
-- session. The watermark is the highest sent_event_id that person has seen.
-- Agents are excluded: a caller whose principal set contains an agent matches
-- nothing, and an empty principal set (service paths) matches nothing.
SET LOCAL lock_timeout = '5s';

CREATE TABLE session_read_markers (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    person_id uuid NOT NULL,
    session_id uuid NOT NULL,
    last_read_message_id uuid NOT NULL,
    last_read_event_id bigint NOT NULL CHECK (last_read_event_id >= 0),
    read_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, person_id, session_id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id),
    FOREIGN KEY (tenant_id, last_read_message_id) REFERENCES inbox_compat_messages(tenant_id, id)
);

ALTER TABLE session_read_markers ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_read_markers FORCE ROW LEVEL SECURITY;

CREATE POLICY session_read_markers_tenant ON session_read_markers
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE POLICY session_read_markers_person ON session_read_markers AS RESTRICTIVE
    USING (
        person_id = ANY ((SELECT aeon_current_principals())::uuid[])
        AND EXISTS (
            SELECT 1 FROM principals p
            WHERE p.tenant_id = session_read_markers.tenant_id
              AND p.id = session_read_markers.person_id
              AND p.kind = 'person')
        AND NOT EXISTS (
            SELECT 1 FROM principals a
            WHERE a.tenant_id = session_read_markers.tenant_id
              AND a.kind = 'agent'
              AND a.id = ANY ((SELECT aeon_current_principals())::uuid[]))
        AND EXISTS (
            SELECT 1 FROM harness_sessions s
            WHERE s.tenant_id = session_read_markers.tenant_id
              AND s.id = session_read_markers.session_id))
    WITH CHECK (
        person_id = ANY ((SELECT aeon_current_principals())::uuid[])
        AND EXISTS (
            SELECT 1 FROM principals p
            WHERE p.tenant_id = session_read_markers.tenant_id
              AND p.id = session_read_markers.person_id
              AND p.kind = 'person')
        AND NOT EXISTS (
            SELECT 1 FROM principals a
            WHERE a.tenant_id = session_read_markers.tenant_id
              AND a.kind = 'agent'
              AND a.id = ANY ((SELECT aeon_current_principals())::uuid[]))
        AND EXISTS (
            SELECT 1 FROM harness_sessions s
            WHERE s.tenant_id = session_read_markers.tenant_id
              AND s.id = session_read_markers.session_id));

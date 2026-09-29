-- SPDX-License-Identifier: AGPL-3.0-only
-- Per-person chat read marker (AEON-276). One row per person per harness
-- session. The watermark is the highest sent_event_id that person has seen.
-- Agents are excluded: a caller whose principal set contains an agent matches
-- nothing on read and write, and an empty principal set matches nothing there.
-- Delete also allows a service path, and a workspace reader once the session
-- row is already gone, so ON DELETE CASCADE can clear every marker.
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
    FOREIGN KEY (tenant_id, session_id) REFERENCES harness_sessions(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, last_read_message_id) REFERENCES inbox_compat_messages(tenant_id, id)
);

ALTER TABLE session_read_markers ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_read_markers FORCE ROW LEVEL SECURITY;

-- Own marker, and only for a person. An agent principal in the caller set matches nothing.
CREATE FUNCTION session_read_marker_owned(p_tenant uuid, p_person uuid) RETURNS boolean
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT p_person = ANY (aeon_current_principals())
        AND EXISTS (
            SELECT 1 FROM principals p
            WHERE p.tenant_id = p_tenant AND p.id = p_person AND p.kind = 'person')
        AND NOT EXISTS (
            SELECT 1 FROM principals a
            WHERE a.tenant_id = p_tenant AND a.kind = 'agent'
              AND a.id = ANY (aeon_current_principals()))
$$;

CREATE FUNCTION session_read_marker_visible(p_tenant uuid, p_person uuid, p_session uuid) RETURNS boolean
LANGUAGE sql STABLE SET search_path = public, pg_temp AS $$
    SELECT session_read_marker_owned(p_tenant, p_person)
        AND EXISTS (
            SELECT 1 FROM harness_sessions s
            WHERE s.tenant_id = p_tenant AND s.id = p_session)
$$;

CREATE POLICY session_read_markers_tenant ON session_read_markers
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- Reads and writes stay on the calling person, and only while the session
-- exists. DELETE is separate: cascade runs after the session row is gone, and
-- must remove every marker of that session, including another person's.
CREATE POLICY session_read_markers_person_select ON session_read_markers AS RESTRICTIVE
    FOR SELECT
    USING ((SELECT session_read_marker_visible(tenant_id, person_id, session_id)));

CREATE POLICY session_read_markers_person_insert ON session_read_markers AS RESTRICTIVE
    FOR INSERT
    WITH CHECK ((SELECT session_read_marker_visible(tenant_id, person_id, session_id)));

CREATE POLICY session_read_markers_person_update ON session_read_markers AS RESTRICTIVE
    FOR UPDATE
    USING ((SELECT session_read_marker_visible(tenant_id, person_id, session_id)))
    WITH CHECK ((SELECT session_read_marker_visible(tenant_id, person_id, session_id)));

CREATE POLICY session_read_markers_person_delete ON session_read_markers AS RESTRICTIVE
    FOR DELETE
    USING (
        (SELECT session_read_marker_owned(tenant_id, person_id))
        OR (SELECT aeon_visibility_system())
        OR (
            (SELECT aeon_visible_all())
            AND NOT EXISTS (
                SELECT 1 FROM harness_sessions s
                WHERE s.tenant_id = session_read_markers.tenant_id
                  AND s.id = session_read_markers.session_id)));

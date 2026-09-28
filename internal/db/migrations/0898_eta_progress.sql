-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-262. Ready and live estimates, percent done, and the tenant report interval.
-- Stale detection and epic roll-up are computed when a ticket or session is read.

ALTER TABLE harness_sessions
    ADD COLUMN eta_ready_at timestamptz,
    ADD COLUMN eta_live_at timestamptz,
    ADD COLUMN progress_pct smallint,
    ADD COLUMN eta_reported_at timestamptz,
    ADD CONSTRAINT harness_sessions_progress_pct CHECK (progress_pct IS NULL OR progress_pct BETWEEN 0 AND 100);

CREATE INDEX harness_sessions_ticket_eta ON harness_sessions (tenant_id, ticket_node_id)
    WHERE stopped_at IS NULL AND ticket_node_id IS NOT NULL;

CREATE TABLE eta_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id),
    interval_minutes integer NOT NULL DEFAULT 10 CHECK (interval_minutes BETWEEN 1 AND 240),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE eta_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE eta_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY eta_settings_tenant ON eta_settings
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

-- One live estimate per ticket, so a coordinator can report a ticket the session is not bound to.
CREATE TABLE ticket_live_eta (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    node_id uuid NOT NULL,
    eta_live_at timestamptz,
    reported_at timestamptz NOT NULL,
    reported_by uuid NOT NULL REFERENCES principals(id),
    PRIMARY KEY (tenant_id, node_id),
    FOREIGN KEY (tenant_id, node_id) REFERENCES nodes(tenant_id, id)
);
ALTER TABLE ticket_live_eta ENABLE ROW LEVEL SECURITY;
ALTER TABLE ticket_live_eta FORCE ROW LEVEL SECURITY;
CREATE POLICY ticket_live_eta_tenant ON ticket_live_eta
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY ticket_live_eta_project_visibility ON ticket_live_eta AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM nodes n WHERE n.tenant_id = ticket_live_eta.tenant_id AND n.id = ticket_live_eta.node_id))
    WITH CHECK ((SELECT aeon_visible_all()) OR EXISTS (
        SELECT 1 FROM nodes n WHERE n.tenant_id = ticket_live_eta.tenant_id AND n.id = ticket_live_eta.node_id));

CREATE FUNCTION aeon_eta_interval() RETURNS interval
LANGUAGE sql STABLE AS $$
    SELECT make_interval(mins => coalesce((SELECT interval_minutes FROM eta_settings), 10));
$$;

-- One row. Epics roll up direct children (nested epics count as one child). A ticket or task
-- with no active session is empty, even when an old report is still stored.
CREATE FUNCTION aeon_node_eta(target uuid, depth int DEFAULT 0)
RETURNS TABLE (
    eta_ready_at timestamptz,
    eta_live_at timestamptz,
    progress_pct integer,
    ready_reported_at timestamptz,
    live_reported_at timestamptz,
    ready_by text,
    live_by text,
    ready_stale boolean,
    live_stale boolean
)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    kind text;
BEGIN
    IF depth >= 16 THEN
        RETURN QUERY SELECT NULL::timestamptz, NULL::timestamptz, NULL::integer, NULL::timestamptz, NULL::timestamptz, NULL::text, NULL::text, false, false;
        RETURN;
    END IF;
    SELECT k.slug INTO kind FROM nodes n
        JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
        WHERE n.id = target AND n.deleted_at IS NULL;
    IF kind = 'epic' THEN
        RETURN QUERY
        WITH kids AS (
            SELECT e.eta_ready_at, e.eta_live_at, e.progress_pct, e.ready_reported_at, e.live_reported_at,
                   e.ready_by, e.live_by, e.ready_stale, e.live_stale
            FROM nodes c
            CROSS JOIN LATERAL aeon_node_eta(c.id, depth + 1) e
            WHERE c.parent_id = target AND c.deleted_at IS NULL
        ),
        ready AS (
            SELECT k.eta_ready_at, k.ready_reported_at, k.ready_by, k.ready_stale
            FROM kids k WHERE k.eta_ready_at IS NOT NULL
            ORDER BY k.eta_ready_at DESC, k.ready_reported_at DESC NULLS LAST
            LIMIT 1
        ),
        live AS (
            SELECT k.eta_live_at, k.live_reported_at, k.live_by, k.live_stale
            FROM kids k WHERE k.eta_live_at IS NOT NULL
            ORDER BY k.eta_live_at DESC, k.live_reported_at DESC NULLS LAST
            LIMIT 1
        )
        SELECT
            (SELECT r.eta_ready_at FROM ready r),
            (SELECT l.eta_live_at FROM live l),
            (SELECT round(avg(k.progress_pct))::integer FROM kids k WHERE k.progress_pct IS NOT NULL),
            (SELECT r.ready_reported_at FROM ready r),
            (SELECT l.live_reported_at FROM live l),
            (SELECT r.ready_by FROM ready r),
            (SELECT l.live_by FROM live l),
            coalesce((SELECT r.ready_stale FROM ready r), false),
            coalesce((SELECT l.live_stale FROM live l), false);
        RETURN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM harness_sessions s WHERE s.ticket_node_id = target AND s.stopped_at IS NULL) THEN
        RETURN QUERY SELECT NULL::timestamptz, NULL::timestamptz, NULL::integer, NULL::timestamptz, NULL::timestamptz, NULL::text, NULL::text, false, false;
        RETURN;
    END IF;
    RETURN QUERY
    SELECT w.eta_ready_at, l.eta_live_at, w.progress_pct::integer, w.eta_reported_at, l.reported_at, w.ready_by, l.live_by,
        coalesce(w.eta_reported_at < now() - aeon_eta_interval() * 2, false),
        coalesce(l.reported_at < now() - aeon_eta_interval() * 2, false)
    FROM (SELECT 1) anchor
    LEFT JOIN LATERAL (
        SELECT s.eta_ready_at, s.progress_pct, s.eta_reported_at, pr.name AS ready_by
        FROM harness_sessions s
        LEFT JOIN principals pr ON pr.tenant_id = s.tenant_id AND pr.id = s.agent_principal_id
        WHERE s.ticket_node_id = target AND s.stopped_at IS NULL AND s.role = 'worker'
          AND (s.eta_ready_at IS NOT NULL OR s.progress_pct IS NOT NULL)
        ORDER BY s.eta_reported_at DESC NULLS LAST
        LIMIT 1
    ) w ON true
    LEFT JOIN LATERAL (
        SELECT t.eta_live_at, t.reported_at, pr.name AS live_by
        FROM ticket_live_eta t
        LEFT JOIN principals pr ON pr.tenant_id = t.tenant_id AND pr.id = t.reported_by
        WHERE t.node_id = target AND t.eta_live_at IS NOT NULL
    ) l ON true;
END;
$$;

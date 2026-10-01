-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-475. Preserve the published ETA function for previous binaries. New
-- readers include finished children without keeping their expired estimates.
SET LOCAL lock_timeout = '5s';

-- One completion projection for ticket reads and recursive epic progress.
-- Only the last unarchived worker can finish a ticket, and an open unarchived
-- session of any role prevents completion. RLS keeps both lookups tenant scoped.
CREATE FUNCTION aeon_node_completion(target uuid)
RETURNS TABLE (stopped_at timestamptz, by text, finished boolean)
LANGUAGE sql STABLE AS $$
    SELECT s.stopped_at, pr.name,
           aeon_session_finished(s.stopped_at, s.stop_reason, s.progress_pct)
    FROM harness_sessions s
    JOIN nodes n ON n.tenant_id = s.tenant_id AND n.id = s.ticket_node_id AND n.project_id = s.project_id
    LEFT JOIN principals pr ON pr.tenant_id = s.tenant_id AND pr.id = s.agent_principal_id
    WHERE s.ticket_node_id = target AND n.deleted_at IS NULL AND s.role = 'worker'
      AND s.stopped_at IS NOT NULL AND s.archived_at IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM harness_sessions o
          WHERE o.tenant_id = s.tenant_id AND o.ticket_node_id = s.ticket_node_id
            AND o.stopped_at IS NULL AND o.archived_at IS NULL
      )
    ORDER BY s.stopped_at DESC, s.id DESC LIMIT 1;
$$;

CREATE FUNCTION aeon_node_eta_progress(target uuid, depth int DEFAULT 0)
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
            SELECT e.eta_ready_at, e.eta_live_at,
                   CASE WHEN coalesce(done.finished, false) THEN 100 ELSE e.progress_pct END AS progress_pct,
                   e.ready_reported_at, e.live_reported_at, e.ready_by, e.live_by, e.ready_stale, e.live_stale
            FROM nodes c
            CROSS JOIN LATERAL aeon_node_eta_progress(c.id, depth + 1) e
            LEFT JOIN LATERAL aeon_node_completion(c.id) done ON true
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
    -- Non-epics retain the existing active-session estimate and stale behavior.
    RETURN QUERY SELECT * FROM aeon_node_eta(target, depth);
END;
$$;

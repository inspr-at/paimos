-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-651. Invoker rights preserve tenant and project RLS. No schema data is
-- rewritten: historical sessions, usage and Decision Desk identities stay put.
SET LOCAL lock_timeout = '5s';

-- A scope contains each node once, including the root's own past spend. UNION
-- terminates even corrupt cycles; there is no silent depth limit. Facts and
-- leaf session reads are shared across overlapping requested roots.
CREATE FUNCTION aeon_work_scope(roots uuid[])
RETURNS TABLE(root uuid, id uuid, kind_slug text, fields jsonb, bucket text, is_leaf boolean, project_id uuid)
LANGUAGE plpgsql STABLE AS $$
DECLARE
 scope_roots uuid[];
 scope_nodes uuid[];
BEGIN
 -- Bound the caller's root array before traversal, including duplicate roots.
 IF cardinality(roots)>4096 THEN
  RAISE EXCEPTION 'work scope root budget exceeded' USING ERRCODE='54000';
 END IF;
 -- LIMIT consumes at most budget+1 recursive pairs, before facts/session reads
 -- or the overlapping-root cross product can materialize unbounded work.
 -- UNION still detects cycles; there is no arbitrary depth cutoff.
 WITH RECURSIVE walk(root,id) AS (
  SELECT n.id,n.id FROM nodes n
  WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
   AND n.id=ANY(roots) AND n.deleted_at IS NULL
  UNION
  SELECT w.root,c.id FROM walk w JOIN nodes c ON c.parent_id=w.id
   AND c.tenant_id=current_setting('aeon.tenant_id')::uuid AND c.deleted_at IS NULL
 ), bounded AS MATERIALIZED (SELECT w.root,w.id FROM walk w LIMIT 50001)
 SELECT array_agg(b.root),array_agg(b.id) INTO scope_roots,scope_nodes FROM bounded b;
 IF cardinality(scope_nodes)>50000 THEN
  RAISE EXCEPTION 'work scope expansion budget exceeded' USING ERRCODE='54000';
 END IF;
 RETURN QUERY
 WITH walk AS MATERIALIZED (
  SELECT p.root,p.id FROM unnest(scope_roots,scope_nodes) AS p(root,id)
 ), work_parents AS MATERIALIZED (
  -- Resolve visible direct work parents once for the bounded scope. A
  -- correlated child-kind join can rescan nodes for each root when a newly
  -- cloned or small tenant has no useful kind statistics.
  SELECT DISTINCT c.parent_id FROM nodes c
  JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id
  WHERE c.tenant_id=current_setting('aeon.tenant_id')::uuid
   AND c.parent_id=ANY(scope_nodes) AND c.deleted_at IS NULL
   AND ck.slug IN ('work','epic','ticket','task')
 ), facts AS MATERIALIZED (
  SELECT n.id,k.slug,n.fields,n.project_id,
   CASE aeon_work_status_category(n.state,k.field_schema)
    WHEN 'delivered' THEN 'done' WHEN 'accepted' THEN 'done' WHEN 'blocked' THEN 'open'
    ELSE aeon_work_status_category(n.state,k.field_schema) END AS bucket,
   k.slug IN ('work','epic','ticket','task') AND NOT EXISTS (
    SELECT 1 FROM work_parents p WHERE p.parent_id=n.id) AS is_leaf
  FROM (SELECT DISTINCT w.id FROM walk w) u CROSS JOIN LATERAL (
   SELECT n.id,n.tenant_id,n.kind_id,n.fields,n.state,n.project_id FROM nodes n WHERE n.id=u.id
    AND n.tenant_id=current_setting('aeon.tenant_id')::uuid OFFSET 0
  ) n CROSS JOIN LATERAL (
   SELECT k.slug,k.field_schema FROM node_kinds k WHERE k.tenant_id=n.tenant_id AND k.id=n.kind_id OFFSET 0
  ) k
 ) SELECT w.root,f.id,f.slug,f.fields,f.bucket,f.is_leaf,f.project_id FROM walk w JOIN facts f ON f.id=w.id;
END;
$$;

-- Direct session evidence only: no kind-dependent recursion, and no report
-- from an archived session or an obsolete project binding.
CREATE FUNCTION aeon_leaf_eta(target uuid)
RETURNS TABLE (eta_ready_at timestamptz,eta_live_at timestamptz,progress_pct integer,
 ready_reported_at timestamptz,live_reported_at timestamptz,ready_by text,live_by text,ready_stale boolean,live_stale boolean)
LANGUAGE sql STABLE AS $$
 SELECT w.eta_ready_at,l.eta_live_at,w.progress_pct::integer,w.eta_reported_at,l.reported_at,w.ready_by,l.live_by,
  coalesce(w.eta_reported_at<now()-aeon_eta_interval()*2,false),
  coalesce(l.reported_at<now()-aeon_eta_interval()*2,false)
 FROM nodes n
 LEFT JOIN LATERAL (
  SELECT s.eta_ready_at,s.progress_pct,s.eta_reported_at,pr.name AS ready_by
  FROM harness_sessions s LEFT JOIN principals pr ON pr.tenant_id=s.tenant_id AND pr.id=s.agent_principal_id
  WHERE s.tenant_id=n.tenant_id AND s.ticket_node_id=n.id AND s.project_id=n.project_id
   AND s.stopped_at IS NULL AND s.archived_at IS NULL AND s.role='worker'
   AND (s.eta_ready_at IS NOT NULL OR s.progress_pct IS NOT NULL)
  ORDER BY s.eta_reported_at DESC NULLS LAST,s.id LIMIT 1
 ) w ON true
 LEFT JOIN LATERAL (
  SELECT t.eta_live_at,t.reported_at,pr.name AS live_by FROM ticket_live_eta t
  LEFT JOIN principals pr ON pr.tenant_id=t.tenant_id AND pr.id=t.reported_by
  WHERE t.tenant_id=n.tenant_id AND t.node_id=n.id AND t.eta_live_at IS NOT NULL
   AND EXISTS(SELECT 1 FROM harness_sessions o WHERE o.tenant_id=n.tenant_id AND o.ticket_node_id=n.id
    AND o.project_id=n.project_id AND o.stopped_at IS NULL AND o.archived_at IS NULL)
 ) l ON true
 WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=target AND n.deleted_at IS NULL;
$$;

CREATE FUNCTION aeon_work_aggregates(roots uuid[])
RETURNS TABLE (
 id uuid, hours numeric, planned_hours numeric, is_parent boolean,
 leaf_count integer, estimated_leaves integer, open_leaves integer,
 progress_pct integer, progress_basis text, ready_leaves integer, live_leaves integer,
 eta_ready_at timestamptz, eta_live_at timestamptz,
 ready_reported_at timestamptz, live_reported_at timestamptz,
 ready_by text, live_by text, ready_stale boolean, live_stale boolean,
 has_working_session boolean, finished boolean, finished_at timestamptz, finished_by text
)
LANGUAGE sql STABLE AS $$
 WITH scope AS MATERIALIZED (SELECT * FROM aeon_work_scope(roots)),
 leaf AS MATERIALIZED (
  SELECT f.*, CASE WHEN jsonb_typeof(f.fields->'estimate_hours')='number' THEN
   CASE WHEN (f.fields->>'estimate_hours')::numeric>0 AND (f.fields->>'estimate_hours')::numeric<=200
    THEN (f.fields->>'estimate_hours')::numeric END END AS hours,
   CASE WHEN f.bucket='done' OR coalesce(d.finished,false) THEN 100 ELSE coalesce(e.progress_pct,0) END AS pct,
   e.*,coalesce(d.finished,false) AS worker_finished,
   EXISTS(SELECT 1 FROM harness_sessions s WHERE s.tenant_id=current_setting('aeon.tenant_id')::uuid
    AND s.project_id=f.project_id
    AND s.ticket_node_id=f.id AND s.phase='working' AND s.stopped_at IS NULL AND s.archived_at IS NULL
    AND s.role IN ('worker','coordinator')) AS working
  FROM (SELECT DISTINCT id,fields,bucket,project_id FROM scope WHERE is_leaf AND bucket NOT IN ('archived','cancelled')) f
  CROSS JOIN LATERAL aeon_leaf_eta(f.id) e
  LEFT JOIN LATERAL aeon_node_completion(f.id) d ON true
 ), totals AS (
  SELECT s.root,sum(l.hours) AS hours,count(l.id)::int AS leaves,count(l.hours)::int AS estimated,
   count(l.id) FILTER (WHERE l.bucket<>'done')::int AS open,
   CASE WHEN count(l.hours)>0 THEN round(sum(l.hours*l.pct)/sum(l.hours))::int
    ELSE round(avg(l.pct))::int END AS pct,
   count(l.eta_ready_at) FILTER (WHERE l.bucket<>'done')::int AS ready,
   count(l.eta_live_at) FILTER (WHERE l.bucket<>'done')::int AS live,
   coalesce(bool_or(l.working),false) AS working
  FROM scope s LEFT JOIN leaf l ON l.id=s.id GROUP BY s.root
 ), ranked AS MATERIALIZED (
  SELECT s.root,l.*,
   row_number() OVER (PARTITION BY s.root ORDER BY l.eta_ready_at DESC NULLS LAST,l.ready_reported_at DESC NULLS LAST,l.id) AS rr,
   row_number() OVER (PARTITION BY s.root ORDER BY l.eta_live_at DESC NULLS LAST,l.live_reported_at DESC NULLS LAST,l.id) AS lr
  FROM scope s JOIN leaf l ON l.id=s.id WHERE l.bucket<>'done'
 ) SELECT t.root,
  CASE WHEN own.is_leaf AND jsonb_typeof(own.fields->'estimate_hours')='number' THEN
   CASE WHEN (own.fields->>'estimate_hours')::numeric>0 AND (own.fields->>'estimate_hours')::numeric<=200
    THEN (own.fields->>'estimate_hours')::numeric END ELSE t.hours END,
  CASE WHEN NOT own.is_leaf AND jsonb_typeof(own.fields->'estimate_hours')='number' THEN
   CASE WHEN (own.fields->>'estimate_hours')::numeric>0 AND (own.fields->>'estimate_hours')::numeric<=200
    THEN (own.fields->>'estimate_hours')::numeric END END,
  NOT own.is_leaf AND own.kind_slug IN ('work','ticket','task','epic'),t.leaves,t.estimated,t.open,
  CASE WHEN own.is_leaf AND own.bucket<>'done' AND NOT coalesce(d.finished,false)
    AND NOT EXISTS(SELECT 1 FROM leaf f WHERE f.id=t.root AND f.progress_pct IS NOT NULL)
    THEN NULL ELSE t.pct END,CASE WHEN t.estimated>0 THEN 'estimate' ELSE 'leaves' END,
  t.ready,t.live,r.eta_ready_at,l.eta_live_at,r.ready_reported_at,l.live_reported_at,
  r.ready_by,l.live_by,coalesce(r.ready_stale,false),coalesce(l.live_stale,false),t.working,
  own.is_leaf AND coalesce(d.finished,false),
  CASE WHEN own.is_leaf AND d.finished THEN d.stopped_at END,
  CASE WHEN own.is_leaf AND d.finished THEN d.by END
 FROM totals t JOIN scope own ON own.root=t.root AND own.id=t.root
 LEFT JOIN ranked r ON r.root=t.root AND r.rr=1
 LEFT JOIN ranked l ON l.root=t.root AND l.lr=1
 LEFT JOIN LATERAL aeon_node_completion(t.root) d ON own.is_leaf;
$$;

-- Preserve the published signature for older binaries without its depth cutoff.
CREATE OR REPLACE FUNCTION aeon_node_eta_progress(target uuid, depth int DEFAULT 0)
RETURNS TABLE (eta_ready_at timestamptz,eta_live_at timestamptz,progress_pct integer,
 ready_reported_at timestamptz,live_reported_at timestamptz,ready_by text,live_by text,ready_stale boolean,live_stale boolean)
LANGUAGE sql STABLE AS $$
 SELECT a.eta_ready_at,a.eta_live_at,a.progress_pct,a.ready_reported_at,a.live_reported_at,
  a.ready_by,a.live_by,a.ready_stale,a.live_stale FROM aeon_work_aggregates(ARRAY[target]) a;
$$;

-- Previous binaries calling the original name get the same bounded scope
-- semantics. Keep the historical optional depth argument; it is no cutoff.
CREATE OR REPLACE FUNCTION aeon_node_eta(target uuid, depth int DEFAULT 0)
RETURNS TABLE (eta_ready_at timestamptz,eta_live_at timestamptz,progress_pct integer,
 ready_reported_at timestamptz,live_reported_at timestamptz,ready_by text,live_by text,ready_stale boolean,live_stale boolean)
LANGUAGE sql STABLE AS $$
 SELECT a.eta_ready_at,a.eta_live_at,a.progress_pct,a.ready_reported_at,a.live_reported_at,
  a.ready_by,a.live_by,a.ready_stale,a.live_stale FROM aeon_work_aggregates(ARRAY[target]) a;
$$;

-- Current work members retain the existing frozen public-note whitelist.
-- Existing snapshots stay immutable; only future captures use the work kind.
CREATE OR REPLACE FUNCTION aeon_release_note_snapshot(p_project uuid, p_release uuid) RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object(
        'schema', 'aeon.release-note-snapshot.v1',
        'tenant_id', r.tenant_id,
        'project_node_id', r.project_node_id,
        'release_node_id', r.release_node_id,
        'version', coalesce(r.version, ''),
        'version_scheme', coalesce(r.version_scheme, ''),
        'release_revision', r.revision,
        'captured_at', statement_timestamp(),
        'membership_source', 'journey_tickets.release_node_id',
        'field_source', 'nodes.fields',
        'frozen', false,
        'tickets', coalesce((SELECT jsonb_agg(jsonb_build_object(
            'id', t.ticket_node_id,
            'key', coalesce(n.key, ''),
            'position', t.walker_position,
            'group', aeon_release_note_group(n.fields),
            'updated_at', n.updated_at,
            'fields', CASE
                WHEN n.id IS NOT NULL AND n.deleted_at IS NULL AND k.slug IN ('work','ticket') THEN (
                    SELECT coalesce(jsonb_object_agg(f.key, f.value), '{}'::jsonb)
                    FROM jsonb_each(n.fields) f
                    WHERE f.key IN ('pill_en', 'pill_de', 'benefit_en', 'benefit_de', 'hide_from_release_notes'))
                WHEN n.id IS NOT NULL THEN jsonb_build_object('hide_from_release_notes', coalesce(n.fields->'hide_from_release_notes', 'false'::jsonb))
                ELSE NULL END,
            'unavailable', CASE
                WHEN n.id IS NULL THEN 'Member is unavailable.'
                WHEN n.deleted_at IS NOT NULL THEN 'Member was deleted before capture.'
                WHEN k.slug IS DISTINCT FROM 'ticket' AND k.slug IS DISTINCT FROM 'work' THEN 'Member is not a ticket.'
                ELSE '' END)
            ORDER BY t.walker_position, t.ticket_node_id)
            FROM journey_tickets t
            LEFT JOIN nodes n ON n.tenant_id = t.tenant_id AND n.id = t.ticket_node_id AND n.project_id = t.project_node_id
            LEFT JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
            WHERE t.tenant_id = r.tenant_id AND t.project_node_id = r.project_node_id AND t.release_node_id = r.release_node_id), '[]'::jsonb))
    FROM journey_releases r
    JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
    JOIN nodes pn ON pn.tenant_id = r.tenant_id AND pn.id = r.project_node_id AND pn.deleted_at IS NULL
    WHERE r.tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid
      AND r.project_node_id = p_project
      AND r.release_node_id = p_release;
$$;

-- Leaf Undo changes aggregate projections even when parent status is unchanged.
-- Admit only this public fact, retaining target visibility and every node-ref
-- check. Unknown automation domains and hidden referenced nodes remain private.
ALTER POLICY events_project_visibility ON events
    USING ((SELECT aeon_visible_all())
        OR (CASE
                WHEN node_id IS NULL THEN
                    (SELECT aeon_visibility_system())
                    OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[])
                ELSE EXISTS (SELECT 1 FROM nodes n WHERE n.tenant_id = events.tenant_id AND n.id = events.node_id)
                    AND (split_part(type, '.', 1) IN ('node', 'nodes', 'comment', 'comments', 'attachment',
                            'attachments', 'relation', 'relations', 'import', 'journey', 'intake', 'requirement',
                            'requirements', 'release', 'releases', 'knowledge', 'view', 'views', 'tag', 'tags',
                            'kind', 'kinds', 'profile', 'recurrence')
                         OR type IN ('status_autopilot.changed', 'status_autopilot.undone', 'status_autopilot.skipped', 'status_autopilot.derived', 'status_autopilot.retained', 'status_autopilot.causal_undo')
                         OR actor_principal_id = ANY ((SELECT aeon_current_principals())::uuid[]))
            END
            AND (cardinality(node_refs) = 0
                 OR NOT EXISTS (SELECT 1 FROM unnest(node_refs) AS ref(id)
                                WHERE ref.id NOT IN (SELECT n.id FROM nodes n)))));

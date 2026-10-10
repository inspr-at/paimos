// SPDX-License-Identifier: AGPL-3.0-only

package eta

// AggregateSQL returns the shared projection for detail, page and sort reads.
// roots is a trusted SQL expression, never request text. The existing invoker
// scope enforces the 4096-root / 50000-pair budgets and terminates cycles.
// Materialized facts are shared across overlapping roots, including completion
// on archived/cancelled roots whose facts must not contribute to parent totals.
// Published SQL functions remain available to older binaries.
func AggregateSQL(roots string) string {
	return `WITH scope AS MATERIALIZED (SELECT * FROM aeon_work_scope(` + roots + `)),
 leaf_nodes AS MATERIALIZED (
  SELECT DISTINCT id,fields,bucket,project_id FROM scope
  WHERE is_leaf AND (bucket NOT IN ('archived','cancelled') OR root=id)
 ), eta_interval AS MATERIALIZED (SELECT aeon_eta_interval() AS duration),
 sessions AS MATERIALIZED (
  SELECT s.id,s.ticket_node_id,s.project_id,s.agent_principal_id,s.role,s.phase,
   s.stopped_at,s.stop_reason,s.progress_pct,s.eta_ready_at,s.eta_reported_at
  FROM harness_sessions s JOIN leaf_nodes f ON f.id=s.ticket_node_id
  WHERE s.tenant_id=current_setting('aeon.tenant_id')::uuid AND s.archived_at IS NULL
 ), open_sessions AS MATERIALIZED (
  -- Any open role, even an obsolete project binding, prevents completion.
  -- Live ETA and the working hint require the current project binding.
  SELECT s.ticket_node_id AS id,
   bool_or(s.project_id=f.project_id) AS live,
   bool_or(s.project_id=f.project_id AND s.phase='working' AND s.role IN ('worker','coordinator')) AS working
  FROM sessions s JOIN leaf_nodes f ON f.id=s.ticket_node_id
  WHERE s.stopped_at IS NULL GROUP BY s.ticket_node_id
 ), latest_worker AS MATERIALIZED (
  SELECT DISTINCT ON (s.ticket_node_id) s.*
  FROM sessions s JOIN leaf_nodes f ON f.id=s.ticket_node_id AND f.project_id=s.project_id
  WHERE s.stopped_at IS NULL AND s.role='worker'
   AND (s.eta_ready_at IS NOT NULL OR s.progress_pct IS NOT NULL)
  ORDER BY s.ticket_node_id,s.eta_reported_at DESC NULLS LAST,s.id
 ), latest_stop AS MATERIALIZED (
  SELECT DISTINCT ON (s.ticket_node_id) s.*
  FROM sessions s JOIN leaf_nodes f ON f.id=s.ticket_node_id AND f.project_id=s.project_id
  WHERE s.stopped_at IS NOT NULL AND s.role='worker'
  ORDER BY s.ticket_node_id,s.stopped_at DESC,s.id DESC
 ), live_eta AS MATERIALIZED (
  SELECT t.node_id,t.eta_live_at,t.reported_at,t.reported_by
  FROM ticket_live_eta t JOIN open_sessions o ON o.id=t.node_id AND o.live
  WHERE t.tenant_id=current_setting('aeon.tenant_id')::uuid AND t.eta_live_at IS NOT NULL
 ), leaf_facts AS MATERIALIZED (
  SELECT f.*, CASE WHEN jsonb_typeof(f.fields->'estimate_hours')='number' THEN
   CASE WHEN (f.fields->>'estimate_hours')::numeric>0 AND (f.fields->>'estimate_hours')::numeric<=200
    THEN (f.fields->>'estimate_hours')::numeric END END AS hours,
   w.progress_pct::integer AS progress_pct,w.eta_ready_at,l.eta_live_at,
   w.eta_reported_at AS ready_reported_at,l.reported_at AS live_reported_at,
   wp.name AS ready_by,lp.name AS live_by,
   coalesce(w.eta_reported_at<now()-i.duration*2,false) AS ready_stale,
   coalesce(l.reported_at<now()-i.duration*2,false) AS live_stale,
   coalesce(o.working,false) AS working,
   coalesce(o.id IS NULL AND aeon_session_finished(d.stopped_at,d.stop_reason,d.progress_pct),false) AS finished,
   d.stopped_at AS finished_at,dp.name AS finished_by
  FROM leaf_nodes f CROSS JOIN eta_interval i
  LEFT JOIN open_sessions o ON o.id=f.id
  LEFT JOIN latest_worker w ON w.ticket_node_id=f.id
  LEFT JOIN latest_stop d ON d.ticket_node_id=f.id
  LEFT JOIN live_eta l ON l.node_id=f.id
  LEFT JOIN principals wp ON wp.tenant_id=current_setting('aeon.tenant_id')::uuid AND wp.id=w.agent_principal_id
  LEFT JOIN principals lp ON lp.tenant_id=current_setting('aeon.tenant_id')::uuid AND lp.id=l.reported_by
  LEFT JOIN principals dp ON dp.tenant_id=current_setting('aeon.tenant_id')::uuid AND dp.id=d.agent_principal_id
 ), leaf AS MATERIALIZED (
  SELECT f.*,CASE WHEN f.bucket='done' OR f.finished THEN 100 ELSE coalesce(f.progress_pct,0) END AS pct
  FROM leaf_facts f WHERE f.bucket NOT IN ('archived','cancelled')
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
 ) SELECT t.root AS id,
  CASE WHEN own.is_leaf AND jsonb_typeof(own.fields->'estimate_hours')='number' THEN
   CASE WHEN (own.fields->>'estimate_hours')::numeric>0 AND (own.fields->>'estimate_hours')::numeric<=200
    THEN (own.fields->>'estimate_hours')::numeric END ELSE t.hours END AS hours,
  CASE WHEN NOT own.is_leaf AND jsonb_typeof(own.fields->'estimate_hours')='number' THEN
   CASE WHEN (own.fields->>'estimate_hours')::numeric>0 AND (own.fields->>'estimate_hours')::numeric<=200
    THEN (own.fields->>'estimate_hours')::numeric END END AS planned_hours,
  NOT own.is_leaf AND own.kind_slug IN ('work','ticket','task','epic') AS is_parent,
  t.leaves AS leaf_count,t.estimated AS estimated_leaves,t.open AS open_leaves,
  CASE WHEN own.is_leaf AND own.bucket<>'done' AND NOT coalesce(d.finished,false)
    AND own_leaf.progress_pct IS NULL
    THEN NULL ELSE t.pct END AS progress_pct,
  CASE WHEN t.estimated>0 THEN 'estimate' ELSE 'leaves' END AS progress_basis,
  t.ready AS ready_leaves,t.live AS live_leaves,r.eta_ready_at,l.eta_live_at,r.ready_reported_at,l.live_reported_at,
  r.ready_by,l.live_by,coalesce(r.ready_stale,false) AS ready_stale,coalesce(l.live_stale,false) AS live_stale,
  t.working AS has_working_session,own.is_leaf AND coalesce(d.finished,false) AS finished,
  CASE WHEN own.is_leaf AND d.finished THEN d.finished_at END AS finished_at,
  CASE WHEN own.is_leaf AND d.finished THEN d.finished_by END AS finished_by
 FROM totals t JOIN scope own ON own.root=t.root AND own.id=t.root
 LEFT JOIN ranked r ON r.root=t.root AND r.rr=1
 LEFT JOIN ranked l ON l.root=t.root AND l.lr=1
 LEFT JOIN leaf own_leaf ON own_leaf.id=t.root
 LEFT JOIN leaf_facts d ON d.id=t.root`
}

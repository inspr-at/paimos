// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Resolve the same live role routes as the cells, including account health.
func prepareModelNameSort(ctx context.Context, tx pgx.Tx, q *listQuery) error {
	rows, err := filteredPlanPlacements(ctx, tx, *q)
	if err != nil {
		return err
	}
	routes, err := resolvePlanRoutes(ctx, tx, rows)
	if err != nil {
		return err
	}
	names := map[string]string{}
	if person := planningViewer(ctx, tx); person != nil {
		names["_viewer"] = *person
	}
	for key, route := range routes {
		if route != nil && route.view != nil {
			names[key] = route.view.FullName()
		}
	}
	q.modelNames, err = json.Marshal(names)
	return err
}

// Actual models lead by total tokens, exactly as loadPlanningModels. Session
// project visibility, configured work states and snapshots all precede paging.
func modelNameSortSQL(namesArg string) (string, string) {
	cte := `, ` + planningStatesCTE() + `, model_sessions AS (
  SELECT t.root,s.id,s.harness,
   coalesce(nullif(pk.model,''),nullif(s.model,''),nullif(s.model_raw,''),usage.model,'') AS model,
   d.model_display,usage.tokens,
   CASE WHEN pk.model IN ('opus','sonnet','haiku','fable') THEN coalesce(d.model_display->>'model_version','') ELSE '' END AS version_key
  FROM (` + planningSubtreeSQL(`SELECT f.id AS root FROM filtered f WHERE f.kind_slug IN ('ticket','task')`) + `) t
  JOIN harness_sessions s ON s.tenant_id=current_setting('aeon.tenant_id')::uuid AND s.ticket_node_id=t.id
   AND ((SELECT aeon_visible_all()) OR s.project_id=ANY((SELECT aeon_visible_projects())::uuid[]))
  LEFT JOIN model_profiles p ON p.tenant_id=s.tenant_id AND p.id=s.model_profile_id
    LEFT JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
  LEFT JOIN LATERAL aeon_session_model_key(p.model,p.effort) pk ON true
  LEFT JOIN LATERAL (
   SELECT min(u.model) AS model,sum(u.input_tokens+u.output_tokens) FILTER (
    WHERE u.input_tokens IS NOT NULL AND u.output_tokens IS NOT NULL AND u.cached_input_tokens IS NOT NULL)::bigint AS tokens
   FROM harness_session_usage u WHERE u.tenant_id=s.tenant_id AND u.session_id=s.id
  ) usage ON true
 ), model_used AS (
  SELECT DISTINCT ON (root) root,
   coalesce((array_agg(nullif(btrim((model_display->>'display_name')||' '||(model_display->>'model_version')), '') ORDER BY id) FILTER (WHERE model_display IS NOT NULL))[1],
    initcap(harness)||' '||CASE WHEN harness='codex' THEN regexp_replace(model,'^gpt-6-','')
     WHEN harness='pi' THEN regexp_replace(model,'^anthropic/claude-','') ELSE model END) AS name
  FROM model_sessions WHERE model<>'' GROUP BY root,harness,model,version_key
  ORDER BY root,sum(coalesce(tokens,0)) DESC,harness,model,version_key
 )`
	join := ` LEFT JOIN LATERAL (
  SELECT lower(coalesce(used.name,
   CASE WHEN snap.id IS NOT NULL THEN coalesce(nullif(btrim((snap.snapshot->'route'->>'display_name')||' '||coalesce(snap.snapshot->'route'->>'model_version','')),''),
     nullif(split_part(snap.snapshot->'route'->>'label',' · ',1),''))
   ELSE ` + namesArg + `::jsonb->>(` + placementKeySQL("pk") + `) END)) COLLATE "C" AS name
  FROM nodes n` + assigneeJoin + `
  CROSS JOIN LATERAL (SELECT ` + planPlacementColumns("("+namesArg+"::jsonb->>'_viewer')") + `) pk
  LEFT JOIN model_used used ON used.root=f.id
  LEFT JOIN LATERAL (SELECT id,snapshot FROM ticket_estimate_snapshots WHERE ticket_node_id=f.id ORDER BY started_at DESC,id DESC LIMIT 1) snap ON true
  WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=f.id AND f.kind_slug IN ('ticket','task')
 ) route ON true`
	return cte, join
}

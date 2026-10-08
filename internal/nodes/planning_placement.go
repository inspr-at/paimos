// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const maxPlanPlacements = 4096

func planningViewer(ctx context.Context, tx pgx.Tx) *string {
	p, _ := tenant.PrincipalFrom(ctx)
	return modelprefs.PrefsPerson(ctx, tx, p)
}

func (r planRow) placementKey() string {
	area := r.area
	if area == "" {
		area = "other"
	}
	return strings.Join([]string{r.project, r.person, area, r.bucket, r.role, r.residency}, "|")
}

// Both pre-paging SQL and page display use these expressions. assigneeJoin
// preserves native and imported assignees; the active-person lookup excludes
// agent and inactive assignees before falling back to the canonical viewer.
func planPlacementColumns(viewer string) string {
	return `coalesce(n.project_id::text,'') AS project,
 coalesce(` + modelprefs.CanonicalPersonSQL("assignee.id") + `::text,` + viewer + `::text,'') AS person,
 coalesce((SELECT wk.slug FROM work_kinds wk WHERE wk.archived_at IS NULL
  AND wk.slug=btrim(coalesce(n.fields->>'area','')) AND wk.slug NOT IN ('review','other')
  AND (wk.project_id IS NULL OR wk.project_id=n.project_id) LIMIT 1),'') AS area,
 CASE WHEN btrim(coalesce(n.fields->>'complexity',''))='L'
  OR btrim(coalesce(n.fields->>'complexity',''))='' AND btrim(coalesce(n.fields->>'route_role',''))='build-hard'
  THEN 'complex' ELSE 'normal' END AS bucket,
 btrim(coalesce(n.fields->>'route_role','')) AS role,
 CASE WHEN btrim(coalesce(n.fields->>'residency','')) IN ('eu','local')
  THEN btrim(n.fields->>'residency') ELSE 'any' END AS residency`
}

func placementKeySQL(alias string) string {
	return `concat_ws('|',` + alias + `.project,` + alias + `.person,coalesce(nullif(` + alias + `.area,''),'other'),` + alias + `.bucket,` + alias + `.role,` + alias + `.residency)`
}

// Collect distinct placements for displayed work rows and their eligible
// priced descendant leaves. Closed rows keep their own route even though they
// do not contribute to ancestors. Never build a view for
// every match; cap resolver work before reading profiles or calibrations.
func filteredPlanPlacements(ctx context.Context, tx pgx.Tx, q listQuery) ([]planRow, error) {
	prefix, args := listFilterSQL(q, false)
	args = append(args, planningViewer(ctx, tx))
	viewer := fmt.Sprintf("$%d", len(args))
	sql := prefix + `, plan_targets AS (
 SELECT id FROM filtered WHERE kind_slug IN ('work','ticket','task','epic')
 UNION
 SELECT DISTINCT id FROM aeon_work_scope(ARRAY(SELECT id FROM filtered WHERE kind_slug IN ('work','ticket','task','epic')))
 WHERE is_leaf AND bucket NOT IN ('cancelled','archived')
 ) SELECT DISTINCT ` + planPlacementColumns(viewer) + `
 FROM plan_targets t JOIN nodes n ON n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=t.id` + assigneeJoin + `
 LIMIT ` + fmt.Sprint(maxPlanPlacements+1)
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []planRow{}
	for rows.Next() {
		var r planRow
		if err := rows.Scan(&r.project, &r.person, &r.area, &r.bucket, &r.role, &r.residency); err != nil {
			return nil, err
		}
		out = append(out, r)
		if len(out) > maxPlanPlacements {
			return nil, badRequest("too many planning placements; narrow the list filters")
		}
	}
	return out, rows.Err()
}

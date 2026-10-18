// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const previousWorkKindCountsSQL = `SELECT id::text,
 (SELECT count(*) FROM nodes n WHERE n.tenant_id=work_kinds.tenant_id AND n.deleted_at IS NULL
 AND n.fields->>'area'=work_kinds.slug AND (work_kinds.project_id IS NULL OR n.project_id=work_kinds.project_id))
FROM work_kinds WHERE (project_id IS NULL OR project_id=$1::uuid) AND ($2 OR archived_at IS NULL)
 AND ($3::uuid IS NULL OR id>$3::uuid) ORDER BY id LIMIT $4`

// Risk: grouped counts leak hidden/foreign projects, lose workspace totals or
// duplicate a project-less group; cursor/archived pages must retain their counts.
// Measure the same fixture before and after the concurrent partial index, with
// the actual page SQL. Plan structure, not elapsed-time thresholds, guards cost.
func TestWorkKindGroupedCountsPreserveVisibilityAndPagination(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "kind-counts", "person", "Admin", []string{"admin"})
	foreign := makePrincipal(t, "foreign-counts", "person", "Other", []string{"admin"})
	projects := []string{editorProject(t, p, "COUNT-1"), editorProject(t, p, "COUNT-2")}
	member := addPrincipal(t, p.TenantID, "person", "Project reader", nil)
	bindEditorRole(t, p, member, "count_reader", projects[0], []string{"models.read", "projects.read", "nodes.read"})
	// Models.read at workspace permits the list, while project visibility stays
	// bound to the project's grant above.
	bindEditorRole(t, p, member, "count_list", "", []string{"models.read"})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_kinds(tenant_id,slug,label,project_id)
			VALUES($1,'local-count','Local one',$2),($1,'local-count','Local two',$3)`, p.TenantID, projects[0], projects[1]); err != nil {
			return err
		}
		// 48 custom areas make the old per-kind scan expensive. Include archived
		// kinds so their nodes continue contributing when that page is requested.
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_kinds(tenant_id,slug,label,archived_at)
			SELECT $1,'count-'||i,'Count '||i,CASE WHEN i=48 THEN now() END FROM generate_series(1,48) i`, p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields,deleted_at)
			SELECT $1,k.id,'DATA-'||i,'Count fixture',CASE WHEN i%3=0 THEN NULL WHEN i%3=1 THEN $2::uuid ELSE $3::uuid END,
			jsonb_build_object('area',CASE WHEN i%10=0 THEN 'backend' WHEN i%10=1 THEN 'local-count' ELSE 'count-'||(i%48+1) END),
			CASE WHEN i%19=0 THEN now() END
			FROM generate_series(1,12000) i CROSS JOIN node_kinds k WHERE k.slug='work'`, p.TenantID, projects[0], projects[1])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, foreign.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields)
			SELECT $1,id,'FOREIGN-1','Foreign count','{"area":"backend"}' FROM node_kinds WHERE slug='work'`, foreign.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var rls, forced, indexed bool
	if err := adminPool.QueryRow(t.Context(), `SELECT relrowsecurity,relforcerowsecurity,
		EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass('nodes_work_kind_area_idx') AND indisvalid)
		FROM pg_class WHERE oid='nodes'::regclass`).Scan(&rls, &forced, &indexed); err != nil || !rls || !forced || !indexed {
		t.Fatalf("count index/RLS: indexed=%v rls=%v forced=%v err=%v", indexed, rls, forced, err)
	}
	if _, err := adminPool.Exec(t.Context(), `ANALYZE nodes; ANALYZE work_kinds`); err != nil {
		t.Fatal(err)
	}
	// Only this isolated fixture's index is removed to capture the old plan.
	if _, err := adminPool.Exec(t.Context(), `DROP INDEX nodes_work_kind_area_idx;
		DELETE FROM schema_migrations WHERE version='1344_work_kind_counts.sql'`); err != nil {
		t.Fatal(err)
	}
	var before, after countPlan
	inRegistry(t, p, func(tx pgx.Tx) error {
		var err error
		before, err = explainKindCounts(t.Context(), tx, previousWorkKindCountsSQL)
		return err
	})
	if err := db.MigrateWithHook(t.Context(), appPool, nil); err != nil {
		t.Fatal(err)
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		var err error
		after, err = explainKindCounts(t.Context(), tx, workKindPageSQL)
		return err
	})
	t.Logf("work-kind counts: before %.3fms/%d node scan loops/%d shared hits; after %.3fms/%d node scan loops/%d shared hits",
		before.Execution, before.NodeLoops, before.Hits, after.Execution, after.NodeLoops, after.Hits)
	if before.NodeLoops <= 1 || after.NodeLoops != 1 || after.Aggregates != 1 {
		t.Fatalf("expected per-kind scans -> one grouped scan: before=%+v after=%+v", before, after)
	}
	for _, actor := range []tenant.Principal{p, member, foreign} {
		for _, project := range []string{"", projects[0], projects[1]} {
			if actor.ID == foreign.ID && project != "" || actor.ID == member.ID && project == projects[1] {
				continue
			}
			for _, archived := range []bool{false, true} {
				cursor := ""
				for page := 0; page < 20; page++ {
					path := fmt.Sprintf("/api/work-kinds?limit=7&include_archived=%t", archived)
					if project != "" {
						path += "&project_id=" + project
					}
					if cursor != "" {
						path += "&cursor=" + cursor
					}
					got := decode[workKindPage](t, &actor, "GET", path, "", 200)
					want := map[string]int64{}
					inRegistry(t, actor, func(tx pgx.Tx) error {
						rows, err := tx.Query(t.Context(), previousWorkKindCountsSQL, optionalUUID(project), archived, optionalUUID(cursor), 7)
						if err != nil {
							return err
						}
						defer rows.Close()
						for rows.Next() {
							var id string
							var count int64
							if err := rows.Scan(&id, &count); err != nil {
								return err
							}
							want[id] = count
						}
						return rows.Err()
					})
					actual := map[string]int64{}
					for _, kind := range got.Items {
						actual[kind.ID] = kind.TicketCount
					}
					if len(actual) != len(got.Items) || !reflect.DeepEqual(actual, want) {
						t.Fatalf("actor=%s project=%s archived=%v cursor=%s: counts=%v want=%v", actor.Name, project, archived, cursor, actual, want)
					}
					if got.NextCursor == nil {
						break
					}
					if *got.NextCursor == cursor || page == 19 {
						t.Fatal("kind cursor did not finish")
					}
					cursor = *got.NextCursor
				}
			}
		}
	}
}

type countPlan struct {
	Execution  float64
	NodeLoops  int
	Aggregates int
	Hits       int
}

func explainKindCounts(ctx context.Context, tx pgx.Tx, query string) (countPlan, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `+query, nil, false, nil, 100).Scan(&raw); err != nil {
		return countPlan{}, err
	}
	type planNode struct {
		Type     string     `json:"Node Type"`
		Relation string     `json:"Relation Name"`
		Loops    int        `json:"Actual Loops"`
		Hits     int        `json:"Shared Hit Blocks"`
		Plans    []planNode `json:"Plans"`
	}
	var plans []struct {
		Plan      planNode
		Execution float64 `json:"Execution Time"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		return countPlan{}, err
	}
	if len(plans) != 1 {
		return countPlan{}, fmt.Errorf("expected one plan, got %d", len(plans))
	}
	out := countPlan{Execution: plans[0].Execution, Hits: plans[0].Plan.Hits}
	var visit func(planNode) bool
	visit = func(node planNode) bool {
		hasNodes := node.Relation == "nodes"
		if node.Relation == "nodes" {
			out.NodeLoops += node.Loops
		}
		for _, child := range node.Plans {
			hasNodes = visit(child) || hasNodes
		}
		if node.Type == "Aggregate" && hasNodes {
			out.Aggregates++
		}
		return hasNodes
	}
	visit(plans[0].Plan)
	return out, nil
}

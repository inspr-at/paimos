// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// The first list after a bulk import must finish with no table statistics.
// This uses the same NOBYPASSRLS owner and principal visibility as requests.
func TestListWithoutStatistics(t *testing.T) {
	p := newPrincipal(t, "no-list-stats")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Large project"}`)
	if _, err := testDB.Admin.Exec(t.Context(), `ALTER TABLE nodes SET (autovacuum_enabled=false)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := testDB.Admin.Exec(ctx, `ALTER TABLE nodes RESET (autovacuum_enabled)`); err != nil {
			t.Error(err)
		}
		if _, err := appPool.Exec(ctx, `ANALYZE nodes`); err != nil {
			t.Error(err)
		}
	})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,fields,state,parent_id,position)
   SELECT $1::uuid,'PERF-'||g,$2::uuid,'Item '||g,
    jsonb_build_object('priority',CASE g%3 WHEN 0 THEN 'high' WHEN 1 THEN 'medium' ELSE 'low' END,
     'tags',CASE g%5 WHEN 0 THEN '[]'::jsonb ELSE jsonb_build_array(jsonb_build_object('name','T'||(g%7)),'bug') END,
     'cost_unit',jsonb_build_object('label','CU '||(g%4))),
    CASE g%4 WHEN 0 THEN 'new' WHEN 1 THEN 'active' WHEN 2 THEN 'qa' ELSE 'done' END,
    $3::uuid,g FROM generate_series(1,6000) AS g`, p.TenantID, ticket.ID, root.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `DELETE FROM pg_statistic WHERE starelid='nodes'::regclass`); err != nil {
		t.Fatal(err)
	}
	member := tenant.Principal{ID: addPrincipalIn(t, p.TenantID, "list-member").ID, TenantID: p.TenantID, Kind: tenant.Person, Name: "list-member", Roles: []string{"member"}}
	dbtest.BindLegacy(t, testDB, p.TenantID, member.ID)
	path := "/api/nodes?within=" + root.ID + "&sort=-assignee,-updated_at&limit=50&tag=bug,!t3&cost_unit=!cu%201&state=!done&facets=state,kind,priority,assignee"
	q, err := parseListQuery(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithPrincipal(t.Context(), member)
	listText, listArgs := listSQL(q, nil)
	facetText, facetArgs := facetSQL(q)
	for _, query := range []struct {
		name string
		sql  string
		args []any
	}{
		{"list", listText, listArgs},
		{"facets", facetText, facetArgs},
	} {
		planCtx := db.WithReadStatementTimeout(ctx, 2*time.Second)
		err = db.InTenant(planCtx, appPool, p.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(planCtx, "EXPLAIN (ANALYZE, BUFFERS) "+query.sql, query.args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				if strings.HasPrefix(line, "Execution Time:") || strings.HasPrefix(line, "  Buffers:") || strings.Contains(line, "Seq Scan on nodes") {
					t.Logf("%s %s", query.name, line)
				}
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("%s without statistics: %v", query.name, err)
		}
	}
	page, err := New(appPool, nil).(*Module).listNodes(db.WithReadStatementTimeout(ctx, 2*time.Second), p.TenantID, q)
	if err != nil || len(page.Items) != 50 || page.Facets["kind"]["ticket"] != 2058 {
		t.Fatalf("list without statistics: %v, items=%d, facets=%v", err, len(page.Items), page.Facets)
	}
	ids := make([]string, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	plan := logPagePlanningPerformancePlan(t, member, ids)
	found, rows, visits := planningSubtreeWork(plan, false)
	t.Logf("page subtree without statistics: rows=%.0f, node visits=%.0f", rows, visits)
	if !found || rows != float64(len(ids)) || visits > float64(2*len(ids)) {
		t.Fatalf("page subtree without statistics: found=%t rows=%.0f visits=%.0f, want %d roots and at most %d node visits", found, rows, visits, len(ids), 2*len(ids))
	}
}

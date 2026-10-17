// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
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
	p, root := useList6000Fixture(t)
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
	if err != nil || len(page.Items) != 50 || page.Facets["kind"]["work"] != 2058 {
		t.Fatalf("list without statistics: %v, items=%d, facets=%v", err, len(page.Items), page.Facets)
	}
}

// A scoped list with no nodes statistics must keep every nodes access on an
// index. Sequential scans are what blow the two-second statement budget after
// a bulk import. Planning only: executing the scan would hide the shape behind
// a timeout.
func TestScopedListWithoutStatisticsUsesChildIndex(t *testing.T) {
	p, root := useList6000Fixture(t)
	if _, err := testDB.Admin.Exec(t.Context(), `DELETE FROM pg_statistic WHERE starelid='nodes'::regclass`); err != nil {
		t.Fatal(err)
	}
	member := tenant.Principal{ID: addPrincipalIn(t, p.TenantID, "list-index-member").ID, TenantID: p.TenantID, Kind: tenant.Person, Name: "list-index-member", Roles: []string{"member"}}
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
		var raw string
		err = db.InTenant(planCtx, appPool, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(planCtx, "EXPLAIN (FORMAT JSON) "+query.sql, query.args...).Scan(&raw)
		})
		if err != nil {
			t.Fatalf("%s plan without statistics: %v", query.name, err)
		}
		seq, indexed := nodesAccessInPlan(t, raw)
		if len(seq) > 0 || indexed == 0 {
			t.Fatalf("%s without statistics: sequential nodes scans %v, indexed nodes accesses %d", query.name, seq, indexed)
		}
	}
}

func nodesAccessInPlan(t *testing.T, raw string) (seq []string, indexed int) {
	t.Helper()
	var explained []any
	if err := json.Unmarshal([]byte(raw), &explained); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			if n["Relation Name"] == "nodes" {
				switch n["Node Type"] {
				case "Seq Scan":
					seq = append(seq, "Seq Scan")
				case "Index Scan", "Index Only Scan", "Bitmap Index Scan", "Bitmap Heap Scan":
					indexed++
				}
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(explained)
	return seq, indexed
}

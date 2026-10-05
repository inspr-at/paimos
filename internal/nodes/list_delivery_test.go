// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Decode the public response independently of the implementation's projection.
type deliveryListPage struct {
	Items []struct {
		ID    string `json:"id"`
		Key   string `json:"key"`
		Order *struct {
			ReleaseID *string `json:"release_id"`
			Rank      *string `json:"rank"`
			Expedite  bool    `json:"expedite"`
		} `json:"delivery_order"`
	} `json:"items"`
	NextCursor *string                      `json:"next_cursor"`
	Facets     map[string]map[string]int    `json:"facets"`
	Labels     map[string]map[string]string `json:"facet_labels"`
}

func TestListDeliveryQueryValidation(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	for _, query := range []string{"sort=order", "sort=-order", "ships_in=" + id + ",none,!" + id, "facets=ships_in,release"} {
		if _, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?"+query, nil)); err != nil {
			t.Errorf("valid %s: %v", query, err)
		}
	}
	for _, query := range []string{"ships_in=not-a-uuid", "ships_in=!", "ships_in="} {
		if _, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?"+query, nil)); err == nil || !strings.Contains(err.Error(), "invalid ships_in") {
			t.Errorf("%s must reject invalid ships_in, got %v", query, err)
		}
	}
	values := make([]string, 101)
	for i := range values {
		values[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
	}
	if _, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?ships_in="+strings.Join(values, ","), nil)); err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("101 placements: %v", err)
	}
	for _, query := range []string{"ships_in=" + strings.TrimSuffix(strings.Repeat(id+",", 101), ","), "ships_in=" + strings.Join(values[:60], ",") + "&ships_in=" + strings.Join(values[60:], ",")} {
		if _, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?"+query, nil)); err == nil || !strings.Contains(err.Error(), "at most 100") {
			t.Errorf("raw bound not enforced: %v", err)
		}
	}

}

func TestListDeliveryOrderFilterFacetAndPaging(t *testing.T) {
	p := newPrincipal(t, "delivery-list")
	// This regression exercises reviewed legacy placement kinds; the current
	// starter catalog contains canonical Work instead.
	for _, slug := range []string{"ticket", "task", "epic"} {
		customKind(t, p, slug, slug)
	}
	kinds := map[string]string{}
	for _, kind := range []string{"project", "release", "ticket", "task", "epic"} {
		kinds[kind] = kindBySlug(t, p, kind).ID
	}
	ids := map[string]string{}
	run := func(fn func(context.Context, pgx.Tx) error) {
		t.Helper()
		ctx := dbtest.Seed(t.Context())
		if err := db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error { return fn(ctx, tx) }); err != nil {
			t.Fatal(err)
		}
	}
	run(func(ctx context.Context, tx pgx.Tx) error {
		node := func(key, kind, parent, priority, state string, age int) error {
			var par any
			if parent != "" {
				par = ids[parent]
			}
			fields, _ := json.Marshal(map[string]any{"priority": priority, "release": "imported-v1"})
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state,fields,created_at,updated_at) VALUES($1,$2,$3,$3,$4,$5,$6::jsonb,'2026-01-01'::timestamptz+$7::int*interval '1 day','2026-02-01') RETURNING id::text`, p.TenantID, kinds[kind], key, par, state, string(fields), age).Scan(&id)
			ids[key] = id
			return err
		}
		for _, key := range []string{"PR-1", "PR-2", "PR-3"} {
			if err := node(key, "project", "", "", "active", 0); err != nil {
				return err
			}
		}
		for _, key := range []string{"PR-1", "PR-2"} {
			if _, err := tx.Exec(ctx, `INSERT INTO project_delivery(tenant_id,project_node_id,adopted_by,next_sequence) VALUES($1,$2,$3,3)`, p.TenantID, ids[key], p.ID); err != nil {
				return err
			}
		}
		for i, key := range []string{"REL-1", "REL-2"} {
			if err := node(key, "release", "PR-1", "", "open", 0); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,sequence,rank) VALUES($1,$2,$3,$4,$5)`, p.TenantID, ids["PR-1"], ids[key], i+1, []string{"B", "D"}[i]); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET title='Same release title' WHERE id=$1`, ids[key]); err != nil {
				return err
			}
		}
		for _, item := range []struct {
			key, kind, project, priority, state, release, rank string
			age                                                int
			expedite                                           bool
		}{
			{"TK-1", "ticket", "PR-1", "low", "open", "REL-1", "Z", 5, false},
			{"TK-2", "ticket", "PR-1", "high", "open", "REL-1", "a", 1, false},
			{"TK-3", "ticket", "PR-1", "low", "open", "REL-2", "V", 3, true},
			{"TK-4", "ticket", "PR-1", "medium", "open", "", "V", 9, false},
			{"TK-5", "ticket", "PR-1", "low", "open", "", "", 0, false},
			{"TK-6", "ticket", "PR-1", "high", "open", "", "", 8, false},
			{"TK-7", "ticket", "PR-1", "low", "done", "REL-1", "V", 4, false},
			{"TK-8", "ticket", "PR-1", "high", "open", "REL-1", "B", 0, false},
			{"TK-9", "ticket", "PR-2", "high", "open", "", "", 0, false},
			{"JK-1", "ticket", "PR-3", "low", "open", "", "", 0, false},
			{"JK-2", "ticket", "PR-3", "high", "open", "", "", 9, false},
			{"EP-1", "epic", "PR-1", "high", "open", "REL-2", "B", 1, false},
			{"TS-1", "task", "PR-1", "high", "open", "REL-2", "C", 1, false},
		} {
			if err := node(item.key, item.kind, item.project, item.priority, item.state, item.age); err != nil {
				return err
			}
			if item.rank != "" {
				var release any
				if item.release != "" {
					release = ids[item.release]
				}
				if _, err := tx.Exec(ctx, `INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by,expedite) VALUES($1,$2,$3,$4,$5,'person',$6,$7)`, p.TenantID, ids[item.project], ids[item.key], release, item.rank, p.ID, item.expedite); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at='2026-02-02' WHERE id=$1`, ids["TK-8"])
		return err
	})
	read := func(t *testing.T, project, query string) deliveryListPage {
		t.Helper()
		status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+ids[project]+"&kind=ticket&"+query, "")
		return decode[deliveryListPage](t, status, body, http.StatusOK)
	}
	keys := func(page deliveryListPage) []string {
		out := []string{}
		for _, item := range page.Items {
			out = append(out, item.Key)
		}
		return out
	}
	expectKeys := func(t *testing.T, page deliveryListPage, want ...string) {
		t.Helper()
		if !slices.Equal(keys(page), want) {
			t.Fatalf("got %v want %v", keys(page), want)
		}
	}
	t.Run("expedite container rank C collation and tail age", func(t *testing.T) {
		page := read(t, "PR-1", "sort=order")
		expectKeys(t, page, "TK-3", "TK-7", "TK-1", "TK-2", "TK-4", "TK-5", "TK-6")
		if page.Items[0].Order == nil || page.Items[0].Order.ReleaseID == nil || *page.Items[0].Order.ReleaseID != ids["REL-2"] || !page.Items[0].Order.Expedite {
			t.Fatalf("missing effective placement: %+v", page.Items[0])
		}
		if page.Items[5].Order == nil || page.Items[5].Order.Rank != nil || page.Items[5].Order.ReleaseID != nil {
			t.Fatalf("tail must have explicit null placement: %+v", page.Items[5])
		}
	})
	t.Run("filter identity exclusions labels and facets before paging", func(t *testing.T) {
		page := read(t, "PR-1", "ships_in="+ids["REL-1"]+"&facets=ships_in,release&sort=order&limit=1")
		expectKeys(t, page, "TK-7")
		if page.Facets["ships_in"][ids["REL-1"]] != 3 || len(page.Facets["ships_in"]) != 1 || page.Facets["release"]["imported-v1"] != 3 || page.Labels["ships_in"][ids["REL-1"]] != "Same release title" {
			t.Fatalf("facet identity/count/label: %+v", page)
		}
		expectKeys(t, read(t, "PR-1", "ships_in=none&sort=order"), "TK-4", "TK-5", "TK-6")
		expectKeys(t, read(t, "PR-1", "ships_in=!none,!"+ids["REL-1"]+"&sort=order"), "TK-3")
		expectKeys(t, read(t, "PR-1", "ships_in="+ids["REL-1"]+","+ids["REL-2"]+"&priority=high&sort=order"), "TK-2")
		status, body := call(t, &p, "GET", "/api/nodes?within="+ids["PR-2"]+"&ships_in="+ids["REL-1"], "")
		if status != http.StatusNotFound || !strings.Contains(string(body), "release scope not found") {
			t.Fatalf("cross-project scope must refuse: %d %s", status, body)
		}
		all := read(t, "PR-1", "facets=ships_in,release&sort=order&limit=1")
		if len(all.Facets["ships_in"]) != 3 || all.Facets["ships_in"]["none"] != 3 || all.Facets["ships_in"][ids["REL-2"]] != 1 {
			t.Fatalf("all memberships: %+v", all.Facets)
		}
	})

	t.Run("filter independent of sort and facet", func(t *testing.T) {
		expectKeys(t, read(t, "PR-1", "ships_in="+ids["REL-2"]+"&sort=key"), "TK-3")
		status, body := call(t, &p, "GET", "/api/nodes?within="+ids["PR-1"]+"&kind=epic,task&ships_in="+ids["REL-2"]+"&sort=order&facets=ships_in", "")
		page := decode[deliveryListPage](t, status, body, http.StatusOK)
		expectKeys(t, page, "EP-1", "TS-1")
		if page.Facets["ships_in"][ids["REL-2"]] != 2 {
			t.Fatal("facet must honor item kinds")
		}
	})
	t.Run("project-scoped reader cannot observe hidden release IDs or labels", func(t *testing.T) {
		guest := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Person, Name: "Guest"}
		if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, p.TenantID).Scan(&guest.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, p.TenantID, guest.ID, ids["PR-2"]); err != nil {
			t.Fatal(err)
		}
		status, body := call(t, &guest, "GET", "/api/nodes?kind=ticket&sort=order&facets=ships_in", "")
		page := decode[deliveryListPage](t, status, body, http.StatusOK)
		expectKeys(t, page, "TK-9")
		if len(page.Facets["ships_in"]) != 1 || page.Facets["ships_in"]["none"] != 1 || len(page.Labels) != 0 || strings.Contains(string(body), ids["REL-1"]) || strings.Contains(string(body), "Same release title") {
			t.Fatalf("hidden membership leaked: %s", body)
		}
		status, body = call(t, &guest, "GET", "/api/nodes?kind=ticket&ships_in="+ids["REL-1"]+"&sort=order&facets=ships_in", "")
		if status != http.StatusNotFound && status != http.StatusForbidden {
			t.Fatalf("hidden scope must refuse: %d %s", status, body)
		}
		if strings.Contains(string(body), ids["REL-1"]) || strings.Contains(string(body), "Same release title") {
			t.Fatal("hidden scope refusal must disclose no identity or label")
		}
	})
	t.Run("cursor retains ordering and is bound to ships_in", func(t *testing.T) {
		page := read(t, "PR-1", "ships_in=!none&sort=order&limit=2")
		expectKeys(t, page, "TK-3", "TK-7")
		if page.NextCursor == nil {
			t.Fatal("missing cursor")
		}
		expectKeys(t, read(t, "PR-1", "ships_in=!none&sort=order&limit=2&cursor="+url.QueryEscape(*page.NextCursor)), "TK-1", "TK-2")
		status, body := call(t, &p, "GET", "/api/nodes?within="+ids["PR-1"]+"&kind=ticket&ships_in=none&sort=order&limit=2&cursor="+url.QueryEscape(*page.NextCursor), "")
		if status != http.StatusBadRequest || !strings.Contains(string(body), "cursor") {
			t.Fatalf("changed filter must reject cursor: %d %s", status, body)
		}
		other := p
		if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other scoped reader') RETURNING id::text`, p.TenantID).Scan(&other.ID); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, testDB, p.TenantID, other.ID, "owner")
		status, body = call(t, &other, "GET", "/api/nodes?within="+ids["PR-1"]+"&kind=ticket&ships_in=!none&sort=order&limit=2&cursor="+url.QueryEscape(*page.NextCursor), "")
		if status != http.StatusBadRequest || !strings.Contains(string(body), "cursor does not match") {
			t.Fatalf("changed authorized principal must reject cursor: %d %s", status, body)
		}
	})
	t.Run("journey mode retains priority and has no placement", func(t *testing.T) {
		page := read(t, "PR-3", "sort=order&facets=ships_in,release")
		expectKeys(t, page, "JK-2", "JK-1")
		if len(page.Facets["ships_in"]) != 0 || page.Items[0].Order != nil || page.Facets["release"]["imported-v1"] != 2 {
			t.Fatalf("journey projection: %+v", page)
		}
		expectKeys(t, read(t, "PR-3", "ships_in=none&sort=order"))
	})
	t.Run("completed expedite flag is ignored", func(t *testing.T) {
		run(func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE ships_in SET expedite=false WHERE item_node_id=$1`, ids["TK-3"]); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, ids["TK-7"])
			return err
		})
		page := read(t, "PR-1", "sort=order")
		expectKeys(t, page, "TK-7", "TK-1", "TK-2", "TK-3", "TK-4", "TK-5", "TK-6")
		if page.Items[0].Order == nil || page.Items[0].Order.Expedite {
			t.Fatal("completed flag must be suppressed")
		}
	})
}

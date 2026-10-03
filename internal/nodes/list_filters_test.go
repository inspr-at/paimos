// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// The U22 list filters: exclusions, tags, cost units, releases, epics, dates,
// body search, the assignee sort and the new facets.
func TestListFiltersExclusionsLabelsEpicsAndDates(t *testing.T) {
	p := newPrincipal(t, "filters")
	mira := addPrincipalIn(t, p.TenantID, "mira")
	project, epic, ticket, task := kindBySlug(t, p, "project"), kindBySlug(t, p, "epic"), kindBySlug(t, p, "ticket"), kindBySlug(t, p, "task")
	create := func(kind, key, title, state, parent string, fields map[string]any, body string) nodeJSON {
		t.Helper()
		if fields == nil {
			fields = map[string]any{}
		}
		if kind == ticket.ID && state == "done" {
			var benefits map[string]any
			_ = json.Unmarshal([]byte(benefitFields), &benefits)
			for key, value := range benefits {
				fields[key] = value
			}
		}
		in := map[string]any{"kind_id": kind, "key": key, "title": title, "state": state, "fields": fields, "body": body}
		if parent != "" {
			in["parent_id"] = parent
		}
		raw, _ := json.Marshal(in)
		return mustNode(t, p, string(raw))
	}
	root := create(project.ID, "PRJ-1", "Main", "active", "", nil, "")
	other := create(project.ID, "PRJ-2", "Other", "active", "", nil, "")
	e1 := create(epic.ID, "PAI-1", "Provisioning", "backlog", root.ID, nil, "")
	e2 := create(epic.ID, "PAI-2", "Billing", "backlog", root.ID, nil, "")
	a := create(ticket.ID, "PAI-3", "Alpha", "new", e1.ID, map[string]any{"priority": "high", "assignee": p.ID,
		"tags": []any{map[string]any{"id": 1, "name": "BUG", "color": "red"}, "hsb8"}, "cost_unit": map[string]any{"id": "x", "label": "Consulting"},
		"release": map[string]any{"id": 9, "label": "v4.7.8"}, "start_date": "2026-09-10"}, "")
	b := create(ticket.ID, "PAI-4", "Beta", "in_progress", e2.ID, map[string]any{"priority": "low", "assignee": mira.ID,
		"tags": []any{"bug"}, "classic": map[string]any{"cost_unit": map[string]any{"id": 4, "label": "Support"}}, "start_date": "2026-02-30"}, "The fleet agent restarts twice.")
	c := create(task.ID, "PAI-5", "Gamma", "qa", a.ID, map[string]any{"cost_unit": nil, "classic": map[string]any{"cost_unit": map[string]any{"label": "Support"}}}, "")
	d := create(ticket.ID, "PAI-6", "Delta", "done", root.ID, map[string]any{"priority": "medium", "tags": []any{}}, "")
	_ = create(ticket.ID, "OTH-1", "Elsewhere", "new", other.ID, map[string]any{"tags": []any{"bug"}}, "")

	keys := func(query string) []string {
		t.Helper()
		if !strings.Contains(query, "sort=") {
			query += "&sort=key"
		}
		status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&kind=ticket,task&"+query, "")
		page := decode[nodePage](t, status, body, http.StatusOK)
		out := []string{}
		for _, item := range page.Items {
			out = append(out, item.Key)
		}
		return out
	}
	expect := func(query string, want ...string) {
		t.Helper()
		if want == nil {
			want = []string{}
		}
		if got := keys(query); !slices.Equal(got, want) {
			t.Fatalf("%s: got %v, want %v", query, got, want)
		}
	}
	_ = b
	_ = c
	_ = d

	// Exclusions: every excluded value must not match; plain values stay alternatives.
	expect("state=!done", "PAI-3", "PAI-4", "PAI-5")
	expect("state=new,qa,!qa", "PAI-3")
	expect("priority=!none", "PAI-3", "PAI-4", "PAI-6")
	expect("priority=!high,!low", "PAI-5", "PAI-6")
	expect("assignee=!"+p.ID, "PAI-4", "PAI-5", "PAI-6")
	expect("assignee=!none", "PAI-3", "PAI-4")

	// Tags by name, case-insensitive, string or object elements; none and not.
	expect("tag=bug", "PAI-3", "PAI-4")
	expect("tag=HSB8", "PAI-3")
	expect("tag=none", "PAI-5", "PAI-6")
	expect("tag=bug,!hsb8", "PAI-4")
	expect("tag=!none", "PAI-3", "PAI-4")

	// Cost units: native wins (also an explicit null), classic falls back.
	expect("cost_unit=consulting", "PAI-3")
	expect("cost_unit=support", "PAI-4")
	expect("cost_unit=none", "PAI-5", "PAI-6")
	expect("cost_unit=!support,!none", "PAI-3")
	expect("release=v4.7.8", "PAI-3")
	expect("release=none", "PAI-4", "PAI-5", "PAI-6")

	// Epics: the whole subtree (a task under a ticket too), none, and not.
	expect("epic="+e1.ID, "PAI-3", "PAI-5")
	expect("epic="+e1.ID+","+e2.ID, "PAI-3", "PAI-4", "PAI-5")
	expect("epic=none", "PAI-6")
	expect("epic=!"+e1.ID, "PAI-4", "PAI-6")
	expect("epic=!none", "PAI-3", "PAI-4", "PAI-5")

	// Dates: created and updated by instant; imported field dates by the caller's day.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	expect("date_field=created&date_from="+url.QueryEscape(past)+"&date_to="+url.QueryEscape(future), "PAI-3", "PAI-4", "PAI-5", "PAI-6")
	expect("date_field=updated&date_from=" + url.QueryEscape(future))
	expect("date_field=start&date_from="+url.QueryEscape("2026-09-10T00:00:00+02:00")+"&date_to="+url.QueryEscape("2026-09-11T00:00:00+02:00"), "PAI-3")
	expect("date_field=start", "PAI-3") // malformed dates never match and never fail
	expect("date_field=start&date_to=" + url.QueryEscape("2026-09-10T00:00:00+02:00"))

	// Text also finds the description; key and title matches rank first.
	expect("q=fleet", "PAI-4")

	for _, bad := range []string{"state=!", "tag=!", "epic=nope", "assignee=!x", "date_field=due", "date_from=2026-09-01T00:00:00Z",
		"date_field=created&date_from=yesterday", "date_field=created&date_from=2026-09-02T00:00:00Z&date_to=2026-09-01T00:00:00Z"} {
		if status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&"+bad, ""); status != http.StatusBadRequest {
			t.Fatalf("%s: status %d %s", bad, status, body)
		}
	}

	// Sort by assignee name, unassigned last in both directions.
	sorted := func(sort string) []string {
		t.Helper()
		return keys("sort=" + sort)
	}
	if got := sorted("assignee,key"); got[0] != "PAI-3" || got[1] != "PAI-4" {
		t.Fatalf("assignee asc: %v", got)
	}
	if got := sorted("-assignee,key"); got[0] != "PAI-4" || got[1] != "PAI-3" || !slices.Contains(got[2:], "PAI-5") {
		t.Fatalf("assignee desc: %v", got)
	}

	// Facets for tags, cost units and releases; a cursor binds the new filters.
	status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&kind=ticket,task&facets=tag,cost_unit,release,state&limit=1&tag=!none", "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if page.Facets["tag"]["BUG"]+page.Facets["tag"]["bug"] != 2 || page.Facets["tag"]["hsb8"] != 1 || page.Facets["cost_unit"]["Consulting"] != 1 || page.Facets["cost_unit"]["Support"] != 1 || page.Facets["release"]["none"] != 1 {
		t.Fatalf("facets: %#v", page.Facets)
	}
	if page.NextCursor == nil {
		t.Fatal("expected a second page")
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&kind=ticket,task&facets=tag,cost_unit,release,state&limit=1&tag=none&cursor="+url.QueryEscape(*page.NextCursor), "")
	if status != http.StatusBadRequest || !strings.Contains(string(body), "cursor") {
		t.Fatalf("cursor across filters: %d %s", status, body)
	}
}

// The 6000-node list stays fast with the new facets and filters in use.
func TestList6000FiltersPerformance(t *testing.T) {
	testList6000FiltersPerformance(t)
}

func TestList6000FiltersWithStaleKindStatistics(t *testing.T) {
	// TRUNCATE resets relation estimates but retains column statistics from
	// the previous tenant, just as it can between full-suite fixtures after
	// autoanalyze. ANALYZE nodes alone does not refresh these join estimates.
	newPrincipal(t, "previous-kind-statistics")
	if _, err := appPool.Exec(t.Context(), `ANALYZE node_kinds`); err != nil {
		t.Fatal(err)
	}
	p, path := testList6000FiltersPerformance(t)
	plans := logListPerformancePlans(t, p, path)
	for _, name := range []string{"list", "facets"} {
		visits := filteredNodeVisits(plans[name], false)
		t.Logf("%s filtered node visits: %.0f", name, visits)
		// Bound actual work, without prescribing an index or join algorithm.
		// A kind join must not repeat the node lookup/filter for every kind.
		if visits <= 0 || visits > 6002 {
			t.Errorf("%s filtered node visits = %.0f, want one pass over 6001 nodes", name, visits)
		}
	}
	// The response also loads planning for just this page. Check the actual
	// subtree work, so an accidental scan of all 6000 tickets per selected
	// parent fails even on a machine fast enough to meet the latency budget.
	status, body := call(t, &p, http.MethodGet, path, "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if len(page.Items) != 50 || page.Facets["kind"]["ticket"] != 2058 {
		t.Fatal("stale-statistics fixture lost its filtered tickets or selected page")
	}
	ids := make([]string, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	plan := logPagePlanningPerformancePlan(t, p, ids)
	found, rows, visits := planningSubtreeWork(plan, false)
	t.Logf("page subtree rows=%.0f, node visits=%.0f", rows, visits)
	if !found || rows != float64(len(ids)) || visits > float64(2*len(ids)) {
		t.Fatalf("page subtree work: found=%t rows=%.0f visits=%.0f, want %d roots and at most %d node visits", found, rows, visits, len(ids), 2*len(ids))
	}
}

func testList6000FiltersPerformance(t *testing.T) (tenant.Principal, string) {
	t.Helper()
	p := newPrincipal(t, "large-filters")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Large project"}`)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,fields,state,parent_id,position)
            SELECT $1::uuid,'PERF-'||g,$2::uuid,'Item '||g,
                jsonb_build_object('priority',CASE g%3 WHEN 0 THEN 'high' WHEN 1 THEN 'medium' ELSE 'low' END,
                    'tags',CASE g%5 WHEN 0 THEN '[]'::jsonb ELSE jsonb_build_array(jsonb_build_object('name','T'||(g%7)),'bug') END,
                    'cost_unit',jsonb_build_object('label','CU '||(g%4))),
                CASE g%4 WHEN 0 THEN 'new' WHEN 1 THEN 'active' WHEN 2 THEN 'qa' ELSE 'done' END,
                $3::uuid,g FROM generate_series(1,6000) AS g`, p.TenantID, ticket.ID, root.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes`); err != nil {
		t.Fatal(err)
	}
	// Materializing the filtered set avoids catastrophic join reordering when
	// statistics are stale. Allow modest local runner variance for that fence.
	limit := 300 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 600 * time.Millisecond
	}
	paths := []string{
		// The same page shape and budget as TestList6000Performance, with the new
		// filters and the assignee sort, then the on-demand label counts.
		"/api/nodes?within=" + root.ID + "&sort=-assignee,-updated_at&limit=50&tag=bug,!t3&cost_unit=!cu%201&state=!done&facets=state,kind,priority,assignee",
		"/api/nodes?within=" + root.ID + "&sort=state,-updated_at&limit=1&facets=tag",
		"/api/nodes?within=" + root.ID + "&sort=state,-updated_at&limit=1&facets=cost_unit",
		"/api/nodes?within=" + root.ID + "&sort=state,-updated_at&limit=50&epic=none&date_field=created&date_from=2020-01-01T00:00:00Z&facets=state,kind,priority,assignee",
	}
	for _, path := range paths {
		var fastest time.Duration
		for i := 0; i < 3; i++ {
			start := time.Now()
			status, body := call(t, &p, http.MethodGet, path, "")
			elapsed := time.Since(start)
			page := decode[nodePage](t, status, body, http.StatusOK)
			if (len(page.Items) != 50 && len(page.Items) != 1) || page.NextCursor == nil {
				t.Fatalf("large list result: %d", len(page.Items))
			}
			if path == paths[0] && page.Facets["kind"]["ticket"] != 2058 {
				t.Fatalf("filtered ticket facet = %d, want 2058", page.Facets["kind"]["ticket"])
			}
			for _, item := range page.Items {
				if item.ChildrenCount != 0 {
					t.Fatalf("unexpected children for %s: %d", item.Key, item.ChildrenCount)
				}
			}
			if fastest == 0 || elapsed < fastest {
				fastest = elapsed
			}
		}
		t.Logf("6000-node list %s: fastest of 3 = %s", path[strings.Index(path, "&"):], fastest)
		if fastest >= limit {
			logListPerformancePlans(t, p, path)
			t.Fatalf("6000-node filtered list exceeded %s: %s", limit, fastest)
		}
	}
	return p, paths[0]
}

func addPrincipalIn(t *testing.T, tenantID, name string) struct{ ID string } {
	t.Helper()
	var id string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals (tenant_id, kind, name, roles) VALUES ($1, 'person', $2, $3) RETURNING id::text`, tenantID, name, []string{"member"}).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return struct{ ID string }{id}
}

// Imported tickets carry large fields that Postgres keeps out of line. The
// assignee projection reads its references once per row, so counting people
// over a project of such tickets stays fast (AEON-140: 0.5 s before on the
// production copy for about 900 rows).
func TestListAssigneeFacetOverLargeImportedFields(t *testing.T) {
	p := newPrincipal(t, "large-fields")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Imported project"}`)
	// A workspace of people, most of them linked to classic accounts.
	for i := 0; i < 14; i++ {
		var identity string
		if err := adminPool.QueryRow(t.Context(), `INSERT INTO identities (issuer, subject) VALUES ('paimos-classic', 'ppm-large:'||$1::int) RETURNING id::text`, i).Scan(&identity); err != nil {
			t.Fatal(err)
		}
		if _, err := adminPool.Exec(t.Context(), `INSERT INTO principals (tenant_id, kind, name, identity_id) VALUES ($1, 'person', 'person '||$2::int, $3)`, p.TenantID, i, identity); err != nil {
			t.Fatal(err)
		}
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,fields,state,parent_id,position)
            SELECT $1::uuid,'IMP-'||g,$2::uuid,'Imported '||g,
                jsonb_build_object('priority','medium','classic',jsonb_build_object('source_id','ppm-large','assignee_id',100+g%5,
                    'description',(SELECT string_agg(md5(g::text||':'||i),' ') FROM generate_series(1,200) i))),
                CASE g%4 WHEN 3 THEN 'done' ELSE 'backlog' END,$3::uuid,g FROM generate_series(1,1500) AS g`, p.TenantID, ticket.ID, root.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes`); err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes?within=" + root.ID + "&kind=ticket&facets=assignee,tag&limit=1"
	var fastest time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		status, body := call(t, &p, http.MethodGet, path, "")
		elapsed := time.Since(start)
		page := decode[nodePage](t, status, body, http.StatusOK)
		if page.Facets["assignee"]["none"] != 1500 {
			t.Fatalf("assignee facet: %#v", page.Facets["assignee"])
		}
		if fastest == 0 || elapsed < fastest {
			fastest = elapsed
		}
	}
	t.Logf("1500 imported rows, assignee and tag facets: fastest of 3 = %s", fastest)
	limit := 150 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 600 * time.Millisecond
	}
	if fastest >= limit {
		t.Fatalf("facets over large fields exceeded %s: %s", limit, fastest)
	}
}

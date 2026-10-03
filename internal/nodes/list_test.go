// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPolishedNodeList(t *testing.T) {
	p := newPrincipal(t, "polished")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	create := func(kind, key, title, state, parent string, fields map[string]any) nodeJSON {
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
		body := map[string]any{"kind_id": kind, "key": key, "title": title, "state": state, "fields": fields}
		if parent != "" {
			body["parent_id"] = parent
		}
		raw, _ := json.Marshal(body)
		return mustNode(t, p, string(raw))
	}
	root := create(project.ID, "PRJ-1", "Main", "active", "", nil)
	archived := create(project.ID, "PRJ-2", "Old", "archived", "", nil)
	a := create(ticket.ID, "PAI-2", "Alpha", "new", root.ID, map[string]any{"priority": "high", "assignee": p.ID})
	b := create(ticket.ID, "PAI-10", "Beta", "done", root.ID, map[string]any{"priority": "low"})
	c := create(ticket.ID, "PAI-3", "Gamma", "qa", a.ID, map[string]any{"priority": "medium"})
	_ = create(ticket.ID, "PAI-20", "Archived child", "archived", archived.ID, nil)
	get := func(path string) nodePage {
		t.Helper()
		status, body := call(t, &p, http.MethodGet, path, "")
		return decode[nodePage](t, status, body, http.StatusOK)
	}
	within := get("/api/nodes?within=" + root.ID + "&sort=key&facets=state,kind,priority,assignee")
	if len(within.Items) != 3 || within.Items[0].ID != a.ID || within.Items[1].ID != c.ID || within.Items[2].ID != b.ID {
		t.Fatalf("natural key order/within: %#v", within.Items)
	}
	if within.Facets["state"]["new"] != 1 || within.Facets["kind"]["ticket"] != 3 || within.Facets["priority"]["high"] != 1 || within.Facets["assignee"][p.ID] != 1 || within.Facets["assignee"]["none"] != 2 {
		t.Fatalf("facets: %#v", within.Facets)
	}
	row := within.Items[0]
	if row.KindSlug != "ticket" || row.KindLabel != "Ticket" || row.Priority == nil || *row.Priority != "high" || row.Assignee == nil || row.Assignee.ID != p.ID || row.Parent == nil || row.Parent.ID != root.ID || row.ChildrenCount != 1 || row.Project == nil || row.Project.ID != root.ID {
		t.Fatalf("projection: %#v", row)
	}
	if within.Items[1].Parent == nil || within.Items[1].Parent.ID != a.ID || within.Items[1].Project == nil || within.Items[1].Project.ID != root.ID {
		t.Fatalf("deep projection: %#v", within.Items[1])
	}
	if got := get("/api/nodes?kind=ticket&kind=project&state=new,qa&priority=high&priority=medium&within=" + root.ID); len(got.Items) != 2 {
		t.Fatalf("list filters: %#v", got.Items)
	}
	if got := get("/api/nodes?assignee=" + p.ID); len(got.Items) != 1 || got.Items[0].ID != a.ID {
		t.Fatalf("assignee filter: %#v", got.Items)
	}
	if got := get("/api/nodes?assignee=none&within=" + root.ID); len(got.Items) != 2 {
		t.Fatalf("unassigned: %#v", got.Items)
	}
	if got := get("/api/nodes?hide_closed=true&within=" + root.ID); len(got.Items) != 2 {
		t.Fatalf("hide closed: %#v", got.Items)
	}
	if got := get("/api/nodes?q=PAI-2&sort=key"); len(got.Items) != 2 || got.Items[0].ID != a.ID {
		t.Fatalf("key prefix search: %#v", got.Items)
	}
	if got := get("/api/nodes?q=gamMA"); len(got.Items) != 1 || got.Items[0].ID != c.ID {
		t.Fatalf("title search: %#v", got.Items)
	}
	for _, sort := range []string{"key", "title", "state", "priority", "kind", "updated_at", "created_at", "position", "state,-updated_at"} {
		t.Run(sort, func(t *testing.T) {
			base := "/api/nodes?within=" + root.ID + "&sort=" + sort + "&limit=1"
			seen := map[string]bool{}
			cursor := ""
			for i := 0; i < 4; i++ {
				path := base
				if cursor != "" {
					path += "&cursor=" + url.QueryEscape(cursor)
				}
				page := get(path)
				if len(page.Items) == 0 {
					break
				}
				if seen[page.Items[0].ID] {
					t.Fatalf("duplicate cursor item for %s", sort)
				}
				seen[page.Items[0].ID] = true
				if page.NextCursor == nil {
					break
				}
				cursor = *page.NextCursor
			}
			if len(seen) != 3 {
				t.Fatalf("cursor %s returned %d distinct nodes", sort, len(seen))
			}
		})
	}
	if got := get("/api/nodes?within=" + root.ID + "&sort=state,-updated_at"); got.Items[0].ID != a.ID || got.Items[1].ID != c.ID || got.Items[2].ID != b.ID {
		t.Fatalf("workflow order: %#v", got.Items)
	}
	for _, path := range []string{"/api/nodes?limit=501", "/api/nodes?within=bad", "/api/nodes?sort=bogus", "/api/nodes?facets=bogus", "/api/nodes?within=" + root.ID + "&parent_id=" + root.ID} {
		status, body := call(t, &p, http.MethodGet, path, "")
		if status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, status, body)
		}
	}
	if got := get("/api/nodes?limit=500"); len(got.Items) != 6 {
		t.Fatalf("limit 500: %d", len(got.Items))
	}
	status, body := call(t, &p, http.MethodGet, "/api/projects", "")
	projects := decode[projectPage](t, status, body, http.StatusOK)
	if len(projects.Items) != 1 || projects.Items[0].ID != root.ID || projects.Items[0].Total != 3 || projects.Items[0].Open != 1 || projects.Items[0].InProgress != 1 || projects.Items[0].Done != 1 || projects.Items[0].LastActivity.IsZero() {
		t.Fatalf("projects: %#v", projects.Items)
	}
	status, body = call(t, &p, http.MethodGet, "/api/projects?include_archived=true", "")
	if got := decode[projectPage](t, status, body, http.StatusOK); len(got.Items) != 2 {
		t.Fatalf("archived projects: %#v", got.Items)
	}
	other := addPrincipal(t, "other-polished")
	assertTenantEmpty(t, other, "/api/nodes?facets=state,kind,priority,assignee")
	assertTenantEmpty(t, other, "/api/projects?include_archived=true")
	status, body = call(t, &other, http.MethodGet, "/api/nodes?within="+root.ID, "")
	if status != http.StatusOK {
		t.Fatalf("cross tenant within: %d %s", status, body)
	}
	if page := decode[nodePage](t, status, body, http.StatusOK); len(page.Items) != 0 {
		t.Fatalf("cross tenant within: %#v", page.Items)
	}
}

// Every row names its nearest epic: a ticket its own, a task its ticket's.
func TestListNearestEpic(t *testing.T) {
	p := newPrincipal(t, "nearest-epic")
	project := kindBySlug(t, p, "project")
	epicKind := kindBySlug(t, p, "epic")
	ticket := kindBySlug(t, p, "ticket")
	task := kindBySlug(t, p, "task")
	create := func(kind, key, parent string) nodeJSON {
		t.Helper()
		body := map[string]any{"kind_id": kind, "key": key, "title": key, "state": "new", "fields": map[string]any{}}
		if parent != "" {
			body["parent_id"] = parent
		}
		raw, _ := json.Marshal(body)
		return mustNode(t, p, string(raw))
	}
	root := create(project.ID, "EP-1", "")
	epic := create(epicKind.ID, "EP-2", root.ID)
	inEpic := create(ticket.ID, "EP-3", epic.ID)
	taskInEpic := create(task.ID, "EP-4", inEpic.ID)
	loose := create(ticket.ID, "EP-5", root.ID)
	looseTask := create(task.ID, "EP-6", loose.ID)
	status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&sort=key", "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	epics := map[string]*listEpic{}
	for _, item := range page.Items {
		epics[item.ID] = item.Epic
	}
	for _, id := range []string{inEpic.ID, taskInEpic.ID} {
		if got := epics[id]; got == nil || got.ID != epic.ID || got.Key != "EP-2" || got.Title != "EP-2" {
			t.Fatalf("epic of %s: %#v", id, got)
		}
	}
	for _, id := range []string{epic.ID, loose.ID, looseTask.ID} {
		if got, ok := epics[id]; !ok || got != nil {
			t.Fatalf("%s should list without an epic: %#v (listed %v)", id, got, ok)
		}
	}
	if !strings.Contains(string(body), `"epic":null`) {
		t.Fatalf("items without an epic send null: %s", body)
	}
}

func assertTenantEmpty(t *testing.T, p tenant.Principal, path string) {
	t.Helper()
	status, body := call(t, &p, http.MethodGet, path, "")
	if status != http.StatusOK {
		t.Fatalf("%s: %d %s", path, status, body)
	}
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &page); err != nil || len(page.Items) != 0 {
		t.Fatalf("%s: %s (%v)", path, body, err)
	}
}

func TestList6000Performance(t *testing.T) {
	testList6000Performance(t)
}

func TestList6000PerformanceWithStaleKindStatistics(t *testing.T) {
	newPrincipal(t, "previous-list-kind-statistics")
	if _, err := appPool.Exec(t.Context(), `ANALYZE node_kinds`); err != nil {
		t.Fatal(err)
	}
	p, path := testList6000Performance(t)
	status, body := call(t, &p, http.MethodGet, path, "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if len(page.Items) != 50 || page.NextCursor == nil || page.Facets["kind"]["ticket"] != 6000 {
		t.Fatal("stale-statistics fixture lost its 6000 tickets or selected page")
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

func testList6000Performance(t *testing.T) (tenant.Principal, string) {
	t.Helper()
	p := newPrincipal(t, "large-list")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Large project"}`)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,fields,state,parent_id,position)
            SELECT $1::uuid,'PERF-'||g,$2::uuid,'Item '||g,
                jsonb_build_object('priority',CASE g%3 WHEN 0 THEN 'high' WHEN 1 THEN 'medium' ELSE 'low' END),
                CASE g%4 WHEN 0 THEN 'new' WHEN 1 THEN 'active' WHEN 2 THEN 'qa' ELSE 'done' END,
                $3::uuid,g FROM generate_series(1,6000) AS g`, p.TenantID, ticket.ID, root.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Importer writes refresh statistics after the bulk transaction; exercise
	// the same plan here so row estimates cannot hide a slow recursive walk.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `ANALYZE nodes`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes?within=" + root.ID + "&sort=state,-updated_at&limit=50&facets=state,kind,priority,assignee"
	var fastest time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		status, body := call(t, &p, http.MethodGet, path, "")
		elapsed := time.Since(start)
		page := decode[nodePage](t, status, body, http.StatusOK)
		if len(page.Items) != 50 || page.NextCursor == nil || page.Facets["kind"]["ticket"] != 6000 {
			t.Fatalf("large list result: %d, %#v", len(page.Items), page.Facets)
		}
		if fastest == 0 || elapsed < fastest {
			fastest = elapsed
		}
	}
	t.Logf("6000-node list with facets: fastest of 3 = %s", fastest)
	// Shared CI runners are slower and noisier than a workstation; keep a
	// regression guard there without failing on runner variance.
	limit := 150 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 600 * time.Millisecond
	}
	if fastest >= limit {
		t.Fatalf("6000-node list exceeded %s: %s", limit, fastest)
	}
	start := time.Now()
	status, body := call(t, &p, http.MethodGet, "/api/projects", "")
	projects := decode[projectPage](t, status, body, http.StatusOK)
	t.Logf("6000-node projects: %s", time.Since(start))
	if len(projects.Items) != 1 || projects.Items[0].Total != 6000 {
		t.Fatalf("large project: %#v", projects.Items)
	}
	if os.Getenv("AEON_PERF") == "1" {
		query, err := parseListQuery(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatal(err)
		}
		listQuerySQL, listArgs := listSQL(query, nil)
		facetQuerySQL, facetArgs := facetSQL(query)
		for _, plan := range []struct {
			name string
			sql  string
			args []any
		}{
			{"node_list", listQuerySQL, listArgs},
			{"node_facets", facetQuerySQL, facetArgs},
		} {
			err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				rows, err := tx.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+plan.sql, plan.args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var line string
					if err := rows.Scan(&line); err != nil {
						return err
					}
					t.Logf("PLAN %s %s", plan.name, line)
				}
				return rows.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return p, path
}

// Project summaries name the people most recently active in each project: the
// actors of recent events on the project or anything below it, newest first,
// a linked principal as its person, at most five, nothing older than 90 days.
func TestProjectSummaryRecentPeople(t *testing.T) {
	p := newPrincipal(t, "recent-people")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	main := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Main"}`)
	child := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"Child","parent_id":"`+main.ID+`"}`)
	grandchild := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"Grandchild","parent_id":"`+child.ID+`"}`)
	ctx := t.Context()
	var ids = map[string]string{}
	var quiet string
	err := db.InTenant(dbtest.Seed(ctx), appPool, p.TenantID, func(tx pgx.Tx) error {
		// A project nobody touched (inserted without an event).
		if err := tx.QueryRow(ctx, `INSERT INTO nodes (tenant_id, key, kind_id, title) VALUES ($1, 'QUI-1', $2, 'Quiet') RETURNING id::text`, p.TenantID, project.ID).Scan(&quiet); err != nil {
			return err
		}
		for _, person := range []struct{ name, kind string }{{"Mira", "person"}, {"Robo", "agent"}, {"Old", "person"}, {"Ann", "person"}, {"Ben", "person"}, {"Cy", "person"}, {"Dee", "person"}} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, $2, $3) RETURNING id::text`, p.TenantID, person.kind, person.name).Scan(&id); err != nil {
				return err
			}
			ids[person.name] = id
		}
		// A classic import principal linked to Mira counts as Mira.
		var imported string
		if err := tx.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name, linked_to) VALUES ($1, 'person', 'mira (classic)', $2) RETURNING id::text`, p.TenantID, ids["Mira"]).Scan(&imported); err != nil {
			return err
		}
		ids["imported"] = imported
		event := func(actor, node string, hoursAgo float64) error {
			_, err := tx.Exec(ctx, `INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after, at) VALUES ($1, $2, $3, 'node.updated', '{}'::jsonb, now() - make_interval(secs => $4))`, p.TenantID, actor, node, hoursAgo*3600)
			return err
		}
		for _, e := range []struct {
			actor, node string
			hours       float64
		}{
			{ids["Robo"], grandchild.ID, 1},
			{ids["imported"], child.ID, 2},
			{ids["Mira"], main.ID, 30},
			{ids["Old"], child.ID, 24 * 120},
			{ids["Ann"], child.ID, 3},
			{ids["Ben"], child.ID, 4},
			{ids["Cy"], child.ID, 5},
			{ids["Dee"], child.ID, 6},
		} {
			if err := event(e.actor, e.node, e.hours); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := call(t, &p, http.MethodGet, "/api/projects", "")
	page := decode[projectPage](t, status, body, http.StatusOK)
	byID := map[string]projectSummary{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	var names []string
	for _, person := range byID[main.ID].People {
		names = append(names, person.Name+":"+person.Kind)
	}
	// The creator of the nodes acted last (node.created, just now); Dee and Old drop out.
	if strings.Join(names, ",") != "recent-people:person,Robo:agent,Mira:person,Ann:person,Ben:person" {
		t.Fatalf("recent people = %v", names)
	}
	if byID[main.ID].People[2].ID != ids["Mira"] {
		t.Fatalf("linked principal shown as itself: %#v", byID[main.ID].People[2])
	}
	if got, ok := byID[quiet]; !ok || got.People == nil || len(got.People) != 0 {
		t.Fatalf("quiet project people = %#v (%s)", got.People, body)
	}
	if !strings.Contains(string(body), `"people":[]`) {
		t.Fatalf("empty people must serialize as []: %s", body)
	}
}

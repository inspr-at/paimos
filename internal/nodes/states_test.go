// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPatchPreconditionAtomic(t *testing.T) {
	p := newPrincipal(t, "preconditions")
	k := kindBySlug(t, p, "ticket")
	n := mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Before"}`)
	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	patch := func(header string) int {
		r := httptest.NewRequest("PATCH", "/api/nodes/"+n.ID, strings.NewReader(`{"title":"After"}`))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		r.Header.Set("If-Unmodified-Since", header)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if got := patch("bad-date"); got != 400 {
		t.Fatalf("malformed = %d", got)
	}
	if got := patch(""); got != 400 {
		t.Fatalf("empty = %d", got)
	}
	if got := patch(n.UpdatedAt.Add(time.Hour).Format(time.RFC3339Nano)); got != 412 {
		t.Fatalf("mismatch = %d", got)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results <- patch(n.UpdatedAt.Format(time.RFC3339Nano)) }()
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[412] != 1 {
		t.Fatalf("competing patches: %v", counts)
	}
	var changes int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='node.updated'`, p.TenantID, n.ID).Scan(&changes)
	}); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("rejected patch wrote an event: %d", changes)
	}
	other := addPrincipal(t, "preconditions-other")
	r := httptest.NewRequest("PATCH", "/api/nodes/"+n.ID, strings.NewReader(`{"title":"Other"}`))
	r.Header.Set("If-Unmodified-Since", n.UpdatedAt.Format(time.RFC3339Nano))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), other))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("cross tenant = %d", w.Code)
	}
	status, body := call(t, &p, "PATCH", "/api/nodes/"+n.ID, `{"title":"Unconditional"}`)
	decode[nodeJSON](t, status, body, 200)
}

func TestClassicAssigneeProjectionAndFacets(t *testing.T) {
	p := newPrincipal(t, "assignee-fallback")
	k := kindBySlug(t, p, "ticket")
	var mapped string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,display_name) VALUES('paimos-classic','source:7','Markus Barta') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,identity_id) VALUES($1,'person','Markus Barta',$2) RETURNING id::text`, p.TenantID, identity).Scan(&mapped)
	}); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []string{
		`{"classic":{"source_id":"source","assignee_id":7}}`,
		`{"assignee_id":"` + mapped + `"}`,
		`{"assignee":{"id":"` + mapped + `"}}`,
		`{"assignee":null,"classic":{"source_id":"source","assignee_id":7}}`,
		`{"classic":{"source_id":"other-source","assignee_id":7}}`,
	} {
		mustNode(t, p, `{"kind_id":"`+k.ID+`","title":"Ticket","fields":`+fields+`}`)
	}
	status, body := call(t, &p, "GET", "/api/nodes?assignee="+mapped+"&facets=assignee", "")
	page := decode[nodePage](t, status, body, 200)
	if len(page.Items) != 3 || page.Facets["assignee"][mapped] != 3 {
		t.Fatalf("mapped filter/facets: %s", body)
	}
	for _, n := range page.Items {
		if n.Assignee == nil || n.Assignee.Name != "Markus Barta" {
			t.Fatalf("mapped name: %#v", n.Assignee)
		}
	}
	status, body = call(t, &p, "GET", "/api/nodes?assignee=none", "")
	page = decode[nodePage](t, status, body, 200)
	if len(page.Items) != 2 {
		t.Fatalf("unassigned = %s", body)
	}
	other := addPrincipal(t, "assignee-other")
	otherKind := kindBySlug(t, other, "ticket")
	status, body = call(t, &other, "POST", "/api/nodes", `{"kind_id":"`+otherKind.ID+`","title":"Other","fields":{"assignee":"`+mapped+`"}}`)
	if status != 400 {
		t.Fatalf("accepted foreign assignment: %d %s", status, body)
	}
	mustNode(t, other, `{"kind_id":"`+otherKind.ID+`","title":"Other","fields":{"classic":{"source_id":"source","assignee_id":7}}}`)
	status, body = call(t, &other, "GET", "/api/nodes?facets=assignee", "")
	page = decode[nodePage](t, status, body, 200)
	if len(page.Items) != 1 || page.Items[0].Assignee != nil || page.Facets["assignee"]["none"] != 1 {
		t.Fatalf("cross tenant mapping: %s", body)
	}
}

func TestProjectCountsWorkKindsAndStateGroups(t *testing.T) {
	p := newPrincipal(t, "work-counts")
	create := func(kind, state, parent string) nodeJSON {
		t.Helper()
		k := kindBySlug(t, p, kind)
		body := map[string]any{"kind_id": k.ID, "title": kind, "state": state}
		if parent != "" {
			body["parent_id"] = parent
		}
		if kind == "ticket" {
			body["fields"] = json.RawMessage(benefitFields)
		}
		raw, _ := json.Marshal(body)
		return mustNode(t, p, string(raw))
	}
	root := create("project", "active", "")
	folder := create("release", "done", root.ID)
	create("memory", "done", root.ID)
	for i, state := range []string{"new", "backlog", "in_progress", "qa", "done", "delivered", "accepted", "cancelled"} {
		create([]string{"ticket", "task", "epic"}[i%3], state, folder.ID)
	}
	status, body := call(t, &p, "GET", "/api/projects", "")
	projects := decode[projectPage](t, status, body, 200)
	if len(projects.Items) != 1 {
		t.Fatalf("projects: %s", body)
	}
	got := projects.Items[0]
	if got.Total != 8 || got.Open != 2 || got.InProgress != 2 || got.Done != 3 || got.Cancelled != 1 {
		t.Fatalf("groups: %+v", got)
	}
	status, body = call(t, &p, "GET", "/api/nodes?within="+root.ID+"&kind=ticket,task,epic&sort=state", "")
	page := decode[nodePage](t, status, body, 200)
	if len(page.Items) != got.Total {
		t.Fatalf("list/count mismatch %d / %d", len(page.Items), got.Total)
	}
	for i, n := range page.Items {
		if n.State == "delivered" && (i == 0 || page.Items[i-1].State != "done") {
			t.Fatalf("delivered workflow order: %s", body)
		}
	}
}

func TestProjectStatusBuckets(t *testing.T) {
	if normaliseWorkState(" QA ") != "qa" || normaliseWorkState("in-progress") != "in_progress" || normaliseWorkState("in progress") != "in_progress" {
		t.Fatalf("state spelling: %q %q %q", normaliseWorkState(" QA "), normaliseWorkState("in-progress"), normaliseWorkState("in progress"))
	}
	p := newPrincipal(t, "status-buckets")
	create := func(kind, state, parent string) {
		t.Helper()
		k := kindBySlug(t, p, kind)
		body := map[string]any{"kind_id": k.ID, "title": kind + " " + state, "state": state, "parent_id": parent}
		if kind == "ticket" {
			body["fields"] = json.RawMessage(benefitFields)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		mustNode(t, p, string(raw))
	}
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Buckets","state":"active"}`)
	folder := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "release").ID+`","title":"Folder","state":"done","parent_id":"`+root.ID+`"}`)
	// Every stored spelling, plus one unknown state. A non-work node must not count.
	for _, state := range []string{
		"new", "backlog", "open", "blocked", "mystery",
		"in_progress", "in-progress", "in progress", "inprogress", "active", "qa", " QA ",
		"accepted", "delivered", "done", "cancelled", "canceled", "archived",
	} {
		create("ticket", state, folder.ID)
	}
	create("task", "open", folder.ID)
	create("epic", "blocked", folder.ID)
	create("memory", "done", root.ID)

	assertBuckets := func(open, progress, done, cancelled, total int) projectSummary {
		t.Helper()
		status, body := call(t, &p, "GET", "/api/projects", "")
		projects := decode[projectPage](t, status, body, 200)
		if len(projects.Items) != 1 {
			t.Fatalf("projects: %s", body)
		}
		got := projects.Items[0]
		if got.Open != open || got.InProgress != progress || got.Done != done || got.Cancelled != cancelled || got.Total != total {
			t.Fatalf("buckets open=%d progress=%d done=%d cancelled=%d total=%d, got %+v", open, progress, done, cancelled, total, got)
		}
		return got
	}
	// open: new, backlog, open, blocked, mystery, task open, epic blocked.
	// in progress: four spellings, active, qa, " QA ".
	// done: accepted, delivered, done. cancelled: cancelled, canceled. archived is total only.
	assertBuckets(7, 7, 3, 2, 20)

	ticketKind := kindBySlug(t, p, "ticket")
	var schema map[string]any
	if err := json.Unmarshal(ticketKind.FieldSchema, &schema); err != nil {
		t.Fatal(err)
	}
	schema["states"] = []any{
		map[string]string{"state": "mystery", "category": "done"},
		map[string]string{"state": "blocked", "category": "doing"},
		map[string]string{"state": "qa", "category": "open"},
		map[string]string{"state": "QA", "category": "done"},
	}
	raw, err := json.Marshal(map[string]any{"field_schema": schema})
	if err != nil {
		t.Fatal(err)
	}
	if status, body := call(t, &p, "PATCH", "/api/kinds/"+ticketKind.ID, string(raw)); status != 400 {
		t.Fatalf("duplicate state accepted: %d %s", status, body)
	}
	schema["states"] = []any{map[string]string{"state": "mystery", "category": "nope"}}
	raw, _ = json.Marshal(map[string]any{"field_schema": schema})
	if status, body := call(t, &p, "PATCH", "/api/kinds/"+ticketKind.ID, string(raw)); status != 400 {
		t.Fatalf("unknown category accepted: %d %s", status, body)
	}
	schema["states"] = []any{
		map[string]string{"state": "mystery", "category": "done"},
		map[string]string{"state": "blocked", "category": "doing"},
		map[string]string{"state": "qa", "category": "open"},
	}
	raw, _ = json.Marshal(map[string]any{"field_schema": schema})
	status, body := call(t, &p, "PATCH", "/api/kinds/"+ticketKind.ID, string(raw))
	updated := decode[kindJSON](t, status, body, 200)
	var stored map[string]any
	if err := json.Unmarshal(updated.FieldSchema, &stored); err != nil {
		t.Fatal(err)
	}
	props, _ := stored["properties"].(map[string]any)
	if _, ok := props["pill_en"]; !ok {
		t.Fatalf("state catalog replaced field properties: %s", updated.FieldSchema)
	}
	// Ticket catalog only: mystery is done, blocked is in progress, both qa spellings are open.
	// The epic's blocked state keeps the fixed mapping.
	got := assertBuckets(7, 6, 4, 2, 20)
	status, body = call(t, &p, "GET", "/api/nodes?within="+root.ID+"&kind=ticket,task,epic&hide_closed=true&limit=100", "")
	hidden := decode[nodePage](t, status, body, 200)
	if len(hidden.Items) != got.Open+got.InProgress {
		t.Fatalf("hide closed %d, open+doing %d", len(hidden.Items), got.Open+got.InProgress)
	}
	seen := map[string]bool{}
	for _, n := range hidden.Items {
		seen[n.Title] = true
	}
	for _, title := range []string{"ticket mystery", "ticket canceled", "ticket done", "ticket archived", "ticket accepted", "ticket delivered"} {
		if seen[title] {
			t.Fatalf("closed work stayed visible: %s", title)
		}
	}
	for _, title := range []string{"ticket open", "ticket blocked", "task open", "epic blocked"} {
		if !seen[title] {
			t.Fatalf("open work hidden: %s", title)
		}
	}
}

// Hide closed and the project counts share one bucket, per work kind.
func TestHideClosedAgreesWithBuckets(t *testing.T) {
	p := newPrincipal(t, "hide-closed-buckets")
	for _, kind := range []string{"ticket", "task", "epic"} {
		t.Run(kind, func(t *testing.T) {
			k := kindBySlug(t, p, kind)
			var schema map[string]any
			if err := json.Unmarshal(k.FieldSchema, &schema); err != nil {
				t.Fatal(err)
			}
			schema["states"] = []any{map[string]string{"state": "mystery", "category": "done"}}
			raw, err := json.Marshal(map[string]any{"field_schema": schema})
			if err != nil {
				t.Fatal(err)
			}
			if status, body := call(t, &p, "PATCH", "/api/kinds/"+k.ID, string(raw)); status != 200 {
				t.Fatalf("catalog: %d %s", status, body)
			}
			root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"`+kind+` project","state":"active"}`)
			for _, state := range []string{"open", "blocked", "mystery", "canceled", "done", "accepted", "delivered", "in-progress", "qa", "archived"} {
				body := map[string]any{"kind_id": k.ID, "title": kind + " " + state, "state": state, "parent_id": root.ID}
				if kind == "ticket" {
					body["fields"] = json.RawMessage(benefitFields)
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				mustNode(t, p, string(raw))
			}
			status, body := call(t, &p, "GET", "/api/projects", "")
			projects := decode[projectPage](t, status, body, 200)
			var summary projectSummary
			found := false
			for _, item := range projects.Items {
				if item.ID == root.ID {
					summary = item
					found = true
				}
			}
			if !found {
				t.Fatalf("project missing: %s", body)
			}
			// open, blocked stay open; in-progress and qa are doing. mystery is done by category.
			// canceled, done, accepted, delivered and archived are closed.
			if summary.Open != 2 || summary.InProgress != 2 {
				t.Fatalf("counts %+v", summary)
			}
			status, body = call(t, &p, "GET", "/api/nodes?within="+root.ID+"&kind="+kind+"&hide_closed=true&limit=100", "")
			page := decode[nodePage](t, status, body, 200)
			if len(page.Items) != summary.Open+summary.InProgress {
				t.Fatalf("hide closed %d, open+doing %d (%s)", len(page.Items), summary.Open+summary.InProgress, body)
			}
			seen := map[string]bool{}
			for _, n := range page.Items {
				seen[n.State] = true
				if n.KindSlug != kind {
					t.Fatalf("kind %s in %s list", n.KindSlug, kind)
				}
			}
			for _, state := range []string{"open", "blocked", "in-progress", "qa"} {
				if !seen[state] {
					t.Fatalf("%s hidden", state)
				}
			}
			for _, state := range []string{"mystery", "canceled", "done", "accepted", "delivered", "archived"} {
				if seen[state] {
					t.Fatalf("%s stayed visible", state)
				}
			}
		})
	}
}

func TestStateSortFollowsWorkflow(t *testing.T) {
	p := newPrincipal(t, "state-sort")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Sort","state":"active"}`)
	for _, state := range []string{"archived", "mystery", "done", "qa", "active", "in-progress", "blocked", "open", "new", "cancelled", "canceled", "accepted", "delivered"} {
		body := map[string]any{"kind_id": ticket.ID, "title": state, "state": state, "parent_id": root.ID}
		if state == "done" || state == "accepted" || state == "delivered" {
			body["fields"] = json.RawMessage(benefitFields)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		mustNode(t, p, string(raw))
	}
	want := []string{"new", "open", "blocked", "active", "in-progress", "qa", "done", "delivered", "accepted", "canceled", "cancelled", "archived", "mystery"}
	get := func(path string) nodePage {
		t.Helper()
		status, body := call(t, &p, http.MethodGet, path, "")
		return decode[nodePage](t, status, body, http.StatusOK)
	}
	statesOf := func(page nodePage) []string {
		out := make([]string, len(page.Items))
		for i, n := range page.Items {
			out[i] = n.State
		}
		return out
	}
	descWant := []string{"archived", "cancelled", "canceled", "accepted", "delivered", "done", "qa", "in-progress", "active", "blocked", "open", "new", "mystery"}
	assertSorted := func(sort, label string, expect []string) {
		t.Helper()
		if got := statesOf(get("/api/nodes?within=" + root.ID + "&sort=" + sort + "&limit=100")); strings.Join(got, ",") != strings.Join(expect, ",") {
			t.Fatalf("%s order:\n got %v\nwant %v", label, got, expect)
		}
		var paged []string
		cursor := ""
		for range len(expect) + 1 {
			path := "/api/nodes?within=" + root.ID + "&sort=" + url.QueryEscape(sort) + "&limit=2"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			page := get(path)
			paged = append(paged, statesOf(page)...)
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		if strings.Join(paged, ",") != strings.Join(expect, ",") {
			t.Fatalf("%s paged order:\n got %v\nwant %v", label, paged, expect)
		}
	}
	assertSorted("state", "workflow", want)
	assertSorted("-state", "descending", descWant)
}

func TestStateSortNormalisesSpellings(t *testing.T) {
	p := newPrincipal(t, "state-sort-norm")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Norm sort","state":"active"}`)
	for _, state := range []string{"mystery", " OPEN ", "done", "in--progress", " QA ", "archived", "open", "qa"} {
		body := map[string]any{"kind_id": ticket.ID, "title": state, "state": state, "parent_id": root.ID}
		if state == "done" {
			body["fields"] = json.RawMessage(benefitFields)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		mustNode(t, p, string(raw))
	}
	get := func(path string) nodePage {
		t.Helper()
		status, body := call(t, &p, http.MethodGet, path, "")
		return decode[nodePage](t, status, body, http.StatusOK)
	}
	statesOf := func(page nodePage) []string {
		out := make([]string, len(page.Items))
		for i, n := range page.Items {
			out[i] = n.State
		}
		return out
	}
	groupsOf := func(states []string) []string {
		out := make([]string, len(states))
		for i, state := range states {
			switch normaliseWorkState(state) {
			case "open", "in_progress", "qa", "done", "archived":
				out[i] = normaliseWorkState(state)
			default:
				out[i] = "unknown"
			}
		}
		return out
	}
	assertSorted := func(sort, label, groups string) {
		t.Helper()
		full := statesOf(get("/api/nodes?within=" + root.ID + "&sort=" + sort + "&limit=100"))
		if strings.Join(groupsOf(full), ",") != groups {
			t.Fatalf("%s groups:\n got %v\nwant %s\nstates %v", label, groupsOf(full), groups, full)
		}
		var paged []string
		cursor := ""
		for range len(full) + 1 {
			path := "/api/nodes?within=" + root.ID + "&sort=" + url.QueryEscape(sort) + "&limit=2"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			page := get(path)
			paged = append(paged, statesOf(page)...)
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		if strings.Join(paged, "\n") != strings.Join(full, "\n") {
			t.Fatalf("%s pages disagree:\n got %q\nwant %q", label, paged, full)
		}
	}
	assertSorted("state", "normalised", "open,open,in_progress,qa,qa,done,archived,unknown")
	assertSorted("-state", "normalised descending", "archived,done,qa,qa,in_progress,open,open,unknown")
}

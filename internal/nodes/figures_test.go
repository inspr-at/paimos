// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/views"
	"github.com/jackc/pgx/v5"
)

// Risk: paged/hidden rows, grouping parents, stale caches or another tenant's
// data could change the amount of work a view claims. Use actual writes and
// their events, retaining every fixture, without time-based waits.
func TestProjectFiguresScopeBucketsCacheAndIsolation(t *testing.T) {
	p := newPrincipal(t, "figures")
	projectKind, workKind := kindBySlug(t, p, "project"), kindBySlug(t, p, "work")
	root := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Figures","state":"active"}`, projectKind.ID))
	create := func(title, state, parent string, hours any, priority string) nodeJSON {
		t.Helper()
		fields := map[string]any{}
		if err := json.Unmarshal([]byte(benefitFields), &fields); err != nil {
			t.Fatal(err)
		}
		if hours != nil {
			fields["estimate_hours"] = hours
		}
		fields["priority"], fields["tags"], fields["release"], fields["cost_unit"] = priority, []string{"figures"}, "134", "core"
		raw, _ := json.Marshal(map[string]any{"kind_id": workKind.ID, "title": title, "state": state, "parent_id": parent, "fields": fields})
		return mustNode(t, p, string(raw))
	}
	parent := create("Parent planned 100", "open", root.ID, 100, "high")
	open := create("Needle open", "open", parent.ID, 2, "high")
	progress := create("Needle working", "in_progress", parent.ID, 3, "high")
	qa := create("Quality", "qa", root.ID, 4, "low")
	done := create("Merged", "done", root.ID, 5, "low")
	delivered := create("Live", "delivered", root.ID, 6, "low")
	accepted := create("Accepted", "accepted", root.ID, 7, "low")
	cancelled := create("Cancelled", "cancelled", root.ID, 8, "high")
	archived := create("Archived", "archived", root.ID, 9, "low")
	missing := create("No size", "new", root.ID, nil, "high")
	deleted := create("Deleted", "open", root.ID, 10, "high")
	if code, body := call(t, &p, "DELETE", "/api/nodes/"+deleted.ID, ""); code != 204 {
		t.Fatalf("delete: %d %s", code, body)
	}
	mod := New(appPool, nil).(*Module)
	base := "/api/projects/" + root.ID + "/figures"
	get := func(who tenant.Principal, query string) projectFigures {
		t.Helper()
		code, body := callAs(t, mod, &who, "GET", base+query, "")
		return decode[projectFigures](t, code, body, 200)
	}
	whole := get(p, "")
	if whole.TotalHours != 27 || whole.WithoutEstimate != 1 || !whole.Approximate || whole.OpenEstimatedLeaves != 3 || !whole.MarksReleases || whole.Scope.Kind != "whole_project" || whole.Scope.Rows != 10 || whole.Scope.Parents != 1 || whole.Scope.Leaves != 9 || whole.Scope.ProjectLeaves != 9 || whole.Scope.Closed != 2 {
		t.Fatalf("whole figures: %+v", whole)
	}
	for group, want := range map[string]float64{"completed": 13, "done_not_live": 5, "in_progress": 7, "open": 2} {
		if whole.Groups[group].Hours != want {
			t.Fatalf("%s: %+v", group, whole.Groups[group])
		}
	}
	if whole.Groups["in_progress"].Statuses["qa"] != 1 || whole.Columns["estimate"].Numbers.Total != whole.TotalHours || *whole.Columns["estimate"].Numbers.Median != 4.5 || whole.Columns["estimate"].Included != 7 || whole.Columns["tokens"].Included != 9 || whole.Columns["created_at"].Rows != 10 {
		t.Fatalf("column scope differs: %+v", whole.Columns)
	}
	if len(mod.figures.entries) != 1 {
		t.Fatal("scope was not cached")
	}
	code, body := call(t, &p, "GET", "/api/nodes?within="+root.ID+"&kind=work&limit=1&sort=key", "")
	page := decode[nodePage](t, code, body, 200)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal("fixture did not establish a paged list")
	}
	// Same response object and timestamp prove the warm read, including an
	// invalid presentation cursor, did not recompute or narrow to the first page.
	hidden := get(p, "?hide_closed=true&hide_states=accepted&limit=1&cursor=expired&sort=-estimate&facets=state")
	if !reflect.DeepEqual(whole, hidden) || len(mod.figures.entries) != 1 {
		t.Fatal("Hide/paging changed cached figures")
	}
	searched := get(p, "?q=Needle&priority=high&limit=1")
	if searched.TotalHours != 5 || searched.Scope.Leaves != 2 || searched.Scope.ProjectLeaves != 9 || searched.Scope.Kind != "this_view" {
		t.Fatalf("search/filter scope: %+v", searched)
	}
	selected := get(p, "?work_state=qa")
	if selected.TotalHours != 4 || selected.Scope.Leaves != 1 || selected.Groups["in_progress"].Tickets != 1 {
		t.Fatalf("canonical status: %+v", selected)
	}
	closed := get(p, "?work_bucket=cancelled,archived")
	if closed.TotalHours != 0 || closed.Scope.Closed != 2 || closed.WithoutEstimate != 0 || closed.Columns["tokens"].Included != 2 {
		t.Fatalf("closed: %+v", closed)
	}
	// Saved views store UI query filters. The caller resolves them with the
	// same node-list parameter mapping used by the rendered list.
	code, body = callAs(t, views.New(appPool), &p, "POST", "/api/views", fmt.Sprintf(`{"name":"Needle","project_id":%q,"filters":{"q":"Needle","priority":"high","closed":"1"},"columns":["key","estimate"]}`, root.ID))
	if code != 201 {
		t.Fatalf("saved view: %d %s", code, body)
	}
	var saved struct {
		ID      string            `json:"id"`
		Filters map[string]string `json:"filters"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	code, body = callAs(t, views.New(appPool), &p, "GET", "/api/views/"+saved.ID, "")
	if code != 200 {
		t.Fatalf("read saved view: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	params := url.Values{"q": {saved.Filters["q"]}, "priority": {saved.Filters["priority"]}, "hide_closed": {"false"}}
	if fromView := get(p, "?"+params.Encode()); fromView.TotalHours != searched.TotalHours || fromView.Scope != searched.Scope || fromView.ViewKey != searched.ViewKey {
		t.Fatalf("saved view: %+v", fromView)
	}
	// An estimate write invalidates the warm scope, then a status write moves
	// that same leaf across buckets. Neither assertion can pass on stale data.
	code, body = call(t, &p, "PATCH", "/api/nodes/"+open.ID, `{"estimate_hours":2.5}`)
	if code != 200 {
		t.Fatalf("estimate write: %d %s", code, body)
	}
	resized := get(p, "")
	if resized.TotalHours != 27.5 || resized.Groups["open"].Hours != 2.5 {
		t.Fatalf("estimate invalidation: %+v", resized)
	}
	code, body = call(t, &p, "PATCH", "/api/nodes/"+open.ID, `{"state":"in_progress"}`)
	if code != 200 {
		t.Fatalf("status write: %d %s", code, body)
	}
	moved := get(p, "")
	if moved.TotalHours != 27.5 || moved.Groups["open"].Hours != 0 || moved.Groups["in_progress"].Hours != 9.5 {
		t.Fatalf("status invalidation: %+v", moved)
	}
	added := create("Added after caching", "open", root.ID, 1, "low")
	if got := get(p, ""); got.TotalHours != 28.5 || got.Scope.Leaves != 10 {
		t.Fatalf("creation invalidation: %+v", got)
	}
	// A no-release project uses Done as its final step. Inject the read clock
	// rather than sleeping or rewriting the append-only event log.
	var noReleases *projectFigures
	err := db.InTenantReadSnapshot(tenant.WithPrincipal(t.Context(), p), appPool, p.TenantID, func(tx pgx.Tx) error {
		seen, err := assigneeAudience(tenant.WithPrincipal(t.Context(), p), tx, p)
		if err != nil {
			return err
		}
		noReleases, err = mod.computeFigures(tenant.WithPrincipal(t.Context(), p), tx, root.ID, listQuery{Within: &root.ID, Kinds: []string{"work", "ticket", "task", "epic"}}, seen, time.Now().UTC().AddDate(0, 0, 91))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if noReleases.MarksReleases || noReleases.Groups["completed"].Hours != 18 || noReleases.Groups["done_not_live"].Tickets != 0 {
		t.Fatalf("no release final step: %+v", noReleases)
	}
	// A tenant-private project is invisible even if its UUID or cached query
	// is known. A second tenant's own scope stays independent.
	foreign := addPrincipal(t, "figures-foreign")
	foreignRoot := mustNode(t, foreign, fmt.Sprintf(`{"kind_id":%q,"title":"Foreign","state":"active"}`, kindBySlug(t, foreign, "project").ID))
	mustNode(t, foreign, fmt.Sprintf(`{"kind_id":%q,"title":"Foreign work","parent_id":%q,"fields":{"estimate_hours":99}}`, kindBySlug(t, foreign, "work").ID, foreignRoot.ID))
	if code, body := callAs(t, mod, &foreign, "GET", base, ""); code != 404 {
		t.Fatalf("foreign cache read: %d %s", code, body)
	}
	code, body = callAs(t, mod, &foreign, "GET", "/api/projects/"+foreignRoot.ID+"/figures", "")
	if f := decode[projectFigures](t, code, body, 200); f.TotalHours != 99 || f.Scope.Leaves != 1 {
		t.Fatalf("foreign own figures: %+v", f)
	}
	if code, body := callAs(t, mod, &p, "GET", base+"?within="+foreignRoot.ID, ""); code != 400 {
		t.Fatalf("foreign subtree: %d %s", code, body)
	}
	// Current projection permissions are part of the cache identity, even
	// when a role write has not emitted a node event.
	reader := insertPerson(t, p.TenantID, "restricted reader")
	bindNamesOnly(t, p.TenantID, reader.ID)
	if _, err := adminPool.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,id,'harness.read' FROM roles WHERE tenant_id=$1 AND key='names_only'`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	if got := get(reader, ""); got.Columns["cost"] == nil {
		t.Fatal("cost grant did not project cost")
	}
	if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_permissions WHERE tenant_id=$1 AND permission='harness.read' AND role_id IN (SELECT id FROM roles WHERE tenant_id=$1 AND key='names_only')`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	if got := get(reader, ""); got.Columns["cost"] != nil {
		t.Fatal("revoked cost grant reused a privileged cache entry")
	}
	// A project-only binding may read its own figures but cannot use the
	// project UUID or a narrower subtree to cross that visibility boundary.
	otherRoot := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Other project","state":"active"}`, projectKind.ID))
	create("Other project work", "open", otherRoot.ID, 88, "high")
	projectReader := insertPerson(t, p.TenantID, "project reader")
	bindProjectRole(t, p.TenantID, projectReader.ID, "viewer", root.ID)
	if got := get(projectReader, ""); got.TotalHours != 28.5 || got.Scope.ProjectLeaves != 10 {
		t.Fatalf("project binding scope: %+v", got)
	}
	if code, body := callAs(t, mod, &projectReader, "GET", "/api/projects/"+otherRoot.ID+"/figures", ""); code != 404 {
		t.Fatalf("hidden project: %d %s", code, body)
	}
	if code, body := callAs(t, mod, &p, "GET", base+"?within="+otherRoot.ID, ""); code != 400 {
		t.Fatalf("other project subtree: %d %s", code, body)
	}
	if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, reader.ID); err != nil {
		t.Fatal(err)
	}
	if code, body := callAs(t, mod, &reader, "GET", base, ""); code != 404 && code != 403 {
		t.Fatalf("revoked reader got cached success: %d %s", code, body)
	}
	for _, n := range []nodeJSON{progress, qa, done, delivered, accepted, cancelled, archived, missing, added} {
		if n.ID == "" {
			t.Fatal("fixture missing")
		}
	}
}

// Risk: column totals could double-count parent plans, mix estimated and
// measured usage, include Closed progress, or drop the top-N tail silently.
func TestFigureColumnsLeafPolicyAndUsage(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	ptr := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }
	pct := 50
	spent, estimated := int64(100), int64(200)
	rows := []figureRow{{item: listItem{nodeJSON: nodeJSON{Key: "F-1", CreatedAt: now, UpdatedAt: now}}, hours: ptr(100)}}
	for i := 0; i < 7; i++ {
		rows = append(rows, figureRow{item: listItem{nodeJSON: nodeJSON{Key: fmt.Sprintf("F-%d", i+2), CreatedAt: now.AddDate(0, 0, -20), UpdatedAt: now.AddDate(0, 0, -8)}}, leaf: true, bucket: "in_progress", status: "in_progress", hours: ptr(2), labels: map[string]any{"tags": []any{"tag", "tag"}, "cost_unit": "core", "priority": "high"}, assignee: fmt.Sprintf("Worker %d", i), eta: &columnETA{Progress: &pct, Running: true, ReadyAt: &now}, plan: &planningView{Tokens: planningTokens{Spent: &spent, Estimated: &estimated, Unreported: 1}, Cost: &planningCost{ListSpent: str("1.5"), ListEstimated: str("2.5")}}})
	}
	for i := 1; i <= 7; i++ {
		rows[i].plan.Models = []planningModel{{Model: "used-model"}}
		rows[i].plan.Route = &planningRoute{Model: "planned-model"}
	}
	rows = append(rows, figureRow{item: listItem{nodeJSON: nodeJSON{Key: "F-9", CreatedAt: now, UpdatedAt: now}}, leaf: true, bucket: "cancelled", status: "cancelled", hours: ptr(200), labels: map[string]any{}, eta: &columnETA{Progress: &pct}})
	out := &projectFigures{Scope: figureScope{Rows: len(rows)}, MarksReleases: true, Groups: map[string]*figureGroup{}, Columns: map[string]*columnFigure{}}
	for _, group := range []string{"completed", "done_not_live", "in_progress", "open"} {
		out.Groups[group] = &figureGroup{Statuses: map[string]int{}}
	}
	fillFigureColumns(out, rows, true, now)
	if out.TotalHours != 14 || out.Scope.Parents != 1 || out.Scope.Leaves != 8 || out.Scope.Closed != 1 || out.Columns["estimate"].Numbers.Total != 14 || *out.Columns["progress"].WeightedPercent != 50 || out.Columns["progress"].Included != 7 {
		t.Fatalf("leaf/closed policy: %+v", out)
	}
	if out.Columns["tokens"].Measured.Total != 700 || out.Columns["tokens"].Estimated.Count != 0 || !out.Columns["tokens"].Partial || out.Columns["tokens"].WithoutValue != 1 {
		t.Fatalf("token cohorts: %+v", out.Columns["tokens"])
	}
	if out.Columns["cost"].Measured.Total != 10.5 || out.Columns["cost"].Estimated.Count != 0 || !out.Columns["cost"].Partial {
		t.Fatalf("cost cohorts: %+v", out.Columns["cost"])
	}
	if *out.Columns["tokens"].Typical.Median != 100 || out.Columns["tokens"].Typical.Approximate || out.Columns["model"].Values[0].Value != "used-model" {
		t.Fatal("displayed typical or actual model differs from list")
	}
	if out.Columns["assignee"].Others != 4 || len(out.Columns["assignee"].Values) != 3 || out.Columns["assignee"].WithoutValue != 1 || out.Columns["tag"].Values[0].Count != 7 || *out.Columns["created_at"].Stale != 7 || *out.Columns["eta_ready"].Running != 7 {
		t.Fatal("top-N, tag deduplication or dates incorrect")
	}
	// Exercise estimated-only and zero measured values independently.
	rows[1].plan.Tokens.Spent = nil
	rows[1].plan.Cost.ListSpent = nil
	for i := 2; i < 8; i++ {
		rows[i].plan = nil
	}
	out = &projectFigures{Scope: figureScope{Rows: len(rows)}, MarksReleases: true, Groups: map[string]*figureGroup{}, Columns: map[string]*columnFigure{}}
	for _, group := range []string{"completed", "done_not_live", "in_progress", "open"} {
		out.Groups[group] = &figureGroup{Statuses: map[string]int{}}
	}
	fillFigureColumns(out, rows, false, now)
	if out.Columns["cost"] != nil || out.Columns["tokens"].Estimated.Total != 200 || out.Columns["tokens"].Measured.Count != 0 {
		t.Fatal("estimated-only or permission projection incorrect")
	}
	// Cache time uses supplied instants, so expiry and concurrent older
	// snapshot publication are proven without sleeps.
	cache := &figuresCache{}
	cache.put("scope", 2, now, out)
	cache.put("scope", 1, now, out)
	if cache.get("scope", 2, now) == nil || cache.get("scope", 2, now.Add(figuresMaxAge)) != nil {
		t.Fatal("cache expiry or revision ordering incorrect")
	}
}

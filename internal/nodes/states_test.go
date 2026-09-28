// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if n.State == "delivered" && (i == 0 || page.Items[i-1].State != "accepted") {
			t.Fatalf("delivered workflow order: %s", body)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package relations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fixture struct {
	db         *dbtest.DB
	handler    http.Handler
	a, b       tenant.Principal
	nodes      []string
	foreign    string
	eventStart int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.Open(t)
	f := fixture{db: d}
	for i, p := range []*tenant.Principal{&f.a, &f.b} {
		p.Kind = tenant.Agent
		p.Roles = []string{}
		if i == 0 {
			p.Kind = tenant.Person
			p.Roles = []string{"admin"}
		}
		err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Test') RETURNING id::text`, fmt.Sprint("tenant", i)).Scan(&p.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		err = db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,$2,'Test',$3) RETURNING id::text`, p.TenantID, p.Kind, p.Roles).Scan(&p.ID); err != nil {
				return err
			}
			for j := 1; j <= 3; j++ {
				var id string
				if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title)
     SELECT $1,$2,id,'Node' FROM node_kinds WHERE tenant_id=$1 AND slug='task' RETURNING id::text`, p.TenantID, fmt.Sprintf("TSK-%d", j)).Scan(&id); err != nil {
					return err
				}
				if i == 0 {
					f.nodes = append(f.nodes, id)
				} else {
					f.foreign = id
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	dbtest.BindLegacy(t, d, f.a.TenantID, f.a.ID)
	if err := d.Admin.QueryRow(t.Context(), `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, f.a.TenantID).Scan(&f.eventStart); err != nil {
		t.Fatal(err)
	}
	f.handler = (&httpapi.Server{Pool: d.App, Modules: []httpapi.Module{New(d.App), events.New(d.App, UndoOption())}}).Handler()
	return f
}

func request(h http.Handler, p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}

func create(t *testing.T, f fixture, source, target, typ string) Relation {
	t.Helper()
	w := request(f.handler, f.a, "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":%q}`, source, target, typ))
	expect(t, w, 201)
	var result Relation
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func logEvents(t *testing.T, f fixture) []events.Event {
	t.Helper()
	w := request(f.handler, f.a, "GET", fmt.Sprintf("/api/events?after=%d", f.eventStart), "")
	expect(t, w, 200)
	var result struct{ Items []events.Event }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Items
}

func undoPath(f fixture, offset int64) string {
	return fmt.Sprintf("/api/events/%d/undo", f.eventStart+offset)
}

func TestRelationsCRUDAndTenantIsolation(t *testing.T) {
	f := setup(t)
	first := create(t, f, strings.ToUpper(f.nodes[0]), f.nodes[1], "relates")
	if first.SourceNodeID > first.TargetNodeID {
		t.Fatal("symmetric endpoints were not normalized")
	}
	duplicate := request(f.handler, f.a, "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"relates"}`, f.nodes[1], f.nodes[0]))
	expect(t, duplicate, 409)
	for _, typ := range []string{"blocks", "implements", "cites", "duplicates"} {
		create(t, f, f.nodes[1], f.nodes[0], typ)
	}
	create(t, f, f.nodes[0], f.nodes[1], "cites") // Directed reverse pair is distinct.
	ev := logEvents(t, f)
	if len(ev) != 6 {
		t.Fatalf("events %d want 6", len(ev))
	}
	for i, e := range ev {
		if e.ID != f.eventStart+int64(i+1) || e.Type != "relation.created" || string(e.Before) != "null" || e.ActorPrincipalID != f.a.ID {
			t.Fatalf("bad event %+v", e)
		}
	}
	var snap Relation
	if err := json.Unmarshal(ev[0].After, &snap); err != nil || snap != first {
		t.Fatalf("snapshot mismatch: %+v / %+v", snap, first)
	}
	for _, node := range f.nodes[:2] {
		w := request(f.handler, f.a, "GET", "/api/relations?node_id="+node, "")
		expect(t, w, 200)
		var result page
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 6 || result.NextCursor != nil {
			t.Fatalf("bad page %+v", result)
		}
	}
	w := request(f.handler, f.b, "GET", "/api/relations?node_id="+f.nodes[0], "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("cross-tenant relations leaked")
	}
	w = request(f.handler, f.b, "GET", "/api/events?node_id="+f.nodes[0], "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("cross-tenant events leaked")
	}
	expect(t, request(f.handler, f.b, "DELETE", "/api/relations/"+first.ID, ""), 404)
	// The unprivileged foreign agent is denied before event lookup. A qualified
	// foreign person then proves tenant-isolated lookup (Undo is person-only).
	expect(t, request(f.handler, f.b, "POST", undoPath(f, 1), ""), 403)
	foreignPerson := tenant.Principal{TenantID: f.b.TenantID, Kind: tenant.Person}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign undo reader') RETURNING id::text`, foreignPerson.TenantID).Scan(&foreignPerson.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, foreignPerson.TenantID, foreignPerson.ID, "member")
	expect(t, request(f.handler, foreignPerson, "POST", undoPath(f, 1), ""), 404)
	expect(t, request(f.handler, f.a, "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"blocks"}`, f.nodes[0], f.foreign)), 404)
	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+first.ID, ""), 204)
	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+first.ID, ""), 404)
	ev = logEvents(t, f)
	if len(ev) != 7 || ev[6].Type != "relation.deleted" || string(ev[6].After) != "null" || !bytes.Equal(ev[0].After, ev[6].Before) {
		t.Fatalf("delete event %+v", ev)
	}
}

func refused(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	expect(t, w, status)
	var body struct{ Code, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Message != message {
		t.Fatalf("message %q want %q", body.Message, message)
	}
}

func post(f fixture, source, target, typ string) *httptest.ResponseRecorder {
	return request(f.handler, f.a, "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":%q}`, source, target, typ))
}

// TestRefusalsReadAsWords covers the reasons the relation picker shows as they
// stand: an existing link, a loop of any length, and a link to itself.
func TestRefusalsReadAsWords(t *testing.T) {
	f := setup(t)
	a, b, c := f.nodes[0], f.nodes[1], f.nodes[2]
	create(t, f, a, b, "blocks")
	refused(t, post(f, a, b, "blocks"), 409, "TSK-1 already blocks TSK-2.")
	refused(t, post(f, b, a, "blocks"), 409, "TSK-2 cannot block TSK-1: TSK-1 already blocks TSK-2, so this link would make a loop.")
	create(t, f, b, c, "blocks")
	refused(t, post(f, c, a, "blocks"), 409, "TSK-3 cannot block TSK-1: TSK-1 blocks TSK-2, which blocks TSK-3, so this link would make a loop.")
	// Other types keep their own graph: the reverse citation and a relates
	// link in either spelling stay allowed once.
	create(t, f, c, a, "cites")
	create(t, f, a, c, "cites")
	create(t, f, c, a, "relates")
	// relates is stored with the smaller UUID first, and the reason names it so.
	want := "TSK-1 already relates to TSK-3."
	if a > c {
		want = "TSK-3 already relates to TSK-1."
	}
	refused(t, post(f, a, c, "relates"), 409, want)
	create(t, f, a, b, "implements")
	refused(t, post(f, b, a, "implements"), 409, "TSK-2 cannot implement TSK-1: TSK-1 already implements TSK-2, so this link would make a loop.")
	create(t, f, b, a, "duplicates")
	refused(t, post(f, a, b, "duplicates"), 409, "TSK-1 cannot duplicate TSK-2: TSK-2 already duplicates TSK-1, so this link would make a loop.")
	refused(t, post(f, a, a, "blocks"), 400, "an item cannot be linked to itself")
	// A deleted item breaks the chain; the loop is no longer there.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	create(t, f, c, a, "blocks")
	// Undo cannot restore a link that a later one turned into a loop.
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var first Relation
	w := request(f.handler, f.a, "GET", "/api/relations?node_id="+a+"&limit=200", "")
	expect(t, w, 200)
	var list page
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, r := range list.Items {
		if r.Type == "blocks" && r.SourceNodeID == a {
			first = r
		}
	}
	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+first.ID, ""), 204)
	create(t, f, b, a, "blocks") // Now C blocks A and B blocks A: no loop.
	events := logEvents(t, f)
	last := events[len(events)-2] // The deletion of A blocks B.
	if last.Type != "relation.deleted" {
		t.Fatalf("event %+v", last)
	}
	expect(t, request(f.handler, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", last.ID), ""), 409)
}

func TestPaginationValidationAndAtomicity(t *testing.T) {
	f := setup(t)
	for _, typ := range []string{"blocks", "cites", "relates"} {
		create(t, f, f.nodes[0], f.nodes[1], typ)
	}
	path := "/api/relations?node_id=" + f.nodes[0] + "&limit=1"
	seen := map[string]bool{}
	for {
		w := request(f.handler, f.a, "GET", path, "")
		expect(t, w, 200)
		var result page
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 1 || seen[result.Items[0].ID] {
			t.Fatalf("bad page %+v", result)
		}
		seen[result.Items[0].ID] = true
		if result.NextCursor == nil {
			break
		}
		expect(t, request(f.handler, f.a, "GET", "/api/relations?node_id="+f.nodes[2]+"&cursor="+*result.NextCursor, ""), 400)
		expect(t, request(f.handler, f.b, "GET", "/api/relations?node_id="+f.nodes[0]+"&cursor="+*result.NextCursor, ""), 400)
		path = "/api/relations?node_id=" + f.nodes[0] + "&limit=1&cursor=" + *result.NextCursor
	}
	if len(seen) != 3 {
		t.Fatal(seen)
	}
	for _, path := range []string{"/api/relations", "/api/relations?node_id=wrong", "/api/relations?node_id=" + f.nodes[0] + "&limit=201", "/api/relations?node_id=" + f.nodes[0] + "&cursor=nope", "/api/events?after=-1", "/api/events?after=9223372036854775808", "/api/events?limit=0", "/api/events?node_id=wrong", "/api/events?after=", "/api/events?node_id="} {
		expect(t, request(f.handler, f.a, "GET", path, ""), 400)
	}
	for _, body := range []string{"null", "[]", "{}", `{"source_node_id":1}`, `{} {}`, fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"blocks"}`, f.nodes[0], f.nodes[0]), fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"unknown"}`, f.nodes[0], f.nodes[1])} {
		expect(t, request(f.handler, f.a, "POST", "/api/relations", body), 400)
	}
	for _, route := range []struct{ method, path string }{{"GET", "/api/relations"}, {"POST", "/api/relations"}, {"DELETE", "/api/relations/id"}, {"GET", "/api/events"}, {"GET", "/api/events/stream"}, {"POST", undoPath(f, 1)}} {
		expect(t, request(f.handler, tenant.Principal{}, route.method, route.path, ""), 401)
	}
	w := request(f.handler, f.a, "GET", fmt.Sprintf("/api/events?after=%d&limit=2", f.eventStart), "")
	expect(t, w, 200)
	var ep struct {
		Items []events.Event `json:"items"`
		Next  *int64         `json:"next_after"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ep); err != nil {
		t.Fatal(err)
	}
	if len(ep.Items) != 2 || ep.Next == nil || *ep.Next != f.eventStart+2 {
		t.Fatalf("bad event page %+v", ep)
	}
	w = request(f.handler, f.a, "GET", fmt.Sprintf("/api/events?after=%d&limit=2", *ep.Next), "")
	expect(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &ep); err != nil {
		t.Fatal(err)
	}
	if len(ep.Items) != 1 || ep.Items[0].ID != f.eventStart+3 || ep.Next != nil {
		t.Fatalf("bad last page %+v", ep)
	}
	w = request(f.handler, f.a, "GET", "/api/events?node_id="+f.nodes[2], "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal(w.Body.String())
	}
	// A valid tenant with an invalid actor makes event insertion fail. The link
	// mutation must roll back, leaving its tuple available for the real caller.
	// Since ADR-003 P2 such an actor also sees no project, so the link is
	// refused before the event; either way nothing is written.
	forged := f.a
	forged.ID = f.b.ID
	body := fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"duplicates"}`, f.nodes[0], f.nodes[1])
	if w := request(f.handler, forged, "POST", "/api/relations", body); w.Code != 500 && w.Code != 404 {
		t.Fatalf("forged actor: %d %s", w.Code, w.Body.String())
	}
	create(t, f, f.nodes[0], f.nodes[1], "duplicates")
	if len(logEvents(t, f)) != 4 {
		t.Fatal("failed writes left history")
	}
	// Deleting a node prevents new relations without consuming an event ID.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, f.nodes[2])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.handler, f.a, "POST", "/api/relations", fmt.Sprintf(`{"source_node_id":%q,"target_node_id":%q,"type":"blocks"}`, f.nodes[0], f.nodes[2])), 404)
}

func TestUndoAuthorizationRestorationAndConflicts(t *testing.T) {
	f := setup(t)
	first := create(t, f, f.nodes[0], f.nodes[1], "blocks")
	other := f.a
	other.Kind = tenant.Agent
	other.Roles = nil
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Other') RETURNING id::text`, f.a.TenantID).Scan(&other.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	// The agent sees the event (ADR-003 P2) but may not undo another's change.
	dbtest.BindRole(t, f.db, f.a.TenantID, other.ID, "member")
	expect(t, request(f.handler, other, "POST", undoPath(f, 1), ""), 403)
	other.Roles = []string{"admin"}
	expect(t, request(f.handler, other, "POST", undoPath(f, 1), ""), 403)
	other.Kind = tenant.Person
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Decider') RETURNING id::text`, f.a.TenantID).Scan(&other.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='admin'`, f.a.TenantID, other.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.handler, other, "POST", undoPath(f, 1), ""), 201)
	ev := logEvents(t, f)
	if len(ev) != 2 || ev[1].UndoOf == nil || *ev[1].UndoOf != ev[0].ID || ev[1].ActorPrincipalID != other.ID || string(ev[1].After) != "null" || !bytes.Equal(ev[1].Before, ev[0].After) {
		t.Fatalf("bad compensation %+v", ev)
	}
	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+first.ID, ""), 404)
	expect(t, request(f.handler, f.a, "POST", undoPath(f, 1), ""), 409)
	expect(t, request(f.handler, other, "POST", undoPath(f, 2), ""), 409)
	second := create(t, f, f.nodes[0], f.nodes[1], "blocks")
	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+second.ID, ""), 204)
	expect(t, request(f.handler, f.a, "POST", undoPath(f, 3), ""), 409) // Already deleted.
	expect(t, request(f.handler, f.a, "POST", undoPath(f, 4), ""), 201)
	w := request(f.handler, f.a, "GET", "/api/relations?node_id="+f.nodes[0], "")
	expect(t, w, 200)
	var result page
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0] != second {
		t.Fatal("undo did not restore exact snapshot")
	}
	expect(t, request(f.handler, f.a, "POST", undoPath(f, 4), ""), 409)
	expect(t, request(f.handler, f.a, "POST", undoPath(f, 999), ""), 404)
	// Once a different relation occupies the original tuple, restoration conflicts.
	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+second.ID, ""), 204)
	third := create(t, f, f.nodes[0], f.nodes[1], "blocks")
	expect(t, request(f.handler, f.a, "POST", undoPath(f, 6), ""), 409)
	expect(t, request(f.handler, f.a, "DELETE", "/api/relations/"+third.ID, ""), 204)
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, f.nodes[1])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.handler, f.a, "POST", undoPath(f, 8), ""), 409)
	if len(logEvents(t, f)) != 8 {
		t.Fatal("failed undo appended an event")
	}
}

func TestConcurrentUndoExactlyOnce(t *testing.T) {
	f := setup(t)
	create(t, f, f.nodes[0], f.nodes[1], "cites")
	statuses := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { <-start; statuses <- request(f.handler, f.a, "POST", undoPath(f, 1), "").Code })
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[201] != 1 || counts[409] != 1 || len(logEvents(t, f)) != 2 {
		t.Fatalf("concurrent undo: %v", counts)
	}
}

func TestRejectMalformedUUIDSeparators(t *testing.T) {
	for _, s := range []string{"", "123", "00000000x0000x0000x0000x000000000001", "00000000-0000-0000-0000-00000000000g"} {
		if _, ok := uuid(s); ok {
			t.Fatalf("accepted invalid UUID %q", s)
		}
	}
	if got, ok := uuid("AAAAAAAA-1234-5678-9012-123456789012"); !ok || got != "aaaaaaaa-1234-5678-9012-123456789012" {
		t.Fatal("UUID was not canonicalized")
	}
}

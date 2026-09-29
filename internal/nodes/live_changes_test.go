// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-326: node events carry the node, its project, the changed attribute
// names and the new revision, and a reader who sees one project only reads
// the changes of that project.
func TestNodeChangesNameProjectFieldsAndRevisionForVisibleNodesOnly(t *testing.T) {
	owner := newPrincipal(t, "live-node-changes")
	project := kindBySlug(t, owner, "project")
	ticket := kindBySlug(t, owner, "ticket")
	seen := mustNode(t, owner, `{"kind_id":"`+project.ID+`","title":"Seen"}`)
	hidden := mustNode(t, owner, `{"kind_id":"`+project.ID+`","title":"Hidden"}`)
	open := mustNode(t, owner, `{"kind_id":"`+ticket.ID+`","parent_id":"`+seen.ID+`","title":"Open","state":"new","fields":{"priority":"low"}}`)
	secret := mustNode(t, owner, `{"kind_id":"`+ticket.ID+`","parent_id":"`+hidden.ID+`","title":"Secret","state":"new"}`)

	guest := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person, Name: "Guest"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, owner.TenantID).Scan(&guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, owner.TenantID, guest.ID, seen.ID); err != nil {
		t.Fatal(err)
	}
	var mark int64
	if err := testDB.Admin.QueryRow(t.Context(), `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, owner.TenantID).Scan(&mark); err != nil {
		t.Fatal(err)
	}

	status, raw := call(t, &owner, http.MethodPatch, "/api/nodes/"+open.ID, `{"title":"Open, renamed","fields":{"priority":"high","notes":"x"}}`)
	updated := decode[nodeJSON](t, status, raw, http.StatusOK)
	if status, raw := call(t, &owner, http.MethodPatch, "/api/nodes/"+secret.ID, `{"title":"Secret, renamed"}`); status != http.StatusOK {
		t.Fatalf("hidden update: %d %s", status, raw)
	}
	if status, raw := call(t, &owner, http.MethodDelete, "/api/nodes/"+open.ID, ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}

	read := func(p tenant.Principal) []events.Event {
		status, raw := callAs(t, events.New(appPool), &p, http.MethodGet, "/api/events?after="+strconv.FormatInt(mark, 10), "")
		return decode[struct {
			Items []events.Event `json:"items"`
		}](t, status, raw, http.StatusOK).Items
	}
	changes := func(items []events.Event) []events.NodeChange {
		var out []events.NodeChange
		for _, e := range items {
			out = append(out, e.NodeChanges...)
		}
		return out
	}

	got := changes(read(guest))
	if len(got) != 2 {
		t.Fatalf("guest reads %d node changes, want 2: %+v", len(got), got)
	}
	first := got[0]
	if first.ID != open.ID || first.ProjectID == nil || *first.ProjectID != seen.ID || first.Change != "updated" ||
		!slices.Equal(first.Fields, []string{"title", "fields.notes", "fields.priority"}) {
		t.Fatalf("update summary %+v", first)
	}
	if first.Revision == nil || *first.Revision != updated.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00") {
		t.Fatalf("revision %v, want the node's updated_at %v", first.Revision, updated.UpdatedAt)
	}
	if second := got[1]; second.ID != open.ID || second.Change != "deleted" || second.ProjectID == nil || *second.ProjectID != seen.ID {
		t.Fatalf("delete summary %+v", second)
	}
	for _, c := range got {
		if c.ID == secret.ID {
			t.Fatal("guest read a change of a project it cannot see")
		}
	}

	all := changes(read(owner))
	if len(all) != 3 || all[1].ID != secret.ID || all[1].ProjectID == nil || *all[1].ProjectID != hidden.ID {
		t.Fatalf("owner reads %+v", all)
	}
}

// AEON-326: a delete answers the revision its node.deleted event carries,
// so the tab that deleted knows exactly that event as its own, and the
// deletion is newer than the update before it.
func TestDeleteAnswersTheRevisionOfItsEvent(t *testing.T) {
	owner := newPrincipal(t, "live-delete-revision")
	project := kindBySlug(t, owner, "project")
	ticket := kindBySlug(t, owner, "ticket")
	parent := mustNode(t, owner, `{"kind_id":"`+project.ID+`","title":"Project"}`)
	node := mustNode(t, owner, `{"kind_id":"`+ticket.ID+`","parent_id":"`+parent.ID+`","title":"Doomed","state":"new"}`)
	status, raw := call(t, &owner, http.MethodPatch, "/api/nodes/"+node.ID, `{"title":"Doomed, renamed"}`)
	updated := decode[nodeJSON](t, status, raw, http.StatusOK)
	var mark int64
	if err := testDB.Admin.QueryRow(t.Context(), `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, owner.TenantID).Scan(&mark); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	New(appPool, nil).Mount(mux)
	r := httptest.NewRequest(http.MethodDelete, "/api/nodes/"+node.ID, nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), owner))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	revision, err := time.Parse(time.RFC3339Nano, rec.Header().Get("Aeon-Revision"))
	if err != nil {
		t.Fatalf("Aeon-Revision %q: %v", rec.Header().Get("Aeon-Revision"), err)
	}
	if !revision.After(updated.UpdatedAt) {
		t.Fatalf("delete revision %v is not after the update %v", revision, updated.UpdatedAt)
	}

	status, raw = callAs(t, events.New(appPool), &owner, http.MethodGet, "/api/events?after="+strconv.FormatInt(mark, 10), "")
	page := decode[struct {
		Items []events.Event `json:"items"`
	}](t, status, raw, http.StatusOK)
	if len(page.Items) != 1 || len(page.Items[0].NodeChanges) != 1 {
		t.Fatalf("events after the update: %+v", page.Items)
	}
	change := page.Items[0].NodeChanges[0]
	if change.Change != "deleted" || change.Revision == nil {
		t.Fatalf("delete summary %+v", change)
	}
	if evented, err := time.Parse(time.RFC3339Nano, *change.Revision); err != nil || !evented.Equal(revision) {
		t.Fatalf("event revision %v, header %v", *change.Revision, revision)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

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

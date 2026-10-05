// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Undo needs what the reversing change needs (review finding 1): a
// principal who may undo other people's changes but not edit nodes cannot
// reverse a batch edit by undoing it.
func TestBulkUndoNeedsTheEditPermission(t *testing.T) {
	owner := newPrincipal(t, "bulk-undo-scope")
	project := kindBySlug(t, owner, "project")
	ticket := kindBySlug(t, owner, "work")
	root := mustNode(t, owner, `{"kind_id":"`+project.ID+`","title":"Undo project"}`)
	item := mustNode(t, owner, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"Item","state":"new","fields":`+benefitFields+`}`)
	status, raw := call(t, &owner, http.MethodPost, "/api/nodes/bulk", `{"ids":["`+item.ID+`"],"state":"done"}`)
	batch := decode[bulkResult](t, status, raw, http.StatusOK)

	undoer := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person, Name: "Undoer"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Undoer') RETURNING id::text`, owner.TenantID).Scan(&undoer.ID); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'undo_only','Undo only') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','events.read','events.undo','events.undo_other'])`, owner.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, undoer.ID, role); err != nil {
		t.Fatal(err)
	}
	undo := func(p tenant.Principal) int {
		status, _ := callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers())), &p, http.MethodPost, "/api/events/"+strconv.FormatInt(*batch.EventID, 10)+"/undo", "")
		return status
	}
	if status := undo(undoer); status != http.StatusForbidden {
		t.Fatalf("undo without nodes.write: %d", status)
	}
	if status := undo(owner); status != http.StatusCreated {
		t.Fatalf("owner undo: %d", status)
	}
}

// Review round 2, finding 3: a nested project whose parent the caller cannot
// see may not be detached from it. A custom workspace role with nodes.move
// but not nodes.read, plus admin of the nested project, used to pass with the
// hidden parent's side decided in the workspace.
func TestMoveOutOfAnUnseenParentIsRefused(t *testing.T) {
	owner := newPrincipal(t, "unseen-parent")
	project := kindBySlug(t, owner, "project")
	outer := mustNode(t, owner, `{"kind_id":"`+project.ID+`","title":"Outer"}`)
	inner := mustNode(t, owner, `{"kind_id":"`+project.ID+`","parent_id":"`+outer.ID+`","title":"Inner"}`)

	mover := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person, Name: "Mover"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Mover') RETURNING id::text`, owner.TenantID).Scan(&mover.ID); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'move_only','Move only') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.move')`, owner.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, mover.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, owner.TenantID, mover.ID, inner.ID); err != nil {
		t.Fatal(err)
	}
	if status, raw := call(t, &mover, http.MethodPost, "/api/nodes/"+inner.ID+"/move", `{"parent_id":null}`); status != http.StatusForbidden {
		t.Fatalf("detached from an unseen parent: %d %s", status, raw)
	}
	var parent *string
	if err := testDB.Admin.QueryRow(t.Context(), `SELECT parent_id::text FROM nodes WHERE id=$1`, inner.ID).Scan(&parent); err != nil || parent == nil || *parent != outer.ID {
		t.Fatalf("inner project parent now %v (%v)", parent, err)
	}
	// The owner, who sees both, may.
	if status, raw := call(t, &owner, http.MethodPost, "/api/nodes/"+inner.ID+"/move", `{"parent_id":null}`); status != http.StatusOK {
		t.Fatalf("owner move: %d %s", status, raw)
	}
}

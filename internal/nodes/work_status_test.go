// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func enableWorkStatusTest(t *testing.T, p tenant.Principal) {
	t.Helper()
	dbtest.EnableWorkParentStatus(t, testDB, p.TenantID)
}
func workNodeTest(t *testing.T, p tenant.Principal, parent string, state string) nodeJSON {
	t.Helper()
	k := kindBySlug(t, p, "work")
	body := fmt.Sprintf(`{"kind_id":%q,"title":"Work child","state":%q,"fields":%s}`, k.ID, state, benefitFields)
	if parent != "" {
		body = strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"parent_id":%q}`, parent)
	}
	return mustNode(t, p, body)
}
func currentWorkTest(t *testing.T, p tenant.Principal, id string) nodeJSON {
	t.Helper()
	status, body := call(t, &p, "GET", "/api/nodes/"+id, "")
	return decode[nodeJSON](t, status, body, 200)
}
func lastDerivedTest(t *testing.T, p tenant.Principal, nodeID string) (int64, int64) {
	t.Helper()
	var id, cause int64
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id,(metadata->>'cause_event_id')::bigint FROM events WHERE node_id=$1 AND type='status_autopilot.derived' ORDER BY id DESC LIMIT 1`, nodeID).Scan(&id, &cause)
	}); err != nil {
		t.Fatal(err)
	}
	return id, cause
}
func causalCallTest(t *testing.T, p tenant.Principal, method string, id int64, body string) (int, []byte) {
	t.Helper()
	suffix := "undo"
	if method == "GET" {
		suffix = "undo-preview"
	}
	return callAs(t, events.New(appPool, events.WithUndoHandlers(UndoHandlers()), events.WithCausalUndoHandlers(CausalUndoHandlers())), &p, method, fmt.Sprintf("/api/events/%d/%s", id, suffix), body)
}
func patchWorkTest(t *testing.T, p tenant.Principal, id, state string) {
	t.Helper()
	status, body := call(t, &p, "PATCH", "/api/nodes/"+id, fmt.Sprintf(`{"state":%q}`, state))
	if status != 200 {
		t.Fatalf("patch %d %s", status, body)
	}
}
func TestWorkParentStatusCausalUndo(t *testing.T) {
	p := newPrincipal(t, "work-causal")
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project"}`)
	top := workNodeTest(t, p, root.ID, "open")
	parent := workNodeTest(t, p, top.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	if currentWorkTest(t, p, parent.ID).State != "in_progress" || currentWorkTest(t, p, top.ID).State != "in_progress" {
		t.Fatal("cascade not derived")
	}
	id, cause := lastDerivedTest(t, p, parent.ID)
	other, _ := lastDerivedTest(t, p, top.ID)
	status, raw := causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("preview %d %s", status, raw)
	}
	var preview struct {
		Cause int64 `json:"cause_event_id"`
		Nodes []struct {
			ID string `json:"id"`
			To string `json:"to"`
		} `json:"affected_nodes"`
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Cause != cause || len(preview.Nodes) != 1 || preview.Nodes[0].ID != child.ID || preview.Nodes[0].To != "open" {
		t.Fatalf("wrong cause preview %s", raw)
	}
	if currentWorkTest(t, p, child.ID).State != "in_progress" {
		t.Fatal("preview committed a write")
	}
	status, raw = causalCallTest(t, p, "POST", id, "")
	if status != 409 {
		t.Fatalf("missing confirmation %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("undo %d %s", status, raw)
	}
	for _, n := range []nodeJSON{child, parent, top} {
		if currentWorkTest(t, p, n.ID).State != "open" {
			t.Fatal("causal undo did not rederive")
		}
	}
	status, raw = causalCallTest(t, p, "POST", other, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 409 {
		t.Fatalf("sibling parent repeated Undo %d %s", status, raw)
	}
	// A later unrelated child edit also invalidates the original preview.
	patchWorkTest(t, p, child.ID, "done")
	id, cause = lastDerivedTest(t, p, parent.ID)
	status, raw = call(t, &p, "PATCH", "/api/nodes/"+child.ID, `{"title":"Later edit"}`)
	if status != 200 {
		t.Fatalf("later edit %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 409 {
		t.Fatalf("stale preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 409 {
		t.Fatalf("stale confirmation %d %s", status, raw)
	}
	if currentWorkTest(t, p, child.ID).State != "done" {
		t.Fatal("stale undo changed child")
	}
}
func TestWorkParentStatusPatchAndBulkBoundary(t *testing.T) {
	p := newPrincipal(t, "work-boundary")
	parent := workNodeTest(t, p, "", "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	for _, state := range []string{"open", "done"} {
		status, raw := call(t, &p, "PATCH", "/api/nodes/"+parent.ID, fmt.Sprintf(`{"state":%q}`, state))
		if status != 409 || !strings.Contains(string(raw), "parent_status_derived") {
			t.Fatalf("parent boundary %d %s", status, raw)
		}
	}
	status, raw := call(t, &p, "POST", "/api/nodes/bulk", fmt.Sprintf(`{"ids":[%q,%q],"state":"in_progress"}`, parent.ID, child.ID))
	if status != 200 {
		t.Fatalf("bulk %d %s", status, raw)
	}
	var result bulkResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].ID != parent.ID || result.Skipped[0].Code != "parent_status_derived" || len(result.Items) != 1 {
		t.Fatalf("bulk must honestly skip parent %s", raw)
	}
	if currentWorkTest(t, p, parent.ID).State != "in_progress" {
		t.Fatal("leaf bulk did not derive parent")
	}
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("bulk preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("bulk causal undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, parent.ID).State != "open" {
		t.Fatal("bulk undo did not derive parent")
	}
}
func TestWorkParentStatusDeleteCausalUndo(t *testing.T) {
	p := newPrincipal(t, "work-delete-undo")
	parent := workNodeTest(t, p, "", "open")
	_ = workNodeTest(t, p, parent.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	status, raw := call(t, &p, "DELETE", "/api/nodes/"+child.ID, "")
	if status != 204 {
		t.Fatalf("delete %d %s", status, raw)
	}
	if currentWorkTest(t, p, parent.ID).State != "open" {
		t.Fatal("delete did not derive")
	}
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("delete preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("delete undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, child.ID).State != "in_progress" || currentWorkTest(t, p, parent.ID).State != "in_progress" {
		t.Fatal("restore did not derive")
	}
}

func TestWorkParentStatusMoveAndCreateCausalUndo(t *testing.T) {
	p := newPrincipal(t, "work-move-undo")
	project := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project"}`)
	source := workNodeTest(t, p, project.ID, "open")
	target := workNodeTest(t, p, project.ID, "open")
	_ = workNodeTest(t, p, source.ID, "open")
	_ = workNodeTest(t, p, target.ID, "open")
	child := workNodeTest(t, p, source.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	status, raw := call(t, &p, "POST", "/api/nodes/"+child.ID+"/move", fmt.Sprintf(`{"parent_id":%q}`, target.ID))
	if status != 200 {
		t.Fatalf("move %d %s", status, raw)
	}
	if currentWorkTest(t, p, source.ID).State != "open" || currentWorkTest(t, p, target.ID).State != "in_progress" {
		t.Fatal("move missed ancestor chain")
	}
	id, cause := lastDerivedTest(t, p, target.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("move preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("move undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, source.ID).State != "in_progress" || currentWorkTest(t, p, target.ID).State != "open" {
		t.Fatal("move undo missed ancestor chain")
	}
	created := workNodeTest(t, p, target.ID, "in_progress")
	id, cause = lastDerivedTest(t, p, target.ID)
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("create preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 201 {
		t.Fatalf("create undo %d %s", status, raw)
	}
	if currentWorkTest(t, p, target.ID).State != "open" {
		t.Fatal("create undo did not derive")
	}
	if status, raw = call(t, &p, "GET", "/api/nodes/"+created.ID, ""); status != 404 {
		t.Fatalf("created child remains %d %s", status, raw)
	}
}

func TestWorkParentStatusHiddenCauseAndPermissionRevocation(t *testing.T) {
	p := newPrincipal(t, "work-hidden-undo")
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project"}`)
	parent := workNodeTest(t, p, root.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	visibleChild := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	id, cause := lastDerivedTest(t, p, parent.ID)
	status, raw := causalCallTest(t, p, "GET", id, "")
	if status != 200 {
		t.Fatalf("preview %d %s", status, raw)
	}
	// Restrict to a project role so event visibility follows node visibility.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
 SELECT $2,$1,id,'project',$3 FROM roles WHERE tenant_id=$2 AND key='admin'`, p.ID, p.TenantID, root.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `CREATE POLICY work_test_hidden ON nodes AS RESTRICTIVE USING(id<>'`+child.ID+`'::uuid OR aeon_visibility_system())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := appPool.Exec(context.Background(), `DROP POLICY IF EXISTS work_test_hidden ON nodes`); err != nil {
			t.Error(err)
		}
	})
	if status, raw = call(t, &p, "GET", "/api/nodes/"+child.ID, ""); status != 404 {
		t.Fatalf("child visibility %d %s", status, raw)
	}
	patchWorkTest(t, p, visibleChild.ID, "done")
	if currentWorkTest(t, p, parent.ID).State != "in_progress" {
		t.Fatal("parent lost canonical hidden-child result")
	}
	status, raw = callAs(t, events.New(appPool), &p, "GET", "/api/events", "")
	if status != 200 {
		t.Fatalf("history %d %s", status, raw)
	}
	var history struct {
		Items []events.Event `json:"items"`
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range history.Items {
		if e.ID == id {
			found = true
			if e.Derivation == nil || e.Derivation.CauseEventID != nil || len(e.Derivation.AffectedNodes) != 1 || e.Derivation.AffectedNodes[0] != parent.ID {
				t.Fatalf("unsafe derivation %+v", e.Derivation)
			}
		}
	}
	if !found {
		t.Fatal("visible parent event missing")
	}
	status, raw = causalCallTest(t, p, "GET", id, "")
	if status != 404 {
		t.Fatalf("hidden preview %d %s", status, raw)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 404 {
		t.Fatalf("hidden undo %d %s", status, raw)
	}
	if _, err := appPool.Exec(t.Context(), `DROP POLICY work_test_hidden ON nodes`); err != nil {
		t.Fatal(err)
	}

	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'causal_reader','Causal reader')`, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,permission FROM roles, unnest(ARRAY['nodes.read','events.read','events.undo','events.undo_other']) permission WHERE tenant_id=$1 AND key='causal_reader'`, p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='causal_reader') WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, raw = causalCallTest(t, p, "POST", id, fmt.Sprintf(`{"confirmed_cause_event_id":%d}`, cause))
	if status != 403 {
		t.Fatalf("revoked write %d %s", status, raw)
	}
}

func TestWorkParentStatusReusesAutopilotActivity(t *testing.T) {
	p := newPrincipal(t, "work-parent-activity")
	parent := workNodeTest(t, p, "", "open")
	child := workNodeTest(t, p, parent.ID, "open")
	enableWorkStatusTest(t, p)
	patchWorkTest(t, p, child.ID, "in_progress")
	id, _ := lastDerivedTest(t, p, parent.ID)
	status, raw := callAs(t, activity.New(appPool), &p, "GET", "/api/nodes/"+parent.ID+"/activity", "")
	if status != 200 {
		t.Fatalf("activity %d %s", status, raw)
	}
	var history activity.Page
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range history.Items {
		if c := item.AutomaticChange; c != nil && c.EventID == id {
			found = true
			if c.Rule != "work_parent" || c.From != "open" || c.To != "in_progress" || c.Reason == "" || !c.RequiresPreview || c.Undoable || !item.Author.Automatic || item.Author.Job != statusautopilot.Job {
				t.Fatalf("derived audit %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("derived status missing from autopilot Activity")
	}
}

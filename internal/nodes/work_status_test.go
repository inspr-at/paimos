// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func enableWorkStatusTest(t *testing.T, p tenant.Principal) {
	t.Helper()
	if _, err := appPool.Exec(t.Context(), `DO $$ BEGIN IF to_regclass('public.features') IS NULL THEN
 CREATE TABLE features(tenant_id uuid NOT NULL REFERENCES tenants(id),key text NOT NULL,project_id uuid,enabled boolean,
 revision bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),UNIQUE NULLS NOT DISTINCT(tenant_id,key,project_id),FOREIGN KEY(tenant_id,project_id) REFERENCES nodes(tenant_id,id));
 ALTER TABLE features ENABLE ROW LEVEL SECURITY; ALTER TABLE features FORCE ROW LEVEL SECURITY;
 CREATE POLICY fixture_features_tenant ON features USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
 END IF;END $$`); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO features(tenant_id,key,enabled) VALUES($1,'work-parent-status',true)`, p.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
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
func causalModuleTest() http.Handler {
	mux := http.NewServeMux()
	events.New(appPool, events.WithUndoHandlers(UndoHandlers()), events.WithCausalUndoHandlers(CausalUndoHandlers())).Mount(mux)
	return mux
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

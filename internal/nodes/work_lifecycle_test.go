// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func lifecyclePreview(t *testing.T, p tenant.Principal, id string) workPreview {
	t.Helper()
	status, raw := call(t, &p, "GET", "/api/nodes/"+id+"/work-lifecycle", "")
	return decode[workPreview](t, status, raw, 200)
}
func lifecycleRequest(t *testing.T, p tenant.Principal, id, kind string, children []splitChild) (string, int, []byte) {
	t.Helper()
	v := lifecyclePreview(t, p, id)
	var request string
	if err := appPool.QueryRow(t.Context(), `SELECT gen_random_uuid()::text`).Scan(&request); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(workRequest{RequestID: request, Kind: kind, UpdatedAt: v.UpdatedAt, OpenLeaves: v.OpenLeaves, ScopeRevision: v.ScopeRevision, Children: children})
	status, body := call(t, &p, "POST", "/api/nodes/"+id+"/work-lifecycle", string(raw))
	return request, status, body
}
func lifecycleSession(t *testing.T, p tenant.Principal, project, node string, stopped bool) string {
	t.Helper()
	var sid string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var agent string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Lifecycle worker') RETURNING id::text`, p.TenantID).Scan(&agent); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,phase,activity,owner_principal_id,stopped_at,stop_reason)
 VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',ARRAY['inbox','pause'],decode(replace(gen_random_uuid()::text,'-',''),'hex'),decode(replace(gen_random_uuid()::text,'-',''),'hex'),CASE WHEN $6 THEN 'stopped' ELSE 'working' END,'busy',$5,CASE WHEN $6 THEN clock_timestamp() END,CASE WHEN $6 THEN 'completed' END) RETURNING id::text`, p.TenantID, project, agent, node, p.ID, stopped).Scan(&sid)
	})
	if err != nil {
		t.Fatal(err)
	}
	return sid
}
func stopLifecycleSession(t *testing.T, p tenant.Principal, id string) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),stop_reason='completed',phase='stopped' WHERE id=$1`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestWorkLifecycleLeafGuardAndHistory(t *testing.T) {
	p := newPrincipal(t, "lifecycle-guards")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	leaf := workNodeTest(t, p, project.ID, "open")
	history := lifecycleSession(t, p, project.ID, leaf.ID, true)
	child := workNodeTest(t, p, leaf.ID, "open")
	// Historical bindings remain on the same former leaf; a new registration fails.
	var saved string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT ticket_node_id::text FROM harness_sessions WHERE id=$1`, history).Scan(&saved)
	}); err != nil {
		t.Fatal(err)
	}
	if saved != leaf.ID {
		t.Fatal("history rebound")
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT aeon_require_work_leaf($1::uuid)`, leaf.ID)
		return err
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "work_leaf_required" {
		t.Fatalf("wrong failure: %v", err)
	}
	lifecycleSession(t, p, project.ID, child.ID, false)
	status, raw := call(t, &p, "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Forbidden child","parent_id":%q}`, leaf.KindID, child.ID))
	if status != 409 || !containsBytes(raw, "busy_work_leaf") {
		t.Fatalf("busy add %d %s", status, raw)
	}
	status, raw = call(t, &p, "PATCH", "/api/nodes/"+child.ID, `{"state":"cancelled"}`)
	if status != 409 || !containsBytes(raw, "busy_work_leaf") {
		t.Fatalf("busy cancel %d %s", status, raw)
	}
	// Non-work children do not change leaf shape, even while busy.
	_ = mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Notes","parent_id":%q}`, kindBySlug(t, p, "memory").ID, child.ID))
	if !lifecyclePreview(t, p, child.ID).IsLeaf {
		t.Fatal("non-work child changed leaf shape")
	}
}
func containsBytes(b []byte, s string) bool { return strings.Contains(string(b), s) }
func TestWorkLifecycleSplitWaitsAndReplays(t *testing.T) {
	p := newPrincipal(t, "lifecycle-split")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	leaf := workNodeTest(t, p, project.ID, "open")
	sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
	request, status, raw := lifecycleRequest(t, p, leaf.ID, "split", []splitChild{{Title: "First"}, {Title: "Second"}})
	a := decode[workAction](t, status, raw, 200)
	if a.State != "waiting" || a.WaitingCount != 1 || len(a.Result) != 0 {
		t.Fatalf("premature split: %s", raw)
	}
	var stopped bool
	var level, reason string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT stopped_at IS NOT NULL,pause_record->>'level',pause_record->>'reason' FROM harness_sessions WHERE id=$1`, sid).Scan(&stopped, &level, &reason)
	}); err != nil {
		t.Fatal(err)
	}
	if stopped || level != "wrap_up" || reason != "work_lifecycle:"+request {
		t.Fatal("handover invented a stop or used a force control")
	}
	path := "/api/nodes/" + leaf.ID + "/work-lifecycle/" + request + "/continue"
	status, raw = call(t, &p, "POST", path, "")
	a = decode[workAction](t, status, raw, 200)
	if a.State != "waiting" {
		t.Fatal("silence treated as stopped")
	}
	stopLifecycleSession(t, p, sid)
	status, raw = call(t, &p, "POST", path, "")
	a = decode[workAction](t, status, raw, 200)
	if a.State != "completed" || len(a.Result) != 2 || a.WaitingCount != 0 {
		t.Fatalf("split not completed %s", raw)
	}
	status, raw = call(t, &p, "POST", path, "")
	again := decode[workAction](t, status, raw, 200)
	if len(again.Result) != 2 || again.Result[0] != a.Result[0] {
		t.Fatal("replay changed children")
	}
	if lifecyclePreview(t, p, leaf.ID).IsLeaf {
		t.Fatal("split is still leaf")
	}
}
func TestWorkLifecycleCancelOpenLeavesAfterStop(t *testing.T) {
	p := newPrincipal(t, "lifecycle-cancel")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	parent := workNodeTest(t, p, project.ID, "open")
	idle := workNodeTest(t, p, parent.ID, "open")
	busy := workNodeTest(t, p, parent.ID, "in_progress")
	done := workNodeTest(t, p, parent.ID, "done")
	sid := lifecycleSession(t, p, project.ID, busy.ID, false)
	enableWorkStatusTest(t, p)
	request, status, raw := lifecycleRequest(t, p, parent.ID, "cancel", nil)
	a := decode[workAction](t, status, raw, 200)
	if a.State != "waiting" || a.TargetCount != 2 || a.WaitingCount != 1 || len(a.Result) != 1 {
		t.Fatalf("wrong partial cancellation %s", raw)
	}
	if currentWorkTest(t, p, idle.ID).State != "cancelled" || currentWorkTest(t, p, busy.ID).State != "in_progress" || currentWorkTest(t, p, done.ID).State != "done" {
		t.Fatal("cancel changed a running or finished leaf")
	}
	stopLifecycleSession(t, p, sid)
	path := "/api/nodes/" + parent.ID + "/work-lifecycle/" + request + "/continue"
	status, raw = call(t, &p, "POST", path, "")
	a = decode[workAction](t, status, raw, 200)
	if a.State != "completed" || len(a.Result) != 2 {
		t.Fatalf("cancel completion %s", raw)
	}
	if currentWorkTest(t, p, busy.ID).State != "cancelled" || currentWorkTest(t, p, parent.ID).State != "done" {
		t.Fatal("derived parent or cancellation incorrect")
	}
	status, raw = call(t, &p, "POST", path, "")
	decode[workAction](t, status, raw, 200)
	var count int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='node.updated' AND after->>'state'='cancelled'`, busy.ID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("cancellation repeated %d times", count)
	}
}
func TestWorkLifecycleStalePreviewAndAbandon(t *testing.T) {
	p := newPrincipal(t, "lifecycle-stale")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	leaf := workNodeTest(t, p, project.ID, "open")
	sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
	request, status, raw := lifecycleRequest(t, p, leaf.ID, "split", []splitChild{{Title: "Child"}})
	decode[workAction](t, status, raw, 200)
	status, raw = call(t, &p, "PATCH", "/api/nodes/"+leaf.ID, `{"title":"Changed while handing over"}`)
	decode[nodeJSON](t, status, raw, 200)
	stopLifecycleSession(t, p, sid)
	status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
	if status != 409 || !containsBytes(raw, "changed during handover") {
		t.Fatalf("wrong stale split failure %d %s", status, raw)
	}
	status, raw = call(t, &p, "DELETE", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request, "")
	a := decode[workAction](t, status, raw, 200)
	if a.State != "abandoned" {
		t.Fatal("intent not released")
	}
	_, status, raw = lifecycleRequest(t, p, leaf.ID, "split", []splitChild{{Title: "Revised child"}})
	a = decode[workAction](t, status, raw, 200)
	if a.State != "completed" {
		t.Fatalf("new split failed %s", raw)
	}
}

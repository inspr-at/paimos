// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
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
	blocked := workNodeTest(t, p, parent.ID, "blocked")
	busy := workNodeTest(t, p, parent.ID, "in_progress")
	done := workNodeTest(t, p, parent.ID, "done")
	sid := lifecycleSession(t, p, project.ID, busy.ID, false)
	enableWorkStatusTest(t, p)
	request, status, raw := lifecycleRequest(t, p, parent.ID, "cancel", nil)
	a := decode[workAction](t, status, raw, 200)
	if a.State != "waiting" || a.TargetCount != 3 || a.WaitingCount != 1 || len(a.Result) != 2 {
		t.Fatalf("wrong partial cancellation %s", raw)
	}
	if currentWorkTest(t, p, idle.ID).State != "cancelled" || currentWorkTest(t, p, blocked.ID).State != "cancelled" || currentWorkTest(t, p, busy.ID).State != "in_progress" || currentWorkTest(t, p, done.ID).State != "done" {
		t.Fatal("cancel changed a running or finished leaf")
	}
	stopLifecycleSession(t, p, sid)
	path := "/api/nodes/" + parent.ID + "/work-lifecycle/" + request + "/continue"
	status, raw = call(t, &p, "POST", path, "")
	a = decode[workAction](t, status, raw, 200)
	if a.State != "completed" || len(a.Result) != 3 {
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

func TestWorkLifecycleBackgroundCompletionAndRevocation(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprint(revoked), func(t *testing.T) {
			p := newPrincipal(t, "lifecycle-background")
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
			leaf := workNodeTest(t, p, project.ID, "open")
			sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
			request, status, raw := lifecycleRequest(t, p, leaf.ID, "split", []splitChild{{Title: "Saved child"}})
			a := decode[workAction](t, status, raw, 200)
			if a.State != "waiting" {
				t.Fatal("split bypassed live worker")
			}
			stopLifecycleSession(t, p, sid)
			if revoked {
				if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, p.ID); err != nil {
					t.Fatal(err)
				}
			}
			m := &Module{pool: appPool, events: SQLWriter{}}
			if err := m.sweepWorkLifecycleTenant(t.Context(), p.TenantID); err != nil {
				t.Fatal(err)
			}
			var state string
			var results []byte
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT state,result FROM work_lifecycle_actions WHERE id=$1`, request).Scan(&state, &results)
			}); err != nil {
				t.Fatal(err)
			}
			if revoked {
				if state != "waiting" || string(results) != "[]" {
					t.Fatal("background job bypassed revoked permission")
				}
			} else {
				if state != "completed" || string(results) == "[]" {
					t.Fatal("closing the sheet stranded the action")
				}
			}
		})
	}
}

func TestWorkLifecycleBindingSerializesChildCreation(t *testing.T) {
	for _, bindingFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(bindingFirst), func(t *testing.T) {
			p := newPrincipal(t, "lifecycle-race")
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
			leaf := workNodeTest(t, p, project.ID, "open")
			agent := ""
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Race worker') RETURNING id::text`, p.TenantID).Scan(&agent)
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 15*time.Second)
			defer cancel()
			held, err := appPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Rollback(context.Background())
			if err = enterLifecycleTenant(ctx, held, p); err != nil {
				t.Fatal(err)
			}
			if err = db.LockWorkTreeTx(ctx, held); err != nil {
				t.Fatal(err)
			}
			bind := func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(replace(gen_random_uuid()::text,'-',''),'hex'),decode(replace(gen_random_uuid()::text,'-',''),'hex'))`, p.TenantID, project.ID, agent, leaf.ID)
				return err
			}
			child := func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) VALUES($1,'RACE-1',$2,'Racing child',$3)`, p.TenantID, leaf.KindID, leaf.ID)
				return err
			}
			first, next := bind, child
			if !bindingFirst {
				first, next = child, bind
			}
			if err = first(held); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
					if err := db.LockWorkTreeTx(ctx, tx); err != nil {
						return err
					}
					return next(tx)
				})
			}()
			waitAccessMoveBlock(t, ctx, held.Conn().PgConn().PID(), done)
			if err = held.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			var pg *pgconn.PgError
			want := "busy_work_leaf"
			if !bindingFirst {
				want = "work_leaf_required"
			}
			if !errors.As(err, &pg) || pg.ConstraintName != want {
				t.Fatalf("wrong racing failure: %v", err)
			}
		})
	}
}
func enterLifecycleTenant(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	_, err := tx.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`, p.TenantID)
	return err
}

func TestWorkLifecycleWorkOrderAndDispatchBindings(t *testing.T) {
	p := newPrincipal(t, "lifecycle-order")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	leaf := workNodeTest(t, p, project.ID, "open")
	var order string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		o, err := workorders.Create(t.Context(), tx, p, workorders.CreateInput{Title: "Bound order", Parent: &leaf.ID, Criteria: []string{"Done"}})
		order = o.NodeID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = workNodeTest(t, p, leaf.ID, "open")
	// A non-running order may remain as history. Dispatch/re-ready cannot revive
	// that historical binding after the former leaf becomes a parent.
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_orders SET status='ready' WHERE node_id=$1`, order)
		return err
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "work_leaf_required" {
		t.Fatalf("order binding bypass: %v", err)
	}
	status, raw := callAs(t, workorders.New(appPool), &p, "POST", "/api/work-orders", fmt.Sprintf(`{"title":"Forbidden parent order","parent_id":%q,"criteria":["Done"]}`, leaf.ID))
	if status != 409 || !containsBytes(raw, "work_leaf_required") {
		t.Fatalf("parent order %d %s", status, raw)
	}
}

func TestWorkLifecycleCancelSettlesOrderWithAudit(t *testing.T) {
	p := newPrincipal(t, "lifecycle-order-cancel")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	leaf := workNodeTest(t, p, project.ID, "open")
	var order string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		o, err := workorders.Create(t.Context(), tx, p, workorders.CreateInput{Title: "Idle order", Parent: &leaf.ID, Criteria: []string{"Done"}})
		order = o.NodeID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request, status, raw := lifecycleRequest(t, p, leaf.ID, "cancel", nil)
	a := decode[workAction](t, status, raw, 200)
	if a.State != "completed" || len(a.Result) != 1 {
		t.Fatalf("idle cancel incomplete: %s", raw)
	}
	status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
	decode[workAction](t, status, raw, 200)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var state string
		var audits int
		if err := tx.QueryRow(t.Context(), `SELECT status FROM work_orders WHERE node_id=$1`, order).Scan(&state); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='work_order.cancelled'`, order).Scan(&audits); err != nil {
			return err
		}
		if state != "cancelled" || audits != 1 {
			t.Fatalf("order status=%s, cancellation events=%d", state, audits)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

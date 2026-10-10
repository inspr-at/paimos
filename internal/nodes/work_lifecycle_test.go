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
	"github.com/inspr-at/paimos/internal/harness"
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

// Real sweeper state, not a fabricated process exit, must retain the work fence.
func loseLifecycleSession(t *testing.T, p tenant.Principal, sid string) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET created_at='2000-01-01',heartbeat_at='2000-01-01' WHERE id=$1`, sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	n, err := harness.SweepLostContact(t.Context(), appPool, p.TenantID)
	if err != nil || n != 1 {
		t.Fatalf("lost-contact sweep=%d: %v", n, err)
	}
}

func TestWorkLifecycleLostContactRetainsStopFence(t *testing.T) {
	for _, kind := range []string{"split", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			p := newPrincipal(t, "lost-contact-work")
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
			leaf := workNodeTest(t, p, project.ID, "open")
			sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
			var children []splitChild
			if kind == "split" {
				children = []splitChild{{Title: "After stop"}}
			}
			request, status, raw := lifecycleRequest(t, p, leaf.ID, kind, children)
			if a := decode[workAction](t, status, raw, 200); a.State != "waiting" {
				t.Fatal("did not wait for worker")
			}
			loseLifecycleSession(t, p, sid)
			if !lifecyclePreview(t, p, leaf.ID).Busy {
				t.Fatal("heartbeat loss cleared busy fence")
			}
			status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
			if a := decode[workAction](t, status, raw, 200); a.State != "waiting" || a.WaitingCount != 1 || len(a.Result) != 0 {
				t.Fatalf("unconfirmed stop completed work: %s", raw)
			}
			m := &Module{pool: appPool, events: SQLWriter{}}
			if err := m.sweepWorkLifecycleTenant(t.Context(), p.TenantID); err != nil {
				t.Fatal(err)
			}
			var state string
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT state FROM work_lifecycle_actions WHERE id=$1`, request).Scan(&state)
			}); err != nil {
				t.Fatal(err)
			}
			if state != "waiting" {
				t.Fatal("background completion accepted heartbeat loss")
			}
			stopLifecycleSession(t, p, sid)
			status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
			if a := decode[workAction](t, status, raw, 200); a.State != "completed" || len(a.Result) != 1 {
				t.Fatalf("confirmed stop did not release fence: %s", raw)
			}
		})
	}
}

func TestWorkLifecycleLostContactBlocksOrdinaryWrites(t *testing.T) {
	p := newPrincipal(t, "lost-contact-guards")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
	leaf := workNodeTest(t, p, project.ID, "open")
	sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
	loseLifecycleSession(t, p, sid)
	status, raw := call(t, &p, "POST", "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Child","parent_id":%q}`, leaf.KindID, leaf.ID))
	if status != 409 || !containsBytes(raw, "busy_work_leaf") {
		t.Fatalf("unconfirmed worker gained children: %d %s", status, raw)
	}
	status, raw = call(t, &p, "PATCH", "/api/nodes/"+leaf.ID, `{"state":"cancelled"}`)
	if status != 409 || !containsBytes(raw, "busy_work_leaf") {
		t.Fatalf("unconfirmed worker cancelled: %d %s", status, raw)
	}
	_, status, raw = lifecycleRequest(t, p, leaf.ID, "split", []splitChild{{Title: "Child"}})
	if status != 409 || !containsBytes(raw, "live session") {
		t.Fatalf("lost contact must expose recovery requirement: %d %s", status, raw)
	}
	t.Run("bounded stale holds", testWorkLifecycleStaleHolds)
}

func TestWorkLifecyclePendingFencesOriginalBindings(t *testing.T) {
	for _, kind := range []string{"split", "cancel"} {
		for _, lost := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/lost=%t", kind, lost), func(t *testing.T) {
				p := newPrincipal(t, "pending-rebinding")
				project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
				leaf := workNodeTest(t, p, project.ID, "open")
				other := workNodeTest(t, p, project.ID, "open")
				sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
				var children []splitChild
				if kind == "split" {
					children = []splitChild{{Title: "After stop"}}
				}
				_, status, raw := lifecycleRequest(t, p, leaf.ID, kind, children)
				if a := decode[workAction](t, status, raw, 200); a.State != "waiting" {
					t.Fatal("missing handover")
				}
				if lost {
					loseLifecycleSession(t, p, sid)
				}
				for _, target := range []any{nil, other.ID} {
					err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
						if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
							return err
						}
						_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET ticket_node_id=$2,work_shape=CASE WHEN $2::uuid IS NULL THEN 'unknown' ELSE 'ship' END WHERE id=$1`, sid, target)
						return err
					})
					var pg *pgconn.PgError
					if !errors.As(err, &pg) || pg.ConstraintName != "work_handover_pending" {
						t.Fatalf("original binding escaped handover: %v", err)
					}
				}
			})
		}
	}
}

func TestWorkLifecycleExpiredHandoverRetriesDelivery(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint(background), func(t *testing.T) {
			p := newPrincipal(t, "expired-handover")
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
			leaf := workNodeTest(t, p, project.ID, "open")
			sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
			sessionIDs := []string{sid}
			if background {
				// Exercise the full 20-generation handover scope; expiry and
				// renewal events must remain bounded without a 64-event cliff.
				for range 19 {
					sessionIDs = append(sessionIDs, lifecycleSession(t, p, project.ID, leaf.ID, false))
				}
			}
			request, status, raw := lifecycleRequest(t, p, leaf.ID, "split", []splitChild{{Title: "After retry"}})
			if a := decode[workAction](t, status, raw, 200); a.State != "waiting" {
				t.Fatal("missing waiting action")
			}
			var oldControl string
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT pause_record->>'control_id' FROM harness_sessions WHERE id=$1`, sid).Scan(&oldControl); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET pause_record=jsonb_set(pause_record,'{deadline_at}','"2000-01-01T00:00:00Z"'::jsonb) WHERE id=ANY($1::uuid[])`, sessionIDs)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// The production deadline sweeper expires the original cooperative request.
			n, err := harness.SweepLostContact(t.Context(), appPool, p.TenantID)
			if err != nil || n != 0 {
				t.Fatalf("deadline sweep=%d: %v", n, err)
			}
			if background {
				m := &Module{pool: appPool, events: SQLWriter{}}
				if err := m.sweepWorkLifecycleTenant(t.Context(), p.TenantID); err != nil {
					t.Fatal(err)
				}
			} else {
				status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
				if a := decode[workAction](t, status, raw, 200); a.State != "waiting" {
					t.Fatal("retry fabricated a stop")
				}
			}
			var control, state, reason string
			var active, signals int
			var stopped bool
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT pause_record->>'control_id',pause_record->>'state',pause_record->>'reason',stopped_at IS NOT NULL FROM harness_sessions WHERE id=$1`, sid).Scan(&control, &state, &reason, &stopped); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE state IN ('pending','claimed') AND request_payload->>'pause'='true'),count(*) FILTER(WHERE kind='interrupt' OR request_payload->>'stop_now'='true') FROM harness_controls WHERE session_id=ANY($1::uuid[])`, sessionIDs).Scan(&active, &signals)
			}); err != nil {
				t.Fatal(err)
			}
			if control == oldControl || state != "requested" || reason != "work_lifecycle:"+request || active != len(sessionIDs) || signals != 0 || stopped {
				t.Fatalf("retry stranded or escalated handover: control=%s state=%s active=%d signals=%d stopped=%t", control, state, active, signals, stopped)
			}
			// Retry replay preserves the same pending delivery.
			status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
			if a := decode[workAction](t, status, raw, 200); a.State != "waiting" {
				t.Fatal("retry replay completed")
			}
			var replayControl string
			var requestedEvents int
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT pause_record->>'control_id' FROM harness_sessions WHERE id=$1`, sid).Scan(&replayControl); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.pause_requested' AND after->>'id'=$1`, sid).Scan(&requestedEvents)
			}); err != nil {
				t.Fatal(err)
			}
			if replayControl != control || requestedEvents != 2 {
				t.Fatalf("retry replay duplicated delivery: control=%s events=%d", replayControl, requestedEvents)
			}
			for _, session := range sessionIDs {
				stopLifecycleSession(t, p, session)
			}
			status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
			if a := decode[workAction](t, status, raw, 200); a.State != "completed" || len(a.Result) != 1 {
				t.Fatalf("retry did not finish after confirmed stop: %s", raw)
			}
		})
	}
}

func TestWorkLifecyclePendingFencesOrderAndRunBindings(t *testing.T) {
	for _, kind := range []string{"split", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			p := newPrincipal(t, "pending-order-bindings")
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
			leaf := workNodeTest(t, p, project.ID, "open")
			other := workNodeTest(t, p, project.ID, "open")
			// The source is reachable only via work_order_id/run_id, not ticket_node_id.
			sid := lifecycleSession(t, p, project.ID, other.ID, false)
			var sourceOrder, destinationOrder, run, agent string
			err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				for _, binding := range []struct {
					parent string
					save   *string
				}{{leaf.ID, &sourceOrder}, {other.ID, &destinationOrder}} {
					o, err := workorders.Create(t.Context(), tx, p, workorders.CreateInput{Title: "Bound order", Parent: &binding.parent, Criteria: []string{"Done"}})
					if err != nil {
						return err
					}
					*binding.save = o.NodeID
				}
				if err := tx.QueryRow(t.Context(), `SELECT agent_principal_id::text FROM harness_sessions WHERE id=$1`, sid).Scan(&agent); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,queue_node_id,agent_principal_id,status,queue_by_principal_id,queue_at,queue_security_review_required) VALUES($1,$2,$3,$4,'running',$5,clock_timestamp(),false) RETURNING id::text`, p.TenantID, sourceOrder, leaf.ID, agent, p.ID).Scan(&run); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET ticket_node_id=NULL,work_shape='unknown',work_order_id=$2,run_id=$3 WHERE id=$1`, sid, sourceOrder, run)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			var children []splitChild
			if kind == "split" {
				children = []splitChild{{Title: "After stop"}}
			}
			_, status, raw := lifecycleRequest(t, p, leaf.ID, kind, children)
			if a := decode[workAction](t, status, raw, 200); a.State != "waiting" {
				t.Fatal("bound run was not fenced")
			}
			// Preserve original run and order placement even for terminal run writes.
			checks := []struct {
				sql  string
				args []any
			}{
				{`UPDATE harness_sessions SET work_order_id=NULL,run_id=NULL WHERE id=$1`, []any{sid}},
				{`UPDATE harness_sessions SET work_order_id=$2,run_id=NULL WHERE id=$1`, []any{sid, destinationOrder}},
				{`UPDATE agent_runs SET queue_node_id=$2,work_order_id=$3 WHERE id=$1`, []any{run, other.ID, destinationOrder}},
				{`UPDATE agent_runs SET queue_node_id=NULL,queue_by_principal_id=NULL,queue_at=NULL,queue_security_review_required=NULL,work_order_id=$2,status='completed' WHERE id=$1`, []any{run, destinationOrder}},
			}
			for _, check := range checks {
				err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), check.sql, check.args...)
					return err
				})
				var pg *pgconn.PgError
				if !errors.As(err, &pg) || pg.ConstraintName != "work_handover_pending" {
					t.Fatalf("bound generation escaped original order/run: %v", err)
				}
			}
		})
	}
}

// R4/R6/R7/R9: stale administrative holds must not strand cancellation, claim
// process-exit evidence, release accounting, leak private diagnostics or bypass
// the original person's current authority. Fixed timestamps prove the boundary.
func testWorkLifecycleStaleHolds(t *testing.T) {
	t.Run("SQL grace boundary and exit proof", func(t *testing.T) {
		useDB(t)
		now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
		old, young, boundary := now.Add(-3*time.Hour), now.Add(-time.Hour), now.Add(-2*time.Hour)
		reasons := []string{"heartbeat_lost", "archived_process_unknown", "removed_process_unknown", "attach detached; process exit unconfirmed"}
		for _, reason := range reasons {
			for _, c := range []struct {
				name               string
				stopped, heartbeat *time.Time
				want               bool
			}{
				{"young NULL heartbeat", &young, nil, false}, {"old NULL heartbeat", &old, nil, true},
				{"exact boundary", &boundary, nil, true}, {"recent heartbeat", &old, &young, false},
				{"old heartbeat", &old, &old, true}, {"live", nil, &old, false},
			} {
				t.Run(reason+"/"+c.name, func(t *testing.T) {
					var released, confirmed bool
					err := appPool.QueryRow(t.Context(), `SELECT aeon_work_session_released($1::timestamptz,$2::text,$3::timestamptz,$4::timestamptz),aeon_work_session_stopped($1::timestamptz,$2::text)`, c.stopped, reason, c.heartbeat, now).Scan(&released, &confirmed)
					if err != nil || released != c.want || confirmed {
						t.Fatalf("released=%t confirmed=%t want=%t: %v", released, confirmed, c.want, err)
					}
				})
			}
		}
		for _, c := range []struct {
			reason  string
			stopped *time.Time
			want    bool
		}{
			{"completed", &now, true}, {"completed", nil, false}, {"ownership_lost", &old, false}, {"unknown", &old, false},
		} {
			var got bool
			if err := appPool.QueryRow(t.Context(), `SELECT aeon_work_session_released($1::timestamptz,$2::text,NULL,$3::timestamptz)`, c.stopped, c.reason, now).Scan(&got); err != nil || got != c.want {
				t.Fatalf("%s released=%t: %v", c.reason, got, err)
			}
		}
	})
	for _, reason := range []string{"heartbeat_lost", "archived_process_unknown", "removed_process_unknown", "attach detached; process exit unconfirmed", "completed", "unknown", "live"} {
		for _, age := range []int{1, 3} {
			t.Run(fmt.Sprintf("cancel/%s/%dh", reason, age), func(t *testing.T) {
				p := newPrincipal(t, "stale-hold")
				project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
				leaf := workNodeTest(t, p, project.ID, "open")
				sid := lifecycleSession(t, p, project.ID, leaf.ID, false)
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET display_label='gate-stale',host='private-host',created_at=now()-interval '4 hours',heartbeat_at=NULL,
      stopped_at=CASE WHEN $2='live' THEN NULL ELSE now()-make_interval(hours=>$3) END,
      stop_reason=CASE WHEN $2='live' THEN NULL ELSE $2 END,phase=CASE WHEN $2='live' THEN 'working' ELSE 'stopped' END WHERE id=$1`, sid, reason, age)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				released := reason == "completed" || age == 3 && reason != "unknown" && reason != "live"
				if lifecyclePreview(t, p, leaf.ID).Busy == released {
					t.Fatal("preview disagrees with hold expiry")
				}
				status, raw := call(t, &p, "PATCH", "/api/nodes/"+leaf.ID, `{"state":"cancelled"}`)
				if released {
					decode[nodeJSON](t, status, raw, 200)
				} else {
					if status != 409 || !containsBytes(raw, "busy_work_leaf") || !containsBytes(raw, sid) || !containsBytes(raw, "gate-stale") || !containsBytes(raw, "age ") || containsBytes(raw, "private-host") {
						t.Fatalf("wrong holder diagnostic: %d %s", status, raw)
					}
					if currentWorkTest(t, p, leaf.ID).State != "open" {
						t.Fatal("busy cancel mutated work")
					}
				}
				var binding, saved string
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT ticket_node_id::text,coalesce(stop_reason,'live') FROM harness_sessions WHERE id=$1`, sid).Scan(&binding, &saved)
				}); err != nil {
					t.Fatal(err)
				}
				if binding != leaf.ID || saved != reason {
					t.Fatal("expiry rewrote process evidence or historical binding")
				}
			})
		}
	}
	for _, c := range []struct {
		name                  string
		age                   int
		reason                string
		telemetry, ticketOnly bool
		want                  string
	}{
		{name: "young orphan", age: 1, want: "rejected"},
		{name: "historical closure before new run", age: 1, reason: "historical_completed", ticketOnly: true, want: "rejected"},
		{name: "revoked run authority", age: 3, want: "forbidden"},
		{name: "old orphan", age: 3, want: "completed"},
		{name: "old run young uncertain session", age: 3, reason: "heartbeat_lost", want: "rejected"},
		{name: "expired session", age: 3, reason: "expired", want: "completed"},
		{name: "confirmed session", age: 1, reason: "completed", want: "completed"},
		{name: "fresh telemetry after expired session", age: 3, reason: "expired", telemetry: true, want: "rejected"},
		{name: "fresh telemetry orphan", age: 3, telemetry: true, want: "rejected"},
		{name: "old run live session", age: 3, reason: "live", want: "waiting"},
		{name: "old run live ticket binding", age: 3, reason: "live", ticketOnly: true, want: "waiting"},
	} {
		t.Run("graceful run/"+c.name, func(t *testing.T) {
			p := newPrincipal(t, "stale-run-hold")
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
			leaf := workNodeTest(t, p, project.ID, "open")
			var sid, run, order, agent string
			if c.reason != "" {
				sid = lifecycleSession(t, p, project.ID, leaf.ID, false)
			}
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				o, err := workorders.Create(t.Context(), tx, p, workorders.CreateInput{Title: "Private order title", Parent: &leaf.ID, Criteria: []string{"Done"}})
				if err != nil {
					return err
				}
				order = o.NodeID
				if sid != "" {
					err = tx.QueryRow(t.Context(), `SELECT agent_principal_id::text FROM harness_sessions WHERE id=$1`, sid).Scan(&agent)
				} else {
					err = tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Private principal') RETURNING id::text`, p.TenantID).Scan(&agent)
				}
				if err != nil {
					return err
				}
				if err = tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,queue_node_id,agent_principal_id,status,queue_by_principal_id,queue_at,queue_security_review_required,created_at,started_at,trace)
     VALUES($1,$2,$3,$4,'running',$5,now()-make_interval(hours=>$6),false,now()-make_interval(hours=>$6),now()-make_interval(hours=>$6),'{"worker_assignment":{"state":"launched"}}') RETURNING id::text`, p.TenantID, order, leaf.ID, agent, p.ID, c.age).Scan(&run); err != nil {
					return err
				}
				if _, err = tx.Exec(t.Context(), `UPDATE work_orders SET status='running' WHERE node_id=$1`, order); err != nil {
					return err
				}
				if sid != "" {
					sessionAge := 1
					reason := c.reason
					if reason == "historical_completed" {
						sessionAge = 3
						reason = "completed"
					}
					if reason == "expired" {
						sessionAge = 3
						reason = "heartbeat_lost"
					}
					_, err = tx.Exec(t.Context(), `UPDATE harness_sessions SET run_id=CASE WHEN $3 THEN NULL ELSE $2::uuid END,display_label='gate-run',heartbeat_at=NULL,
      stopped_at=CASE WHEN $4='live' THEN NULL ELSE now()-make_interval(hours=>$5) END,
      phase=CASE WHEN $4='live' THEN 'working' ELSE 'stopped' END,stop_reason=CASE WHEN $4='live' THEN NULL ELSE $4 END WHERE id=$1`, sid, run, c.ticketOnly, reason, sessionAge)
					if err != nil {
						return err
					}
				}
				if c.telemetry {
					_, err = tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind) VALUES($1,$2,1,'heartbeat')`, p.TenantID, run)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// An active run/order still requires the person action even after expiry.
			status, raw := call(t, &p, "PATCH", "/api/nodes/"+leaf.ID, `{"state":"cancelled"}`)
			if status != 409 || !containsBytes(raw, run) || !containsBytes(raw, order) || !containsBytes(raw, "age ") || containsBytes(raw, "Private order title") || containsBytes(raw, "Private principal") {
				t.Fatalf("run/order diagnostic: %d %s", status, raw)
			}
			if c.want == "forbidden" {
				if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_permissions WHERE tenant_id=$1 AND permission='run.create'`, p.TenantID); err != nil {
					t.Fatal(err)
				}
			}
			request, status, raw := lifecycleRequest(t, p, leaf.ID, "cancel", nil)
			if c.want == "forbidden" {
				if status != 403 || !containsBytes(raw, "forbidden") {
					t.Fatalf("revoked run authority was accepted: %d %s", status, raw)
				}
			} else if c.want == "rejected" {
				if status != 409 || !containsBytes(raw, "live session") || !containsBytes(raw, run) {
					t.Fatalf("young or reporting hold was released: %d %s", status, raw)
				}
			} else {
				if a := decode[workAction](t, status, raw, 200); a.State != c.want {
					t.Fatalf("want %s: %s", c.want, raw)
				}
			}
			if c.want == "completed" {
				status, raw = call(t, &p, "POST", "/api/nodes/"+leaf.ID+"/work-lifecycle/"+request+"/continue", "")
				if a := decode[workAction](t, status, raw, 200); a.State != "completed" {
					t.Fatal("completed replay failed")
				}
			}
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				var state, assignment, orderState string
				var audits int
				var proof bool
				if err := tx.QueryRow(t.Context(), `SELECT status,trace->'worker_assignment'->>'state' FROM agent_runs WHERE id=$1`, run).Scan(&state, &assignment); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT status FROM work_orders WHERE node_id=$1`, order).Scan(&orderState); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*),coalesce(bool_or((after->>'process_exit_confirmed')::boolean),false) FROM events WHERE node_id=$1 AND type='run.stale_hold_released' AND after->'run'->>'id'=$2 AND actor_principal_id=$3 AND after->>'action_id'=$4`, order, run, p.ID, request).Scan(&audits, &proof); err != nil {
					return err
				}
				if c.want == "completed" {
					if state != "ownership_lost" || orderState != "cancelled" || audits != 1 || proof || currentWorkTest(t, p, leaf.ID).State != "cancelled" {
						t.Fatalf("stale release: run=%s order=%s audits=%d exit=%t", state, orderState, audits, proof)
					}
				} else if state != "running" || audits != 0 || orderState != "running" {
					t.Fatal("live or young run was mutated")
				}
				if assignment != "launched" {
					t.Fatal("expiry erased unconfirmed writer evidence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, c := range []struct {
		name       string
		age        int
		closureAge int
		reason     string
		want       int
	}{
		{"young standalone order", 1, 0, "", 409}, {"old standalone order", 3, 0, "", 200}, {"confirmed standalone order", 1, 0, "completed", 200},
		{"historical confirmed closure with new order", 1, 4, "completed", 409},
		{"historical expired closure with new order", 1, 4, "heartbeat_lost", 409},
		{"historical closure with old order", 3, 4, "completed", 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPrincipal(t, "stale-order")
			project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
			leaf := workNodeTest(t, p, project.ID, "open")
			if c.reason != "" {
				sid := lifecycleSession(t, p, project.ID, leaf.ID, true)
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET created_at=now()-make_interval(hours=>$2)-interval '1 hour',stopped_at=now()-make_interval(hours=>$2),stop_reason=$3,heartbeat_at=NULL WHERE id=$1`, sid, c.closureAge, c.reason)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			var order string
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				o, err := workorders.Create(t.Context(), tx, p, workorders.CreateInput{Title: "Order", Parent: &leaf.ID, Criteria: []string{"Done"}})
				if err != nil {
					return err
				}
				order = o.NodeID
				_, err = tx.Exec(t.Context(), `UPDATE work_orders SET status='running',updated_at=now()-make_interval(hours=>$2) WHERE node_id=$1`, order, c.age)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, status, raw := lifecycleRequest(t, p, leaf.ID, "cancel", nil)
			if status != c.want {
				t.Fatalf("order hold status %d want %d: %s", status, c.want, raw)
			}
			if status == 409 && (!containsBytes(raw, order) || !containsBytes(raw, "age ")) {
				t.Fatalf("missing order holder: %s", raw)
			}
			if status == 409 {
				if !containsBytes(raw, "running work must publish a live session") {
					t.Fatalf("standalone order rejected for the wrong reason: %s", raw)
				}
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					var state string
					var actions int
					if err := tx.QueryRow(t.Context(), `SELECT status FROM work_orders WHERE node_id=$1`, order).Scan(&state); err != nil {
						return err
					}
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM work_lifecycle_actions WHERE node_id=$1`, leaf.ID).Scan(&actions); err != nil {
						return err
					}
					if state != "running" || actions != 0 || currentWorkTest(t, p, leaf.ID).State != "open" {
						t.Fatal("held standalone order or ticket was mutated")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if status == 200 {
				if a := decode[workAction](t, status, raw, 200); a.State != "completed" {
					t.Fatalf("order did not settle: %s", raw)
				}
			}
		})
	}
	t.Run("diagnostic visibility and truncation", func(t *testing.T) {
		p := newPrincipal(t, "hold-diagnostics")
		project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Project"}`, kindBySlug(t, p, "project").ID))
		leaf := workNodeTest(t, p, project.ID, "open")
		hidden := lifecycleSession(t, p, project.ID, leaf.ID, false)
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET display_label='private-label' WHERE id=$1`, hidden); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.visible_projects','',true),set_config('aeon.system','off',true)`); err != nil {
				return err
			}
			var busy bool
			var holders string
			if err := tx.QueryRow(t.Context(), `SELECT aeon_work_busy($1),aeon_work_busy_holders($1)`, leaf.ID).Scan(&busy, &holders); err != nil {
				return err
			}
			if !busy || strings.Contains(holders, hidden) || strings.Contains(holders, "private-label") {
				t.Fatalf("hidden holder leaked or fence lost: %s", holders)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for range 20 {
			lifecycleSession(t, p, project.ID, leaf.ID, false)
		}
		status, raw := call(t, &p, "PATCH", "/api/nodes/"+leaf.ID, `{"state":"cancelled"}`)
		if status != 409 || !containsBytes(raw, "additional holders omitted") || strings.Count(string(raw), "age ") != 20 {
			t.Fatalf("unbounded or silent truncation: %d %s", status, raw)
		}
	})
}

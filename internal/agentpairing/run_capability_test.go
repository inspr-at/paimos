// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Risk: one computer's ambient transport authority exposes another person's
// run context, or a stale capability survives person/generation revocation.
// All content markers remain in the fixture while the negative reads run.
func TestRunCapabilitySeparatesTransportAndBindsContent(t *testing.T) {
	f := newFixture(t)
	p := f.propose("codex", "claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	e, sibling := v.Enrollments[0], v.Enrollments[1]
	if e.Harness != "codex" {
		e, sibling = sibling, e
	}
	f.probe(v, e, key, 200)
	f.probe(v, sibling, key, 200)
	f.call("POST", "/api/agent-accounts/"+e.AccountID+"/capacity/approve", nil, true, "", 204)
	call := func(method, path string, body any, proof string, modern bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := f.request(method, path, body, false, key)
		if proof != "" {
			r.Header.Set(db.RunCapabilityHeader, proof)
		}
		if modern {
			r.Header.Set(db.RunCapabilityPolicyHeader, db.RunCapabilityPolicy)
		}
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	var projectA, projectB string
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		for _, project := range []struct {
			key string
			id  *string
		}{{"CAPA-1", &projectA}, {"CAPB-1", &projectB}} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,'Capability project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.tenantID, project.key).Scan(project.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newRun := func(title, project string) agentruns.Run {
		t.Helper()
		var order workorders.Order
		decodeResult(t, f.call("POST", "/api/work-orders", map[string]any{"title": title, "parent_id": project, "body": "private-work-order-body", "criteria": []string{"private-criterion"}, "assignee_principal_id": *v.PrincipalID}, true, "", 201), &order)
		decodeResult(t, f.call("PATCH", "/api/work-orders/"+order.NodeID, map[string]any{"expected_revision": order.Revision, "status": "ready"}, true, "", 200), &order)
		var run agentruns.Run
		decodeResult(t, f.call("POST", "/api/work-orders/"+order.NodeID+"/runs", map[string]any{"agent_principal_id": *v.PrincipalID, "model_profile_id": e.ProfileID, "requested_account_id": e.AccountID}, true, "", 201), &run)
		return run
	}
	a, b := newRun("private-person-A", projectA), newRun("private-person-B", projectB)
	// B serves another real person; issuer and machine remain unchanged.
	var personB string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Person B') RETURNING id::text`, f.tenantID).Scan(&personB); err != nil {
			return err
		}
		// Seed historical provenance directly: production bindings are write-once.
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET acting_person_id=$2,trace='{"private_trace":"retained-trace-content"}' WHERE id=$1`, b.ID, personB)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,project_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, f.tenantID, personB, projectB); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,computer_id) SELECT $1,$2,id,'computer',$3 FROM roles WHERE tenant_id=$1 AND key='admin'`, f.tenantID, personB, *v.ComputerID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	source := newRun("retained recovery source", projectB)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET acting_person_id=$2,status='completed',ended_at=clock_timestamp() WHERE id=$1`, source.ID, personB); err != nil {
			return err
		}
		var session string
		if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,work_order_id,ticket_node_id,work_shape,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,$5,'ship','codex','fixture-host','managed','worker','\x01','\x02') RETURNING id::text`, f.tenantID, projectB, *v.PrincipalID, source.ID, source.OrderID).Scan(&session); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_recoveries(tenant_id,id,session_id,requested_by,action,observed_revision,expected_ownership,request_digest,context_digest,state,outcome,next_run_id,handover,service_tier,display_label) VALUES($1,gen_random_uuid(),$2,$3,'restart',repeat('a',64),'{}','\x03','\x04','completed','continuation_queued',$4,'{"brief":"retained-handover-content"}','default','retained-private-label')`, f.tenantID, session, personB, b.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue := func(run string) string {
		t.Helper()
		w := call("POST", "/api/runs/"+run+"/capability", map[string]string{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation"}, "", true, 200)
		var out struct {
			Token string `json:"capability"`
		}
		decodeResult(t, w, &out)
		if len(out.Token) != 64 {
			t.Fatal("server did not issue a bounded credential")
		}
		return out.Token
	}
	for _, path := range []string{"/api/runs/queued", "/api/runs", "/api/runs/" + b.ID} {
		w := call("GET", path, nil, "", false, 200)
		for _, marker := range []string{"retained-trace-content", "retained-handover-content", "retained-private-label", "private-person", "recovery_brief", "recovery_display_label", "\"trace\""} {
			if strings.Contains(w.Body.String(), marker) {
				t.Fatalf("transport leaked %s", marker)
			}
		}
		if !strings.Contains(w.Body.String(), "run_capability_upgrade_required") && w.Header().Get("Aeon-Upgrade-Reason") != "run_capability_upgrade_required" {
			t.Fatal("old daemon did not receive upgrade reason")
		}
	}
	for _, path := range []string{"/api/nodes/" + a.OrderID, "/api/work-orders/" + a.OrderID, "/api/runs/" + a.ID + "/handoff"} {
		w := call("GET", path, nil, "", false, 403)
		if !strings.Contains(w.Body.String(), "run_capability_upgrade_required") {
			t.Fatal("content refused for wrong reason")
		}
	}
	var unbound string
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_account_id) SELECT tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_account_id FROM agent_runs WHERE id=$1 RETURNING id::text`, a.ID).Scan(&unbound)
	}); err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/runs/"+unbound+"/capability", map[string]string{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation"}, "", true, 403)
	// Old daemons cannot claim even a bound run, and cannot repair an unbound
	// run by supplying a person/header of their own.
	call("POST", "/api/runs/"+a.ID+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": []string{uuid(t, f.db)}}, "", false, 403)
	call("POST", "/api/runs/"+unbound+"/claim", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "reservation_ids": []string{uuid(t, f.db)}}, "", false, 403)
	tokenA, tokenB := issue(a.ID), issue(b.ID)
	otherComputer := f.propose("codex")
	f.approve(otherComputer, "connect_only")
	otherView := f.redeem(otherComputer)
	otherKey := "aeon_" + otherView.RuntimePrefix + "_" + otherComputer.runtime
	wrongComputer := f.request("GET", "/api/runs/"+a.ID, nil, false, otherKey)
	wrongComputer.Header.Set(db.RunCapabilityHeader, tokenA)
	wrongResponse := httptest.NewRecorder()
	f.h.ServeHTTP(wrongResponse, wrongComputer)
	if wrongResponse.Code != 403 {
		t.Fatalf("wrong computer accepted: %d", wrongResponse.Code)
	}
	call("GET", "/api/nodes/"+a.OrderID, nil, tokenA, true, 200)
	w := call("GET", "/api/runs/"+b.ID, nil, tokenB, true, 200)
	if !strings.Contains(w.Body.String(), "retained-trace-content") || !strings.Contains(w.Body.String(), "retained-handover-content") {
		t.Fatal("authorized content fixture was lost")
	}
	call("GET", "/api/runs/"+b.ID, nil, tokenA, true, 403)
	call("GET", "/api/runs", nil, tokenA, true, 403)
	call("GET", "/api/work-orders/"+b.OrderID, nil, tokenA, true, 403)
	call("GET", "/api/nodes/"+b.OrderID, nil, tokenA, true, 403)
	call("GET", "/api/runs/"+a.ID, nil, strings.Repeat("0", 64), true, 403)
	call("POST", "/api/runs/"+a.ID+"/capability", map[string]string{"daemon_id": *v.DaemonID, "daemon_generation": "wrong-generation"}, "", true, 403)
	// Reissuance invalidates the predecessor credential, independently of time.
	freshA := issue(a.ID)
	call("GET", "/api/runs/"+a.ID, nil, tokenA, true, 403)
	call("GET", "/api/runs/"+a.ID, nil, freshA, true, 200)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET content_capability_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/runs/"+a.ID, nil, freshA, true, 403)
	freshA = issue(a.ID)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET content_capability_person_id=$2 WHERE id=$1`, a.ID, personB)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	call("GET", "/api/runs/"+a.ID, nil, freshA, true, 403)
	// The same token works immediately before the real generation bump and
	// fails afterwards; no sleep or real-clock boundary is involved.
	call("GET", "/api/runs/"+b.ID, nil, tokenB, true, 200)
	call("POST", "/api/agent-accounts/"+sibling.AccountID+"/probe", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "next-generation", "available": true}, "", false, 200)
	call("GET", "/api/runs/"+b.ID, nil, tokenB, true, 403)
	call("POST", "/api/agent-accounts/"+sibling.AccountID+"/probe", map[string]any{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation", "available": true}, "", false, 200)
	call("GET", "/api/runs/"+b.ID, nil, tokenB, true, 403)
	// Person deactivation cannot be bypassed by issuing another capability.
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, personB)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	denied := call("POST", "/api/runs/"+b.ID+"/capability", map[string]string{"daemon_id": *v.DaemonID, "daemon_generation": "test-generation"}, "", true, 403)
	if !strings.Contains(denied.Body.String(), "permission_revoked") {
		t.Fatal("revoked person refused for wrong reason")
	}
	var retained string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT trace->>'private_trace' FROM agent_runs WHERE id=$1`, b.ID).Scan(&retained); err != nil || retained != "retained-trace-content" {
		t.Fatal("negative fixture was destroyed")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestThrottledHeartbeatAndStateEvidence(t *testing.T) {
	f := fixture(t) // internal/dbtest: isolated database and tenant transactions.
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "sc1-lease-0000000000000000000000001"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
		"harness_session_ref": "sc1-generation-0000000001", "worker_lease": lease,
		"management_mode": "managed", "role": "worker",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	beat := map[string]any{"phase": "working", "activity": "throttled", "activity_sequence": 1}
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, "wrong-lease-00000000000000000000001"), 403)
	w = f.call(f.agent, "POST", path+"/heartbeat", beat, lease)
	expect(t, w, 200)
	if decode(t, w)["activity"] != "throttled" {
		t.Fatal("throttled activity was lost")
	}
	beat["activity"] = "busy"
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 409)
	beat["activity"] = "throttled"
	beat["activity_sequence"] = 0
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", beat, lease), 409)
	for _, endpoint := range []string{path, "/api/harness-sessions", "/api/harness-sessions/live", "/api/harness-sessions/live?include_inactive=true"} {
		w = f.call(f.person, "GET", endpoint, nil, "")
		expect(t, w, 200)
		data := decode(t, w)
		if items, ok := data["items"].([]any); ok {
			data = items[0].(map[string]any)
		}
		if data["activity"] != "throttled" || data["has_problem"] != false || data["needs_attention"] != false {
			t.Fatalf("state evidence from %s: %v", endpoint, data)
		}
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.heartbeat' AND after->>'activity'='throttled'`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("expected one committed throttled heartbeat event, got %d", n)
		}
		return nil
	})
	// Normal closure remains neutral even though the last activity was throttled.
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
	data := decode(t, f.call(f.person, "GET", path, nil, ""))
	if data["has_problem"] != false {
		t.Fatal("normal stop became a problem")
	}
	legacy := decode(t, f.call(f.person, "GET", "/api/harness-sessions/live", nil, ""))["items"].([]any)
	if len(legacy) != 0 {
		t.Fatal("legacy live response includes stopped history")
	}
	state := decode(t, f.call(f.person, "GET", "/api/harness-sessions/live?include_inactive=true", nil, ""))["items"].([]any)
	if len(state) != 1 || state[0].(map[string]any)["phase"] != "stopped" {
		t.Fatal("state view lost recent stop")
	}
	expect(t, f.call(f.foreign, "GET", path, nil, ""), 404)
	foreign := decode(t, f.call(f.foreign, "GET", "/api/harness-sessions/live?include_inactive=true", nil, ""))["items"].([]any)
	if len(foreign) != 0 {
		t.Fatal("state evidence crossed tenant boundary")
	}
}

func TestStateViewRetainsAgingAndInactiveSessions(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	for i, scenario := range []struct {
		phase, activity, reason string
		age                     string
		problem                 bool
	}{
		{"working", "busy", "", "11 minutes", false}, // Age thresholds belong to the viewer.
		{"working", "idle", "", "20 minutes", false},
		{"yielded", "unknown", "", "1 minute", false},
		{"stopped", "busy", "run_failed", "1 minute", true},
		{"stopped", "busy", "blocked", "1 minute", true},
		{"stopped", "busy", "completed", "1 minute", false},
	} {
		w := f.call(f.person, "POST", base, map[string]any{
			"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
			"harness_session_ref": fmt.Sprintf("sc1-state-generation-%d", i), "worker_lease": "sc1-state-lease-000000000000000001",
			"management_mode": "managed", "role": "worker",
		}, "")
		expect(t, w, 201)
		id := decode(t, w)["id"].(string)
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase=$2,activity=$3,stop_reason=$4,
                heartbeat_at=now()-$5::interval, stopped_at=CASE WHEN $2='stopped' THEN now() END WHERE id=$1`, id, scenario.phase, scenario.activity, scenario.reason, scenario.age)
			return err
		})
		data := decode(t, f.call(f.person, "GET", base+"/"+id, nil, ""))
		if data["has_problem"] != scenario.problem {
			t.Fatalf("reason %q: got problem %v", scenario.reason, data["has_problem"])
		}
	}
	items := decode(t, f.call(f.person, "GET", "/api/harness-sessions/live?include_inactive=true", nil, ""))["items"].([]any)
	if len(items) != 6 {
		t.Fatalf("state view contains %d, want all six", len(items))
	}
}

func TestStateEvidenceTracksRunOutcomeAndApprovalResolution(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
		"harness_session_ref": "sc1-evidence-generation", "worker_lease": "sc1-evidence-lease-000000000000001",
		"management_mode": "managed", "role": "worker",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	order, run, approval := uid(), uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,'SC1-1',id,'State test',$3 FROM node_kinds WHERE slug='work_order'`, []any{f.person.TenantID, order, f.project}},
			{`INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, []any{f.person.TenantID, order, f.person.ID}},
			{`INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,status) VALUES($1,$2,$3,$4,'waiting')`, []any{f.person.TenantID, run, order, f.agent.ID}},
			{`UPDATE harness_sessions SET run_id=$2,phase='working',activity='busy',heartbeat_at=now() WHERE id=$1`, []any{id, run}},
		} {
			if _, err := tx.Exec(t.Context(), statement.sql, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
	check := func(needs, problem bool, status string) {
		t.Helper()
		for _, endpoint := range []string{base + "/" + id, "/api/harness-sessions/live?include_inactive=true"} {
			w := f.call(f.person, "GET", endpoint, nil, "")
			expect(t, w, 200)
			data := decode(t, w)
			if items, ok := data["items"].([]any); ok {
				data = items[0].(map[string]any)
			}
			if data["needs_attention"] != needs || data["has_problem"] != problem || data["run_status"] != status {
				t.Fatalf("state from %s: %v", endpoint, data)
			}
		}
	}
	check(true, false, "waiting")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='failed' WHERE id=$1`, run)
		return err
	})
	check(false, true, "failed")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running' WHERE id=$1`, run); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,id,proposed_by_principal_id,agent_principal_id,run_id,scope,resource_kind,resource_id,rationale,expires_at)
            VALUES($1,$2,$3,$3,$4,'nodes.write','node',$5,'Continue work',now()+interval '1 hour')`, f.person.TenantID, approval, f.agent.ID, run, f.project)
		return err
	})
	check(true, false, "running")
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'approved')`, f.person.TenantID, approval, f.person.ID)
		return err
	})
	check(false, false, "running")
}

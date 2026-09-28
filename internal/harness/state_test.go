// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestThrottledHeartbeatAndStateEvidence(t *testing.T) {
	f := fixture(t) // internal/dbtest: isolated database and tenant transactions.
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "sc1-lease-0000000000000000000000001"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
		"harness_session_ref": "sc1-generation-0000000001", "worker_lease": lease,
		"management_mode": "managed", "role": "worker",
		"model": "integration-model", "reasoning_effort": "high", "account_label": "Test account",
		"harness_version": "1.2.3", "brief": "AEON-221", "worktree": "/Code/aeon-sc1", "branch": "sc1.state-colours",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	// TM1 metadata and SC1 evidence must share the live scan, including a
	// registered session that has not sent its first heartbeat yet.
	unstarted := decode(t, f.call(f.person, "GET", "/api/harness-sessions/live?include_inactive=true", nil, ""))["items"].([]any)
	if len(unstarted) != 1 || unstarted[0].(map[string]any)["heartbeat_at"] != nil || unstarted[0].(map[string]any)["model"] != "integration-model" {
		t.Fatalf("unstarted metadata/state scan: %v", unstarted)
	}
	beat := map[string]any{
		"phase": "working", "activity": "throttled", "activity_sequence": 1,
		"reasoning_effort": "xhigh", "commits": []map[string]string{{"sha": "abc1234", "subject": "Integrate session states"}},
	}
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
		for key, want := range map[string]string{"model": "integration-model", "reasoning_effort": "xhigh", "account_label": "Test account", "harness_version": "1.2.3"} {
			if data[key] != want {
				t.Fatalf("%s lost %s alongside throttled state: %v", endpoint, key, data[key])
			}
		}
		if endpoint == path || endpoint == "/api/harness-sessions" {
			for key, want := range map[string]string{"brief": "AEON-221", "worktree": "/Code/aeon-sc1", "branch": "sc1.state-colours"} {
				if data[key] != want {
					t.Fatalf("%s lost work context %s: %v", endpoint, key, data[key])
				}
			}
			commits, ok := data["commits"].([]any)
			if !ok || len(commits) != 1 || commits[0].(map[string]any)["sha"] != "abc1234" {
				t.Fatalf("%s lost heartbeat commit: %v", endpoint, data["commits"])
			}
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
	stopped := state[0].(map[string]any)
	if stopped["model"] != "integration-model" || stopped["reasoning_effort"] != "xhigh" || stopped["stop_reason"] != "process_exited" || stopped["stopped_at"] == nil {
		t.Fatalf("stopped scan lost metadata or stop evidence: %v", stopped)
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

func TestThrottledCoordinatorResolution(t *testing.T) {
	f := fixture(t)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "sc1-coordinator-lease-" + uid()
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
		"harness_session_ref": "sc1-coordinator-" + uid(), "worker_lease": lease,
		"management_mode": "managed", "role": "coordinator",
	}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	check := func(want string) {
		t.Helper()
		w := f.call(f.person, "GET", base+"/orchestrator", nil, "")
		expect(t, w, 200)
		data := decode(t, w)
		if data["state"] != want {
			t.Fatalf("orchestrator: got %v, want %s", data, want)
		}
		if want == "resolved" {
			s := data["session"].(map[string]any)
			if s["id"] != id || s["activity"] != "throttled" || s["needs_attention"] != false || s["has_problem"] != false {
				t.Fatalf("resolved coordinator lost state evidence: %v", s)
			}
		}
	}
	check("unset")
	for _, phase := range []string{"starting", "working", "yielded"} {
		w = f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": phase, "activity": "throttled", "activity_sequence": 1}, lease)
		expect(t, w, 200)
		if phase == "starting" {
			check("unset")
		} else {
			check("resolved")
		}
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=now()-interval '3 minutes' WHERE id=$1`, id)
		return err
	})
	check("unset")
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "throttled", "activity_sequence": 1}, lease), 200)
	check("resolved")
	expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "stopped"}, lease), 200)
	check("unset")
}

func stateRun(t *testing.T, f *harnessFixture, project, status, key string) (order, run string) {
	t.Helper()
	order, run = uid(), uid()
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id)
            SELECT $1,$2,$4,id,'State work order',$3 FROM node_kinds WHERE slug='work_order'`, f.person.TenantID, order, project, key); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.person.TenantID, order, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,status) VALUES($1,$2,$3,$4,$5)`, f.person.TenantID, run, order, f.agent.ID, status)
		return err
	})
	return order, run
}

func TestSessionMutationsOmitUnloadedStateEvidence(t *testing.T) {
	for _, scenario := range []string{"pending approval", "failed run"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			lease := "sc1-mutation-lease-" + uid()
			registration := map[string]any{
				"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
				"harness_session_ref": "sc1-mutation-" + uid(), "worker_lease": lease,
				"management_mode": "managed", "role": "worker",
			}
			if scenario == "failed run" {
				order, run := stateRun(t, f, f.project, "failed", "SC1-10")
				registration["work_order_id"], registration["run_id"] = order, run
			} else {
				order, run := stateRun(t, f, f.project, "running", "SC1-11")
				registration["work_order_id"], registration["run_id"] = order, run
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at,run_id)
                        VALUES($1,$2,$2,'nodes.write','node',$3,'Continue work',now()+interval '1 hour',$4)`, f.person.TenantID, f.agent.ID, f.ticket, run)
					return err
				})
			}
			unknown := func(data map[string]any) {
				t.Helper()
				for _, field := range []string{"run_status", "needs_attention", "has_problem", "attention_reasons"} {
					if value, present := data[field]; present {
						t.Fatalf("unloaded %s reported as %v", field, value)
					}
				}
			}
			w := f.call(f.person, "POST", base, registration, "")
			expect(t, w, 201)
			s := decode(t, w)
			unknown(s)
			path := base + "/" + s["id"].(string)
			w = f.call(f.person, "POST", base, registration, "")
			expect(t, w, 201)
			unknown(decode(t, w)) // exact registration replay
			for _, mutation := range []struct {
				method, suffix string
				body           map[string]any
			}{
				{"PATCH", "/binding", map[string]any{"expected_revision": s["revision"], "ticket_node_id": f.ticket, "work_shape": "ship"}},
				{"POST", "/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}},
				{"POST", "/yield", map[string]any{}},
				{"POST", "/yield", map[string]any{}}, // already-yielded replay
				{"POST", "/stop", map[string]any{"reason": "process_failed"}},
			} {
				w = f.call(f.agent, mutation.method, path+mutation.suffix, mutation.body, lease)
				expect(t, w, 200)
				data := decode(t, w)
				if nested, ok := data["session"].(map[string]any); ok {
					data = nested
				}
				unknown(data)
				w = f.call(f.person, "GET", path, nil, "")
				expect(t, w, 200)
				data = decode(t, w)
				stopped := mutation.suffix == "/stop"
				if data["needs_attention"] != (scenario == "pending approval" && !stopped) || data["has_problem"] != (scenario == "failed run" || stopped) {
					t.Fatalf("read after %s lost evidence: %v", mutation.suffix, data)
				}
				if scenario == "failed run" && data["run_status"] != "failed" {
					t.Fatalf("read after %s lost run status: %v", mutation.suffix, data)
				}
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var n int
				err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type LIKE 'harness.%'
                    AND ("before" ?| ARRAY['run_status','needs_attention','has_problem','attention_reasons'] OR "after" ?| ARRAY['run_status','needs_attention','has_problem','attention_reasons'])`).Scan(&n)
				if n != 0 {
					t.Errorf("%d event snapshots contain unloaded evidence", n)
				}
				return err
			})
		})
	}
}

func TestPendingApprovalStateRespectsProjectVisibility(t *testing.T) {
	f := fixture(t)
	second, unassigned := uid(), uid()
	reader := tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Person}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title)
            SELECT $1,$2,'SC1-3',kind_id,'Hidden project' FROM nodes WHERE id=$3`, f.person.TenantID, second, f.project); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,key,kind_id,title)
            SELECT $1,$2,'SC1-4',id,'Unassigned resource' FROM node_kinds WHERE slug='ticket'`, f.person.TenantID, unassigned); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Project reader')`, reader.TenantID, reader.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
            SELECT $1,$2,id,'project',$3 FROM roles WHERE key='member'`, reader.TenantID, reader.ID, f.project)
		return err
	})
	sameOrder, sameRun := stateRun(t, f, f.project, "running", "SC1-10")
	_, otherRun := stateRun(t, f, second, "running", "SC1-11")
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "sc1-project-lease-" + uid()
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "codex", "host": "test-host",
		"harness_session_ref": "sc1-project-" + uid(), "worker_lease": lease,
		"run_id": sameRun, "work_order_id": sameOrder,
		"management_mode": "managed", "role": "worker",
	}, "")
	expect(t, w, 201)
	path := base + "/" + decode(t, w)["id"].(string)
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}, lease), 200)
	check := func(t *testing.T, p tenant.Principal, want bool) {
		t.Helper()
		for _, endpoint := range []string{path, "/api/harness-sessions/live", "/api/harness-sessions/live?include_inactive=true"} {
			w := f.call(p, "GET", endpoint, nil, "")
			expect(t, w, 200)
			data := decode(t, w)
			if items, ok := data["items"].([]any); ok {
				if len(items) != 1 {
					t.Fatalf("expected one visible session from %s: %v", endpoint, items)
				}
				data = items[0].(map[string]any)
			}
			reasons := data["attention_reasons"].([]any)
			found := false
			blocking := false
			for _, value := range reasons {
				reason := value.(map[string]any)
				if reason["kind"] == "approval" {
					found = true
					blocking = reason["blocking"].(bool)
				}
			}
			if found != want || data["needs_attention"] != blocking || data["has_problem"] != false {
				t.Fatalf("approval evidence from %s: got %v, want visible=%v", endpoint, data, want)
			}
		}
	}
	for _, scenario := range []struct {
		name, kind        string
		resource          any
		reader, workspace bool
	}{
		{"other project node", "node", second, false, false},
		{"other project run", "run", otherRun, false, false},
		{"tenant proposal", "tenant", nil, false, true},
		{"unassigned node", "node", unassigned, false, true},
		{"same project node without run", "node", f.ticket, true, true},
		{"same project run resource", "run", sameRun, true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			approval := uid()
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO approval_requests(tenant_id,id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at)
                    VALUES($1,$2,$3,$3,'nodes.write',$4,$5,'Continue work',now()+interval '1 hour')`, f.person.TenantID, approval, f.agent.ID, scenario.kind, scenario.resource)
				return err
			})
			check(t, reader, scenario.reader)
			check(t, f.person, scenario.workspace)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'approved')`, f.person.TenantID, approval, f.person.ID)
				return err
			})
			check(t, reader, false)
		})
	}
}

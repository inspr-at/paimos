// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func historyNodeReader(f *fixture) tenant.Principal {
	p := projectPrincipal(f, "viewer")
	f.tx(func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(f.t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,$2,'History node reader') RETURNING id::text`, p.TenantID, "history_"+p.ID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(f.t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read')`, p.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(f.t.Context(), `UPDATE role_bindings SET role_id=$3 WHERE principal_id=$1 AND scope_id=$2`, p.ID, f.project, role)
		return err
	})
	return p
}

// Risk: false execution/success and an open state vocabulary in the logbook.
// Every current and legacy server state maps deliberately; new unknowns hold.
func TestRoutineRunStateMappings(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want RunState
	}{
		{"pending", RunCreated}, {"queued", RunCreated}, {"created", RunCreated},
		{"starting", RunRunning}, {"running", RunRunning},
		{"needs_person", RunWaitingForPerson}, {"waiting_for_person", RunWaitingForPerson},
		{"waiting", RunHeld}, {"held", RunHeld}, {"blocked", RunHeld}, {"paused", RunHeld},
		{"failed", RunFailed}, {"ownership_lost", RunFailed},
		{"succeeded", RunDone}, {"completed", RunDone}, {"done", RunDone},
		{"cancelled", RunSkipped}, {"skipped", RunSkipped}, {"future_state", RunHeld}, {"", RunHeld},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			state, reason := runState(tc.raw)
			if state != tc.want || (reason == "") != (state == RunDone) || strings.Count(reason, ".") > 1 {
				t.Fatalf("%q: %q %q", tc.raw, state, reason)
			}
		})
	}
}

// Risks: tenant/person/source leaks, repeated cumulative snapshots or root and
// child settlements charging twice, false green without exit, and unstable
// pagination. Existing broker tests prove settlement replay; this projection
// test retains real receipts and overlapping lineage and reads through RLS.
func TestRoutineRunHistoryPrivacyUsageAndPagination(t *testing.T) {
	t.Run("scope visibility and retired history", func(t *testing.T) {
		f := setup(t)
		in := intentInput(f)
		reader := projectPrincipal(f, "viewer")
		workspaceReader := viewer(f)
		for _, scope := range []DefinitionScope{{Kind: "project", ProjectID: f.project}, {Kind: "personal"}, {Kind: "workspace"}} {
			in.Definition.Scope = scope
			routine := f.create(in)
			run := f.manual(routine.ID, "scope").Run
			base := "/api/recurrences/" + routine.ID + "/runs"
			for _, p := range []tenant.Principal{reader, workspaceReader, f.p} {
				status := 200
				if scope.Kind == "personal" && p.ID != f.p.ID || scope.Kind == "workspace" && p.ID == reader.ID {
					status = 404
				}
				f.call(p, "GET", base, nil, status)
				f.call(p, "GET", base+"/"+run.ID, nil, status)
			}
			foreign := tenant.Principal{ID: f.p.ID, TenantID: "20000000-0000-4000-8000-000000000001", Kind: tenant.Person}
			f.call(foreign, "GET", base+"/"+run.ID, nil, 404)
			f.call(f.p, "DELETE", "/api/recurrences/"+routine.ID, map[string]any{"expected_revision": routine.Revision}, 204)
			var out RunHistory
			if err := json.Unmarshal(f.call(f.p, "GET", base+"/"+run.ID, nil, 200), &out); err != nil {
				t.Fatal(err)
			}
			if out.Run.State != RunCreated || out.Run.Reason == "" || out.Run.Usage.Tokens.Measured != nil || out.Run.Usage.PaidMicroUSD != nil || !out.Run.Usage.Partial {
				t.Fatalf("inert run became executed or free: %+v", out.Run)
			}
		}
	})
	t.Run("cumulative usage root settlement and redaction", func(t *testing.T) {
		f := setup(t)
		r := f.create(intentInput(f))
		run := f.manual(r.ID, "usage").Run
		base := "/api/recurrences/" + r.ID + "/runs/" + run.ID
		var session, attempt, action, account, grant, child, holdAgent, holdAttempt string
		f.tx(func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,assignment_digest,state)
 SELECT tenant_id,id,'build','build',agent_run_id,assignment_digest,'completed' FROM routine_runs WHERE id=$1 RETURNING id::text`, run.ID).Scan(&attempt); err != nil {
				return err
			}
			// Both attempts name the same immutable agent run; it is charged once.
			if _, err := tx.Exec(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,assignment_digest,state)
 SELECT tenant_id,id,'review','review',agent_run_id,assignment_digest,'completed' FROM routine_runs WHERE id=$1`, run.ID); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,harness,host,management,role,ref_digest,lease_digest)
 SELECT tenant_id,$2,agent_principal_id,id,'codex','synthetic','managed','worker',sha256('history-ref'::bytea),sha256('history-lease'::bytea) FROM agent_runs WHERE id=$1 RETURNING id::text`, run.AgentRunID, f.project).Scan(&session); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million) VALUES($1,'history-model',1,1,1,1)`, f.p.TenantID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage(tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,price_version,estimated_cost_usd,account_label,billing_mode)
 VALUES($1,$2,'history-model',2,80,20,0,false,1,0.1234,'PRIVATE ACCOUNT','api')`, f.p.TenantID, session); err != nil {
				return err
			}
			for i := 1; i <= 2; i++ {
				if _, err := tx.Exec(t.Context(), `INSERT INTO harness_usage_receipts(tenant_id,session_id,report_id,model,sequence,payload_digest) VALUES($1,$2,gen_random_uuid(),'history-model',$3,sha256('fixture'::bytea))`, f.p.TenantID, session, i); err != nil {
					return err
				}
			}
			if err := tx.QueryRow(t.Context(), `SELECT id::text FROM routine_actions WHERE run_id=$1`, run.ID).Scan(&action); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,billing_mode)
 SELECT tenant_id,'history','codex','synthetic',agent_principal_id,'PRIVATE ACCOUNT','api' FROM agent_runs WHERE id=$1 RETURNING id::text`, run.AgentRunID).Scan(&account); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO routine_budget_balances(tenant_id,run_id,owner_principal_id,mode,recovery_ceiling_ms,settled_tokens,settled_microusd,settled_ms,held_tokens,held_microusd,held_ms) VALUES($1,$2,$3,'off',1000,100,200,100,30,7,10)`, f.p.TenantID, run.ID, f.p.ID); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO routine_budget_grants(tenant_id,run_id,owner_principal_id,attempt_id,action_id,grant_key,agent_run_id,account_id,harness,billing_mode,maximum_tokens,maximum_microusd,maximum_ms,state,used_tokens,used_microusd,used_ms)
 VALUES($1,$2,$3,$4,$5,'root',$6,$7,'codex','api',100,200,100,'settled',100,200,100) RETURNING id::text`, f.p.TenantID, run.ID, f.p.ID, attempt, action, run.AgentRunID, account).Scan(&grant); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO routine_budget_grants(tenant_id,run_id,owner_principal_id,attempt_id,action_id,grant_key,parent_id,agent_run_id,account_id,harness,billing_mode,maximum_tokens,maximum_microusd,maximum_ms,state,used_tokens,used_microusd,used_ms)
 VALUES($1,$2,$3,$4,$5,'call',$8,$6,$7,'codex','api',100,200,100,'settled',100,200,100) RETURNING id::text`, f.p.TenantID, run.ID, f.p.ID, attempt, action, run.AgentRunID, account, grant).Scan(&child); err != nil {
				return err
			}
			for _, id := range []string{grant, child} {
				if _, err := tx.Exec(t.Context(), `INSERT INTO routine_budget_settlements(tenant_id,run_id,grant_id,event_key,receipt_digest,evidence_digest) VALUES($1,$2,$3,$3::text,repeat('a',64),repeat('b',64))`, f.p.TenantID, run.ID, id); err != nil {
					return err
				}
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id) SELECT tenant_id,work_order_id,agent_principal_id FROM agent_runs WHERE id=$1 RETURNING id::text`, run.AgentRunID).Scan(&holdAgent); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,assignment_digest,state) SELECT tenant_id,id,'held-evaluation','evaluation',$2,assignment_digest,'completed' FROM routine_runs WHERE id=$1 RETURNING id::text`, run.ID, holdAgent).Scan(&holdAttempt); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO routine_budget_grants(tenant_id,run_id,owner_principal_id,attempt_id,action_id,grant_key,agent_run_id,account_id,harness,billing_mode,maximum_tokens,maximum_microusd,maximum_ms,state)
 VALUES($1,$2,$3,$4,$5,'unknown-root',$6,$7,'codex','unknown',30,7,10,'unknown')`, f.p.TenantID, run.ID, f.p.ID, holdAttempt, action, holdAgent, account); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET state='completed' WHERE id=$1`, run.ID)
			return err
		})
		read := func() RunHistory {
			t.Helper()
			raw := f.call(f.p, "GET", base, nil, 200)
			if strings.Contains(string(raw), "PRIVATE ACCOUNT") || strings.Contains(string(raw), account) || strings.Contains(string(raw), "history-lease") {
				t.Fatal("private account/process data leaked")
			}
			var out RunHistory
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		for i := 0; i < 2; i++ {
			out := read()
			u := out.Run.Usage
			if u.Tokens.Measured == nil || *u.Tokens.Measured != "100" || u.PaidMicroUSD == nil || *u.PaidMicroUSD != "200" || u.EstimatedAPIUSD == nil || *u.EstimatedAPIUSD != "0.1234" || u.ReservedTokens == nil || *u.ReservedTokens != "30" {
				t.Fatalf("usage doubled or conflated: %+v", u)
			}
			if out.Run.State != RunHeld || out.Run.Reason == "" {
				t.Fatal("missing process evidence became green")
			}
		}
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE routine_attempts SET process_receipt='{"exit_confirmed":true,"exit_code":0,"private":"PRIVATE PROCESS"}' WHERE run_id=$1`, run.ID)
			return err
		})
		if out := read(); out.Run.State != RunDone || out.Run.Reason != "" {
			t.Fatalf("confirmed completion not projected: %+v", out.Run)
		}
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET state='failed' WHERE id=$1`, run.ID)
			return err
		})
		if out := read(); out.Run.State != RunFailed {
			t.Fatal("failed run became green")
		}
		// A reader can see the routine but cannot read foreign source/account data.
		reader := historyNodeReader(f)
		var out RunHistory
		raw := f.call(reader, "GET", base, nil, 200)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if !out.Run.Usage.Partial || out.Run.Usage.PaidMicroUSD != nil || out.Run.Usage.Tokens.Measured != nil {
			t.Fatal("usage authority was borrowed from definition visibility")
		}
		// A second session for the same run is ambiguous overlapping telemetry.
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,harness,host,management,role,ref_digest,lease_digest)
 SELECT tenant_id,project_id,agent_principal_id,run_id,harness,host,management,role,sha256('second-ref'::bytea),sha256('second-lease'::bytea) FROM harness_sessions WHERE id=$1`, session)
			return err
		})
		if out := read(); out.Run.Usage.Tokens.Measured != nil || !out.Run.Usage.Partial || out.Run.Usage.Tokens.Unknown == 0 {
			t.Fatal("overlapping cumulative sessions were added")
		}
	})
	t.Run("evaluation approval PR and foreign target references", func(t *testing.T) {
		f := setup(t)
		r := f.create(intentInput(f))
		run := f.manual(r.ID, "references").Run
		foreignProject := f.node("project", nil, "Unreadable source")
		foreignTarget := f.node("work", &foreignProject, "Private source content")
		var evaluation, approval, action string
		f.tx(func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO routine_guard_evaluations(tenant_id,recurrence_id,scope_kind,scope_project_id,owner_principal_id,output_project_id,definition_revision,checkpoint,policy_digest,context_digest,hard_version,preset_version,result,required_evaluation,decision)
 SELECT tenant_id,recurrence_id,scope_type,scope_project_id,owner_principal_id,output_project_id,definition_revision,'action',policy_digest,assignment_digest,'1','1','needs_person',true,'{"private":"PRIVATE EVALUATION"}' FROM routine_runs WHERE id=$1 RETURNING id::text`, run.ID).Scan(&evaluation); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,run_id,scope,resource_kind,resource_id,rationale,expires_at)
 SELECT tenant_id,agent_principal_id,agent_principal_id,id,'nodes.write','node',$2,'PRIVATE APPROVAL',now()+interval '1 day' FROM agent_runs WHERE id=$1 RETURNING id::text`, run.AgentRunID, f.parent).Scan(&approval); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO routine_actions(tenant_id,run_id,action_key,kind,request_digest,target_node_id,evaluation_id,approval_id,state,result)
 SELECT tenant_id,id,'reviewed','work.update',assignment_digest,$2,$3,$4,'needs_person','{"private":"PRIVATE RESULT"}' FROM routine_runs WHERE id=$1 RETURNING id::text`, run.ID, f.parent, evaluation, approval).Scan(&action); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO routine_actions(tenant_id,run_id,action_key,kind,request_digest,target_node_id,evaluation_id,state,result)
 SELECT tenant_id,id,'private-pr','pr.open',assignment_digest,$2,$3,'succeeded','{"pull_request_number":17,"private":"PRIVATE PR"}' FROM routine_runs WHERE id=$1`, run.ID, foreignTarget, evaluation)
			return err
		})
		path := "/api/recurrences/" + r.ID + "/runs/" + run.ID
		for _, p := range []tenant.Principal{f.p, historyNodeReader(f)} {
			raw := f.call(p, "GET", path, nil, 200)
			if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "Private source content") {
				t.Fatal("raw decision, source or approval content leaked")
			}
			var out RunHistory
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			foundAction, foundPR := false, false
			for _, v := range out.Actions.Items {
				if v.ID == action {
					foundAction = true
					if v.EvaluationID == nil || *v.EvaluationID != evaluation || v.EvaluationResult == nil || *v.EvaluationResult != "needs_person" {
						t.Fatal("visible evaluation result lost")
					}
					if p.ID == f.p.ID && (v.ApprovalID == nil || *v.ApprovalID != approval || v.ApprovalState == nil || *v.ApprovalState != "pending") {
						t.Fatal("authorized approval reference lost")
					}
					if p.ID != f.p.ID && v.ApprovalID != nil {
						t.Fatal("approval authority borrowed from node visibility")
					}
				}
				if v.Kind == "pr.open" {
					foundPR = true
					if p.ID == f.p.ID && (v.PullRequestNumber == nil || *v.PullRequestNumber != 17) {
						t.Fatal("recorded PR reference lost")
					}
					if p.ID != f.p.ID && (v.TargetID != nil || v.EvaluationID != nil || v.PullRequestNumber != nil) {
						t.Fatal("foreign source references leaked")
					}
				}
			}
			if !foundAction || !foundPR { t.Fatal("reference fixtures were absent from the assertion") }
		}
	})
	t.Run("immutable keysets and independent evidence cursors", func(t *testing.T) {
		f := setup(t)
		r := f.create(intentInput(f))
		base := "/api/recurrences/" + r.ID + "/runs"
		for i := 0; i < 3; i++ {
			f.manual(r.ID, fmt.Sprint(i))
		}
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET created_at=$2 WHERE recurrence_id=$1`, r.ID, f.now)
			return err
		})
		var page HistoryPage[RunHistoryEntry]
		if err := json.Unmarshal(f.call(f.p, "GET", base+"?limit=1", nil, 200), &page); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{page.Items[0].ID: true}
		if page.NextCursor == nil {
			t.Fatal("missing run continuation")
		}
		f.manual(r.ID, "newer")
		for page.NextCursor != nil {
			if err := json.Unmarshal(f.call(f.p, "GET", base+"?limit=1&before="+url.QueryEscape(*page.NextCursor), nil, 200), &page); err != nil {
				t.Fatal(err)
			}
			for _, v := range page.Items {
				if seen[v.ID] {
					t.Fatal("duplicate run across pages")
				}
				seen[v.ID] = true
			}
		}
		if len(seen) != 3 {
			t.Fatalf("unstable run continuation: %d", len(seen))
		}
		var runID string
		for id := range seen {
			runID = id
			break
		}
		f.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,assignment_digest,created_at)
 SELECT tenant_id,id,n::text,'build',assignment_digest,$2 FROM routine_runs CROSS JOIN generate_series(1,3) n WHERE id=$1`, runID, f.now)
			return err
		})
		var out RunHistory
		if err := json.Unmarshal(f.call(f.p, "GET", base+"/"+runID+"?limit=1", nil, 200), &out); err != nil {
			t.Fatal(err)
		}
		if out.Attempts.NextCursor == nil || len(out.Attempts.Items) != 1 {
			t.Fatal("attempt evidence unbounded or missing cursor")
		}
		cursor := url.QueryEscape(*out.Attempts.NextCursor)
		f.call(f.p, "GET", base+"/"+runID+"?action_before="+cursor, nil, 400)
		f.call(f.p, "GET", base+"/"+runID+"?attempt_before="+cursor, nil, 200)
		for _, query := range []string{"limit=51", "limit=0", "limit=1&limit=2", "before=invalid", "unknown=1"} {
			f.call(f.p, "GET", base+"?"+query, nil, 400)
		}
		f.call(f.p, "GET", base+"/"+r.ID, nil, 404)
	})
}

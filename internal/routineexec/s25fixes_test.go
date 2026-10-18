// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/routinebudget"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func s25Mount(t *testing.T, d *dispatchFixture) {
	t.Helper()
	d.e = New(d.d.App, d.options)
	runs := agentruns.NewWithLeadAdmission(d.d.App, d.options.LeadAdmission, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r agentruns.Run, _ agentruns.Telemetry, pending *[]events.Change) error {
		return agentaccounts.SettleDeferred(ctx, tx, p, r.ID, pending)
	})
	must(t, d.e.Bind(runs))
	d.mux = http.NewServeMux()
	runs.Mount(d.mux)
}

func s25Dispatch(t *testing.T, review bool) *dispatchFixture {
	t.Helper()
	d := setupDispatch(t)
	if review {
		prepareRoutineReviewer(t, d.fixture)
		d.options.Reviews = crossreview.New(d.d.App, nil)
	}
	s25Mount(t, d)
	must(t, d.tx(func(tx pgx.Tx) error {
		out, err := d.e.DispatchTx(t.Context(), tx, d.owner.TenantID, d.run, d.session, 1)
		if err == nil && !out.Routed {
			t.Fatalf("dispatch: %+v", out)
		}
		return err
	}))
	return d
}

func s25Request(t *testing.T, d *dispatchFixture, run, operation string, body any, g agentsetup.RoutineGrant, generation string, want int, reason string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	must(t, err)
	r := httptest.NewRequest("POST", "/api/runs/"+run+"/"+operation, strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(t.Context(), d.agent))
	r.Header.Set("Authorization", "Bearer "+d.token)
	r.Header.Set(agentpairing.LedgerGenerationHeader, g.LedgerGeneration)
	r.Header.Set(agentruns.DaemonHeader, g.DaemonID)
	r.Header.Set(agentruns.GenerationHeader, generation)
	r.Header.Set(reviewgate.PolicyHeader, reviewgate.Policy)
	w := httptest.NewRecorder()
	d.mux.ServeHTTP(w, r)
	if w.Code != want || reason != "" && !strings.Contains(w.Body.String(), reason) {
		t.Fatalf("%s: got %d %s; want %d %s", operation, w.Code, w.Body.String(), want, reason)
	}
	return w
}

func s25Claim(t *testing.T, d *dispatchFixture, run string) (agentsetup.RoutineGrant, map[string]any) {
	t.Helper()
	var g agentsetup.RoutineGrant
	must(t, d.tx(func(tx pgx.Tx) error { return loadActorGrant(t.Context(), tx, run, &g) }))
	rows, err := d.d.Admin.Query(t.Context(), `SELECT id::text FROM account_reservations WHERE run_id=$1 AND state='active' ORDER BY id`, run)
	must(t, err)
	ids := []string{}
	for rows.Next() {
		var id string
		must(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	must(t, rows.Err())
	rows.Close()
	worktree := strings.Repeat("7", 64)
	body := map[string]any{"daemon_id": g.DaemonID, "daemon_generation": g.DaemonGeneration, "reservation_ids": ids,
		"routine": agentsetup.RoutinePickup{LaunchID: g.LaunchID, WorktreeID: worktree, Capability: g.Capability, DaemonDigest: g.DaemonDigest, CapabilityDigest: g.CapabilityDigest, ComputerID: g.ComputerID}}
	var raw []byte
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT trace->'worker_assignment' FROM agent_runs WHERE id=$1`, run).Scan(&raw))
	if len(raw) > 0 {
		var h agentruns.WorkerHandoff
		must(t, json.Unmarshal(raw, &h))
		body["handoff"] = agentruns.WorkerPickup{TicketRevision: h.TicketRevision, WorkOrderRevision: h.WorkOrderRevision, BriefSHA256: h.BriefSHA256, WorktreeID: worktree}
	}
	s25Request(t, d, run, "claim", body, g, g.DaemonGeneration, 200, "")
	return g, body
}

func s25Started(t *testing.T, d *dispatchFixture, run string, g agentsetup.RoutineGrant) agentruns.Telemetry {
	t.Helper()
	var model string
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT p.model FROM agent_runs r JOIN model_profiles p ON p.id=r.model_profile_id WHERE r.id=$1`, run).Scan(&model))
	in := agentruns.Telemetry{Sequence: 1, Kind: "started", Model: model, Evidence: "vendor_reported"}
	s25Request(t, d, run, "telemetry", in, g, g.DaemonGeneration, 200, "")
	return in
}

func s25Settle(t *testing.T, d *dispatchFixture, run string) {
	t.Helper()
	var grant string
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM routine_budget_grants WHERE agent_run_id=$1 AND parent_id IS NULL`, run).Scan(&grant))
	must(t, d.tx(func(tx pgx.Tx) error {
		_, err := d.broker.SettleTx(t.Context(), tx, d.owner, d.run, grant, routinebudget.Settlement{EventKey: run, Usage: routinebudget.Amount{RecoveryMS: 1000}, UsageComplete: true, TokensKnown: true, PaidCostKnown: true, ExitConfirmed: true, EvidenceDigest: strings.Repeat("f", 64)})
		return err
	}))
}

// Risk: a lost reply becomes another start or uses a different persisted
// actor. Check the actual claim endpoint after restart, at a one-agent host
// ceiling, with changed binding and live admission negative controls.
func TestRoutineClaimReplayRetainsBindingAndLiveGuards(t *testing.T) {
	d := s25Dispatch(t, false)
	d.exec(`UPDATE agent_pairing_computers SET capacity_policy=jsonb_set(capacity_policy,'{maximum_agents}','1') WHERE principal_id=$1`, d.agent.ID)
	g, body := s25Claim(t, d, d.agentRun)
	s25Mount(t, d)
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 200, "")
	s25Started(t, d, d.agentRun, g)
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 200, "")
	var sibling string
	must(t, d.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status) SELECT tenant_id,work_order_id,agent_principal_id,model_profile_id,'running' FROM agent_runs WHERE id=$1 RETURNING id::text`, d.agentRun).Scan(&sibling))
	// An external assignment has no managed account binding and consumes no
	// host slot. Binding it to a second account must restore the host refusal.
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 200, "")
	// A separate paired account keeps each account's immutable one-run cap
	// intact, so the expected refusal proves the computer-wide ceiling.
	var siblingAccount string
	must(t, d.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id,capacity_owner,linked_at)
 SELECT tenant_id,account_key||'-sibling',harness,daemon_id,'Host guard sibling',registered_by_principal_id,owner_person_id,capacity_owner,linked_at
 FROM agent_accounts WHERE id=$1 RETURNING id::text`, g.AccountID).Scan(&siblingAccount))
	d.exec(`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at)
 SELECT tenant_id,$2,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at
 FROM agent_pairing_enrollments WHERE account_id=$1`, g.AccountID, siblingAccount)
	d.exec(`UPDATE agent_runs SET account_id=$2 WHERE id=$1`, sibling, siblingAccount)
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 409, "host_maximum_agents")
	d.exec(`UPDATE agent_runs SET status='cancelled' WHERE id=$1`, sibling)
	d.exec(`UPDATE routine_launch_intents SET binding=jsonb_set(binding,'{account_id}',to_jsonb($2::text)) WHERE id=$1`, g.LaunchID, d.project)
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 409, "routine_account_unbound")
	d.exec(`UPDATE routine_launch_intents SET binding=(SELECT trace->'routine_launch' FROM agent_runs WHERE id=$2) WHERE id=$1`, g.LaunchID, d.agentRun)
	d.mode.Store(2)
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 409, "main_red")
	d.mode.Store(0)
	d.exec(`UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{load}','30') WHERE principal_id=$1`, d.agent.ID)
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 409, "host_load")
	d.exec(`UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{load}','0') WHERE principal_id=$1`, d.agent.ID)
	d.exec(`UPDATE routine_budget_grants SET state='unknown' WHERE id=$1`, g.BudgetGrantID)
	s25Request(t, d, d.agentRun, "claim", body, g, g.DaemonGeneration, 409, "routine_grant_unavailable")
	var unchanged bool
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT
 (SELECT count(*)=1 FROM events WHERE type='run.claimed' AND node_id=(SELECT work_order_id FROM agent_runs WHERE id=$1))
 AND (SELECT count(*)=1 FROM routine_launch_evidence WHERE launch_id=$2 AND kind='claimed')
 AND (SELECT count(*)=1 FROM routine_attempts WHERE run_id=$3)
 AND (SELECT count(*)=1 FROM routine_budget_grants WHERE run_id=$3 AND parent_id IS NULL)
 AND (SELECT count(*)=1 FROM account_reservations WHERE run_id=$1 AND state='active')`, d.agentRun, g.LaunchID, d.run).Scan(&unchanged))
	if !unchanged {
		t.Fatal("claim replay changed its actor, budget, reservation or claim receipts")
	}
}

// Risk: rejected/stale telemetry enables action authority, or a start commits
// without its matching grant. Failed binding must roll back the complete
// telemetry write; exact replay must leave the original activation untouched.
func TestRoutineStartedTelemetryActivatesOnlyMatchingGrant(t *testing.T) {
	d := s25Dispatch(t, false)
	g, _ := s25Claim(t, d, d.agentRun)
	in := agentruns.Telemetry{Sequence: 1, Kind: "started"}
	s25Request(t, d, d.agentRun, "telemetry", in, g, "wrong-generation", 409, "daemon generation conflict")
	d.exec(`UPDATE routine_launch_intents SET binding=jsonb_set(binding,'{daemon_generation}',to_jsonb('wrong-generation'::text)) WHERE id=$1`, g.LaunchID)
	s25Request(t, d, d.agentRun, "telemetry", in, g, g.DaemonGeneration, 409, "routine_start_binding_changed")
	var rolledBack bool
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT ar.status='starting' AND r.state='dispatched' AND a.state='pending'
 AND NOT EXISTS(SELECT 1 FROM run_telemetry WHERE run_id=ar.id)
 AND NOT EXISTS(SELECT 1 FROM routine_launch_evidence WHERE launch_id=$2 AND kind='launched')
 FROM agent_runs ar JOIN routine_attempts a ON a.agent_run_id=ar.id JOIN routine_runs r ON r.id=a.run_id WHERE ar.id=$1`, d.agentRun, g.LaunchID).Scan(&rolledBack))
	if !rolledBack {
		t.Fatal("rejected start committed telemetry or action authority")
	}
	d.exec(`UPDATE routine_launch_intents SET binding=(SELECT trace->'routine_launch' FROM agent_runs WHERE id=$2) WHERE id=$1`, g.LaunchID, d.agentRun)
	s25Request(t, d, d.agentRun, "telemetry", in, g, g.DaemonGeneration, 200, "")
	s25Mount(t, d)
	s25Request(t, d, d.agentRun, "telemetry", in, g, g.DaemonGeneration, 200, "")
	var active bool
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT ar.status='running' AND r.state='running' AND a.state='running'
 AND (SELECT count(*)=1 FROM run_telemetry WHERE run_id=ar.id)
 AND (SELECT count(*)=1 FROM routine_launch_evidence WHERE launch_id=$2 AND kind='launched')
 FROM agent_runs ar JOIN routine_attempts a ON a.agent_run_id=ar.id JOIN routine_runs r ON r.id=a.run_id WHERE ar.id=$1`, d.agentRun, g.LaunchID).Scan(&active))
	if !active {
		t.Fatal("accepted start did not activate exactly its matching action grant")
	}
	s25Request(t, d, d.agentRun, "telemetry", agentruns.Telemetry{Sequence: 2, Kind: "finished", ProcessState: "exited"}, g, g.DaemonGeneration, 200, "")
	s25Request(t, d, d.agentRun, "telemetry", agentruns.Telemetry{Sequence: 3, Kind: "started"}, g, g.DaemonGeneration, 409, "run is not live")
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT state='completed' AND (SELECT count(*)=2 FROM run_telemetry WHERE run_id=$1) FROM routine_attempts WHERE agent_run_id=$1`, d.agentRun).Scan(&active))
	if !active {
		t.Fatal("stale start reactivated a completed action grant")
	}
}

// Risk: final handoff mistakes a completed reviewer for a new queued launch,
// or bypasses exact settlement/head/current CI proof. Run a real build/review
// continuation and retain the original receipt through each refusal.
func TestRoutineSettledReviewHandoffKeepsCurrentFacts(t *testing.T) {
	d := s25Dispatch(t, true)
	advance := func(run, reason string) map[string]any {
		t.Helper()
		var receipt string
		must(t, d.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM routine_effect_outbox WHERE effect_key=$1 AND run_id=$2`, "finish:"+run, d.run).Scan(&receipt))
		var advanced bool
		must(t, d.tx(func(tx pgx.Tx) error {
			var err error
			advanced, err = d.e.ContinueTx(t.Context(), tx, d.owner.TenantID, receipt)
			return err
		}))
		var raw []byte
		must(t, d.d.Admin.QueryRow(t.Context(), `SELECT result FROM routine_effect_outbox WHERE id=$1`, receipt).Scan(&raw))
		out := map[string]any{}
		must(t, json.Unmarshal(raw, &out))
		if advanced != (reason == "") || reason != "" && out["wait_reason"] != reason {
			t.Fatalf("handoff advanced=%v result=%+v want %s", advanced, out, reason)
		}
		return out
	}
	g, _ := s25Claim(t, d, d.agentRun)
	s25Started(t, d, d.agentRun, g)
	cr := &reviewgate.CommitRange{Repository: "example/routine", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
	s25Request(t, d, d.agentRun, "telemetry", agentruns.Telemetry{Sequence: 2, Kind: "finished", ProcessState: "exited", ReviewRange: cr}, g, g.DaemonGeneration, 200, "")
	s25Settle(t, d, d.agentRun)
	first := advance(d.agentRun, "")
	reviewer, order := first["agent_run_id"].(string), first["review_order_id"].(string)
	g, _ = s25Claim(t, d, reviewer)
	s25Started(t, d, reviewer, g)
	must(t, d.tx(func(tx pgx.Tx) error {
		var evidence string
		if err := tx.QueryRow(t.Context(), `INSERT INTO work_evidence(tenant_id,work_order_id,submitted_by_principal_id,kind,reference,run_id) VALUES($1,$2,$3,'text','VERDICT: ok',$4) RETURNING id::text`, d.owner.TenantID, order, d.agent.ID, reviewer).Scan(&evidence); err != nil {
			return err
		}
		return reviewgate.RecordEvidence(t.Context(), tx, order, evidence, d.agent.ID, reviewer, "text", "VERDICT: ok", g.DaemonID, g.DaemonGeneration)
	}))
	s25Request(t, d, reviewer, "telemetry", agentruns.Telemetry{Sequence: 2, Kind: "finished", ProcessState: "exited"}, g, g.DaemonGeneration, 200, "")
	advance(reviewer, "previous_actor_exit_or_usage_unconfirmed")
	s25Settle(t, d, reviewer)
	d.exec(`UPDATE work_order_reviews SET github_status='stale' WHERE work_order_id=$1`, order)
	advance(reviewer, "routine_review_proof_unavailable")
	d.exec(`UPDATE work_order_reviews SET github_status='unconfigured' WHERE work_order_id=$1`, order)
	d.mode.Store(2)
	advance(reviewer, "main_red")
	d.mode.Store(0)
	d.exec(`UPDATE routine_launch_intents SET binding=jsonb_set(binding,'{account_id}',to_jsonb($2::text)) WHERE id=$1`, g.LaunchID, d.project)
	advance(reviewer, "routine_account_unbound")
	d.exec(`UPDATE routine_launch_intents SET binding=(SELECT trace->'routine_launch' FROM agent_runs WHERE id=$2) WHERE id=$1`, g.LaunchID, reviewer)
	s25Mount(t, d)
	out := advance(reviewer, "")
	if out["state"] != "awaiting_pipeline" || out["head_sha"] != cr.HeadSHA {
		t.Fatalf("wrong final handoff: %+v", out)
	}
	// Settled completion grants no permission to launch again.
	err := d.tx(func(tx pgx.Tx) error { return d.e.LaunchChecks().Admission(t.Context(), tx, d.owner, g) })
	var refusal *workorders.Error
	if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Message != "routine_grant_unavailable" {
		t.Fatalf("completed actor re-admitted for launch: %v", err)
	}
	var stable bool
	must(t, d.d.Admin.QueryRow(t.Context(), `SELECT
 (SELECT count(*)=2 FROM routine_attempts WHERE run_id=$1)
 AND (SELECT count(*)=2 FROM routine_budget_grants WHERE run_id=$1 AND parent_id IS NULL)
 AND (SELECT count(*)=2 FROM routine_effect_outbox WHERE run_id=$1 AND kind='routine.continue' AND state='succeeded')
 AND NOT EXISTS(SELECT 1 FROM account_reservations r JOIN routine_attempts a ON a.agent_run_id=r.run_id WHERE a.run_id=$1 AND r.state='active')`, d.run).Scan(&stable))
	if !stable {
		t.Fatal("final handoff added an actor/hold or lost its exact completion receipt")
	}
}

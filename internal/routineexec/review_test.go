// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/routinebudget"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks: duplicate reviews/charges after replay or restart, unverified or stale
// review advancement, budget escape through child orders, and infinite failed
// fix pickup. Reuse the real consent, dispatch, telemetry, parser, budget and
// Decision Desk services; fixture process/usage receipts represent qualified
// adapter observations, never elapsed-time or sleep-based proof.
func TestRoutineReviewFixLineageAndFiniteEscalation(t *testing.T) {
	d := setupDispatch(t)
	f := d.fixture
	request := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	must(t, modelregistry.PrepareCatalog(t.Context(), f.d.App, f.owner, modelregistry.CatalogPreparation{Operation: modelregistry.CatalogRead, Request: request}))
	f.exec(`DELETE FROM model_role_routes WHERE profile_id<>$1`, f.profile)
	var reviewer, account string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'routine-reviewer','fixture','claude','anthropic','review-model','xhigh','frontier') RETURNING id::text`, f.owner.TenantID).Scan(&reviewer))
	f.exec(`INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state) VALUES($1,'review-gate',1,$2,'available')`, f.owner.TenantID, reviewer)
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id,capacity_owner,linked_at,last_probe_at,last_probe_ok,last_daemon_generation,max_parallel_runs) SELECT tenant_id,'review','claude',daemon_id,'Review',registered_by_principal_id,owner_person_id,capacity_owner,linked_at,clock_timestamp(),true,last_daemon_generation,1 FROM agent_accounts WHERE id=$1 RETURNING id::text`, f.account).Scan(&account))
	f.exec(`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at) SELECT tenant_id,$2,computer_id,request_id,$3,verification_expires_at,ongoing_approved_at FROM agent_pairing_enrollments WHERE account_id=$1`, f.account, account, reviewer)
	f.exec(`INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source) SELECT tenant_id,$2,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,'claude',capacity_source FROM account_allowance_windows WHERE account_id=$1`, f.account, account)
	f.exec(`INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) SELECT tenant_id,$2,window_kind,'claude',window_minutes,used_percent,resets_at,read_at,source FROM account_capacity_readings WHERE account_id=$1`, f.account, account)
	f.exec(`INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) SELECT tenant_id,principal_id,scope,$2::uuid::text,$2,schedule FROM account_capacity_schedules WHERE account_id=$1`, f.account, account)
	reviews := crossreview.New(f.d.App, nil)
	d.options.Reviews = reviews
	d.e = New(f.d.App, d.options)
	runs := agentruns.NewWithReviews(f.d.App, nil, reviews.RequestForRun, reviews.PrepareForRun)
	must(t, d.e.Bind(runs))
	runs.Mount(f.mux)
	var routed agentruns.RoutineQueueResult
	must(t, f.tx(func(tx pgx.Tx) error {
		var err error
		routed, err = d.e.DispatchTx(t.Context(), tx, f.owner.TenantID, f.run, d.session, 1)
		return err
	}))
	if !routed.Routed {
		t.Fatalf("build dispatch: %+v", routed)
	}

	report := func(run, status, verdict string, cr *reviewgate.CommitRange) string {
		t.Helper()
		f.exec(`UPDATE agent_runs ar SET status='running',daemon_id='budget-daemon',daemon_generation='budget-generation',started_at=clock_timestamp(),effective_model=p.model,model_evidence='vendor_reported' FROM model_profiles p WHERE ar.id=$1 AND p.id=ar.model_profile_id`, run)
		if verdict != "" {
			must(t, f.tx(func(tx pgx.Tx) error {
				var order, evidence string
				if err := tx.QueryRow(t.Context(), `SELECT work_order_id::text FROM agent_runs WHERE id=$1`, run).Scan(&order); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `INSERT INTO work_evidence(tenant_id,work_order_id,submitted_by_principal_id,kind,reference,run_id) VALUES($1,$2,$3,'text',$4,$5) RETURNING id::text`, f.owner.TenantID, order, f.agent.ID, verdict, run).Scan(&evidence); err != nil {
					return err
				}
				return reviewgate.RecordEvidence(t.Context(), tx, order, evidence, f.agent.ID, run, "text", verdict, "budget-daemon", "budget-generation")
			}))
		}
		payload := agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: status, ProcessState: "exited", ReviewRange: cr}
		raw, _ := json.Marshal(payload)
		call := func() {
			req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run+"/telemetry", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
			req.Header.Set("Authorization", "Bearer "+f.token)
			req.Header.Set(agentruns.DaemonHeader, "budget-daemon")
			req.Header.Set(agentruns.GenerationHeader, "budget-generation")
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("terminal telemetry: %d %s", w.Code, w.Body.String())
			}
		}
		call()
		call() // exact finish replay uses the existing telemetry dedupe
		var receipt string
		must(t, f.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM routine_effect_outbox WHERE run_id=$1 AND effect_key=$2`, f.run, "finish:"+run).Scan(&receipt))
		return receipt
	}
	settle := func(run string) {
		t.Helper()
		must(t, f.tx(func(tx pgx.Tx) error {
			var grant string
			if err := tx.QueryRow(t.Context(), `SELECT id::text FROM routine_budget_grants WHERE agent_run_id=$1 AND parent_id IS NULL`, run).Scan(&grant); err != nil {
				return err
			}
			in := routinebudget.Settlement{EventKey: run, Usage: routinebudget.Amount{RecoveryMS: 1000}, UsageComplete: true, TokensKnown: true, PaidCostKnown: true, ExitConfirmed: true, EvidenceDigest: strings.Repeat("f", 64)}
			_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, grant, in)
			return err
		}))
	}
	advance := func(receipt string, want bool) map[string]any {
		t.Helper()
		var advanced bool
		must(t, f.tx(func(tx pgx.Tx) error {
			var err error
			advanced, err = d.e.ContinueTx(t.Context(), tx, f.owner.TenantID, receipt)
			return err
		}))
		var raw []byte
		must(t, f.d.Admin.QueryRow(t.Context(), `SELECT coalesce(result,'{}') FROM routine_effect_outbox WHERE id=$1`, receipt).Scan(&raw))
		out := map[string]any{}
		must(t, json.Unmarshal(raw, &out))
		if advanced != want {
			t.Fatalf("advance=%v want %v; result=%+v", advanced, want, out)
		}
		return out
	}
	wait := func(receipt, reason string) {
		t.Helper()
		if out := advance(receipt, false); out["wait_reason"] != reason {
			t.Fatalf("wait=%+v want %s", out, reason)
		}
	}
	cr := reviewgate.CommitRange{Repository: "example/routine", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
	finished := report(d.agentRun, "completed", "", &cr)
	wait(finished, "previous_actor_exit_or_usage_unconfirmed")
	settle(d.agentRun)
	f.exec(`UPDATE agent_runs SET model_evidence='unverified' WHERE id=$1`, d.agentRun)
	wait(finished, "author_model_proof_unavailable")
	f.exec(`UPDATE agent_runs SET model_evidence='vendor_reported' WHERE id=$1`, d.agentRun)
	d.mode.Store(2)
	wait(finished, "main_red")
	d.mode.Store(0)
	f.exec(`UPDATE project_routine_execution_settings SET automatic_launch_enabled=false WHERE project_id=$1`, f.project)
	wait(finished, "main_red") // disabled execution retains the existing receipt
	var provisional int
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM routine_budget_grants WHERE run_id=$1`, f.run).Scan(&provisional))
	if provisional != 1 {
		t.Fatalf("disabled continuation created grants: %d", provisional)
	}
	f.exec(`UPDATE project_routine_execution_settings SET automatic_launch_enabled=true WHERE project_id=$1`, f.project)
	first := advance(finished, true)
	reviewRun := first["agent_run_id"].(string)
	reviewOrder := first["review_order_id"].(string)
	advance(finished, false)
	var count int
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM work_order_reviews WHERE ticket_node_id=$1`, f.work).Scan(&count))
	if count != 1 {
		t.Fatalf("duplicate review: %d", count)
	}
	var g agentsetup.RoutineGrant
	must(t, f.tx(func(tx pgx.Tx) error { return loadActorGrant(t.Context(), tx, reviewRun, &g) }))
	if g.Lineage == nil || g.Lineage.PreviousRunID != d.agentRun || g.RoutineRunID != f.run || g.Lineage.HeadSHA != cr.HeadSHA {
		t.Fatalf("lost lineage: %+v", g)
	}
	reviewFinished := report(reviewRun, "completed", "FINDING: high internal/example.go:1 Broken.\nVERDICT: changes", nil)
	settle(reviewRun)
	f.exec(`UPDATE work_order_reviews SET github_status='stale' WHERE work_order_id=$1`, reviewOrder)
	wait(reviewFinished, "routine_review_proof_unavailable")
	f.exec(`UPDATE work_order_reviews SET github_status='unconfigured' WHERE work_order_id=$1`, reviewOrder)
	f.exec(`UPDATE agent_runs SET effective_model='different-model' WHERE id=$1`, reviewRun)
	wait(reviewFinished, "routine_review_proof_unavailable")
	f.exec(`UPDATE agent_runs SET effective_model='review-model' WHERE id=$1`, reviewRun)
	f.exec(`UPDATE model_profiles SET family='openai' WHERE id=$1`, reviewer)
	wait(reviewFinished, "routine_review_proof_unavailable")
	f.exec(`UPDATE model_profiles SET family='anthropic' WHERE id=$1`, reviewer)
	f.exec(`UPDATE work_order_reviews SET head_sha=$2 WHERE work_order_id=$1`, reviewOrder, strings.Repeat("c", 40))
	wait(reviewFinished, "review_head_changed")
	f.exec(`UPDATE work_order_reviews SET head_sha=$2 WHERE work_order_id=$1`, reviewOrder, cr.HeadSHA)
	f.exec(`INSERT INTO cross_family_policies(tenant_id,project_id,mode,allowed_families,updated_by) VALUES($1,$2,'allowlist',ARRAY['anthropic'],$3)`, f.owner.TenantID, f.project, f.owner.ID)
	wait(reviewFinished, "review_policy_changed")
	f.exec(`DELETE FROM cross_family_policies WHERE project_id=$1`, f.project)
	// Reconstruct the executor: the persisted completion, budget and review are
	// sufficient; no in-memory counter grants another attempt.
	d.e = New(f.d.App, d.options)
	fix := advance(reviewFinished, true)
	fixRun := fix["agent_run_id"].(string)
	f.exec(`INSERT INTO model_situation_limits(tenant_id,fix_rounds) VALUES($1,1) ON CONFLICT(tenant_id) DO UPDATE SET fix_rounds=1`, f.owner.TenantID)
	advance(reviewFinished, false)
	must(t, f.tx(func(tx pgx.Tx) error {
		var lineage string
		var roots int
		if err := tx.QueryRow(t.Context(), `SELECT retry_of_run_id::text FROM agent_runs WHERE id=$1`, fixRun).Scan(&lineage); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_budget_grants WHERE run_id=$1 AND parent_id IS NULL`, f.run).Scan(&roots); err != nil {
			return err
		}
		if lineage != d.agentRun || roots != 3 {
			t.Fatalf("fix lineage/charges: %s %d", lineage, roots)
		}
		return nil
	}))
	fixFinished := report(fixRun, "failed", "", nil)
	settle(fixRun)
	escalated := advance(fixFinished, true)
	if escalated["state"] != "awaiting_decision" || escalated["question_id"] == nil {
		t.Fatalf("missing desk escalation: %+v", escalated)
	}
	advance(fixFinished, false)
	pass, err := d.e.RunTenant(t.Context(), f.owner.TenantID)
	must(t, err)
	if pass.Scanned != 0 {
		t.Fatalf("parked work immediately picked up: %+v", pass)
	}
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM desk_askers WHERE request_id=$1`, f.run).Scan(&count))
	if count != 1 {
		t.Fatalf("duplicate escalation: %d", count)
	}
}

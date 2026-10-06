// SPDX-License-Identifier: AGPL-3.0-only
package outcomes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/escalation"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestEscalationOutcomeReplayAndSingleQuestion(t *testing.T) {
	d := dbtest.Open(t)
	p := newPerson(t, d, "stuck-outcomes")
	project := insertNode(t, d, p, "project", "STK-1", "Project", nil)
	ticket := insertNode(t, d, p, "ticket", "STK-2", "Stuck work", &project)
	mod := New(d.App)
	mux := http.NewServeMux()
	mod.Mount(mux)
	record := func(key string, round int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/outcomes", strings.NewReader(fmt.Sprintf(`{"kind":"fix_round","ticket":%q,"payload":{"round":%d}}`, ticket, round)))
		r.Header.Set("Idempotency-Key", key)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	read := func() map[string]any {
		t.Helper()
		w := callAs(t, mod, p, "GET", "/api/nodes/"+ticket+"/escalation", "")
		if w.Code != 200 {
			t.Fatalf("state: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			State map[string]any `json:"escalation"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.State
	}
	if read() != nil {
		t.Fatal("invented state before evidence")
	}
	for _, round := range []int{1, 2, 3} {
		w := record(fmt.Sprintf("fix-round-%d", round), round)
		if w.Code != 201 {
			t.Fatalf("round %d: %d %s", round, w.Code, w.Body.String())
		}
	}
	before := read()
	if before["fix_rounds"] != float64(3) {
		t.Fatal(before)
	}
	if w := record("fix-round-3", 3); w.Code != 200 {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}
	after := read()
	if after["revision"] != before["revision"] {
		t.Fatal("replay advanced detector", after)
	}
	// With no configured stronger route, ask once, then keep recording failures.
	if before["status"] != "awaiting_decision" || before["question_id"] == nil {
		t.Fatal("missing terminal question", before)
	}
	for _, round := range []int{4, 5} {
		w := record(fmt.Sprintf("fix-round-%d", round), round)
		if w.Code != 201 {
			t.Fatalf("later round: %d %s", w.Code, w.Body.String())
		}
	}
	inTenant(t, d, p, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM desk_askers WHERE request_id=$1`, before["episode_id"]).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("%d duplicate questions", n)
		}
		// A pending human decision is a closed execution gate, even if callers
		// supply an unrelated profile or enough money.
		budget := int64(1)
		_, err := escalation.ReserveTx(t.Context(), tx, p, ticket, project, ticket, ticket, &budget)
		if err == nil || !strings.Contains(err.Error(), "Decision Desk") {
			t.Fatalf("wrong retry refusal: %v", err)
		}
		return nil
	})
	reported := httptest.NewRequest("POST", "/api/outcomes", strings.NewReader(fmt.Sprintf(`{"kind":"review_verdict","ticket":%q,"payload":{"verdict":"ok"}}`, ticket)))
	reported.Header.Set("Idempotency-Key", "unbound-ok-review")
	reported = reported.WithContext(tenant.WithPrincipal(reported.Context(), p))
	reportedResult := httptest.NewRecorder()
	mux.ServeHTTP(reportedResult, reported)
	if reportedResult.Code != 201 {
		t.Fatalf("reporting an outcome failed: %d %s", reportedResult.Code, reportedResult.Body.String())
	}
	if state := read(); state["status"] != "awaiting_decision" || state["episode_id"] != before["episode_id"] {
		t.Fatal("unverified ok reset episode budget", state)
	}
	foreign := newPerson(t, d, "stuck-foreign")
	if w := callAs(t, mod, foreign, "GET", "/api/nodes/"+ticket+"/escalation", ""); w.Code != 404 {
		t.Fatalf("foreign state: %d %s", w.Code, w.Body.String())
	}
}
func TestEscalationPayloadValidation(t *testing.T) {
	for _, tc := range []struct{ kind, body string }{
		{"review_verdict", `{"verdict":"changes","findings_fingerprint":"raw findings"}`},
		{"ci_result", `{"result":"fail","repo":"a/b","number":1,"attempt_id":"invalid"}`},
	} {
		if _, err := canonicalPayload(tc.kind, json.RawMessage(tc.body)); err == nil {
			t.Fatal("unbounded evidence accepted", tc)
		}
	}
	raw, err := canonicalPayload("review_verdict", json.RawMessage(`{"verdict":"changes","round":2,"findings_fingerprint":"`+strings.Repeat("a", 64)+`"}`))
	if err != nil || !strings.Contains(string(raw), "findings_fingerprint") {
		t.Fatal(string(raw), err)
	}
}

func TestEscalationRetryBudgetExitAndRollback(t *testing.T) {
	f := setupEscalationRetry(t)
	d, p, project, ticket, order, runner, opus, run, session := f.d, f.p, f.project, f.ticket, f.order, f.runner, f.opus, f.run, f.session
	attempt := func(cost *int64, rollback bool) error {
		return db.InTenant(tenant.WithPrincipal(t.Context(), p), d.App, p.TenantID, func(tx pgx.Tx) error {
			if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
				return err
			}
			rejection, err := escalation.ReserveTx(t.Context(), tx, p, ticket, project, run, opus, cost)
			if err != nil {
				return err
			}
			if rejection != nil {
				return rejection
			}
			if rollback {
				return errRetryRollback
			}
			return nil
		})
	}
	cost := int64(10_000_000)
	if err := attempt(&cost, false); err == nil || !strings.Contains(err.Error(), "exit must be proven") {
		t.Fatalf("lost contact accepted as exit: %v", err)
	}
	inTenant(t, d, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stop_reason='process_failed' WHERE id=$1`, session)
		return err
	})
	over := escalation.MaxCostMicros + 1
	if err := attempt(nil, false); err == nil || !strings.Contains(err.Error(), "cost and attempt budget") {
		t.Fatalf("unknown cost: %v", err)
	}
	if err := attempt(&over, false); err == nil || !strings.Contains(err.Error(), "cost and attempt budget") {
		t.Fatalf("over budget: %v", err)
	}
	if err := attempt(&cost, true); !errors.Is(err, errRetryRollback) {
		t.Fatalf("positive reservation did not reach rollback: %v", err)
	}
	inTenant(t, d, p, func(tx pgx.Tx) error {
		s, err := escalation.LoadTx(t.Context(), tx, ticket)
		if err != nil {
			return err
		}
		if s.Attempts != 0 || s.HeldCost != 0 {
			t.Fatal("rollback spent budget", s)
		}
		return nil
	})
	if err := attempt(&cost, false); err != nil {
		t.Fatal("qualified retry rejected", err)
	}
	inTenant(t, d, p, func(tx pgx.Tx) error {
		s, err := escalation.LoadTx(t.Context(), tx, ticket)
		if err != nil {
			return err
		}
		if s.Attempts != 1 || s.HeldCost != cost || len(s.UsedProfiles) != 1 {
			t.Fatal("attempt was not charged", s)
		}
		return nil
	})
	if err := attempt(&cost, false); err == nil || !strings.Contains(err.Error(), "current allowed escalation route") {
		t.Fatalf("same model repeated: %v", err)
	}
	reviewOrder := insertNode(t, d, p, "work_order", "RTY-4", "Independent review", &ticket)
	var reviewRun, evidenceID string
	inTenant(t, d, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed' WHERE id=$1`, run); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,kind,status) VALUES($1,$2,$3,'review','done')`, p.TenantID, reviewOrder, p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status,requested_model,effective_model,model_evidence,started_at,ended_at) VALUES($1,$2,$3,$4,'completed','opus','opus','vendor_reported',now()-interval '1 minute',now()) RETURNING id::text`, p.TenantID, reviewOrder, runner, opus).Scan(&reviewRun); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO work_evidence(tenant_id,work_order_id,run_id,submitted_by_principal_id,kind,reference) VALUES($1,$2,$3,$4,'text','VERDICT: ok') RETURNING id::text`, p.TenantID, reviewOrder, reviewRun, runner).Scan(&evidenceID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO work_order_reviews(tenant_id,work_order_id,ticket_node_id,request_id,request,ticket_snapshot,repository,base_sha,head_sha,author_run_id,author_family,reviewer_profile_id,reviewer_family,run_id,evidence_id,result) VALUES($1,$2,$3,gen_random_uuid(),'{}','Fixture work','fixture/review',$4,$5,$6,'openai',$7,'anthropic',$8,$9,'{"verdict":"ok","findings":[],"reason":""}')`, p.TenantID, reviewOrder, ticket, strings.Repeat("a", 40), strings.Repeat("b", 40), run, opus, reviewRun, evidenceID)
		return err
	})
	inTenant(t, d, p, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"verdict": "ok", "review_id": reviewOrder})
		if err := escalation.ObserveTx(t.Context(), tx, p, ticket, project, ticket, "review_verdict", raw); err != nil {
			return err
		}
		state, err := escalation.LoadTx(t.Context(), tx, ticket)
		if err != nil {
			return err
		}
		if state.Status != "resolved" || state.Attempts != 1 || state.HeldCost != cost {
			t.Fatal("verified review failed to resolve without erasing evidence", state)
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_order_reviews SET github_status='stale' WHERE work_order_id=$1`, reviewOrder); err != nil {
			return err
		}
		verified, err := reviewgate.VerifiedLatestTx(t.Context(), tx, reviewOrder, ticket)
		if err != nil {
			return err
		}
		if verified {
			t.Fatal("stale exact-range review accepted")
		}
		state.Status = "stuck"
		state.HeldCost = 15_000_000
		raw, _ = json.Marshal(state)
		_, err = tx.Exec(t.Context(), `UPDATE work_escalations SET state=$2 WHERE ticket_node_id=$1`, ticket, raw)
		return err
	})
	// The ordinary retry endpoint commits the human question on a budget
	// refusal, while returning an honest conflict and creating no run.
	body, _ := json.Marshal(map[string]any{"agent_principal_id": runner, "model_profile_id": opus, "retry_of_run_id": run})
	for i := 0; i < 2; i++ {
		denied := callAs(t, agentruns.New(d.App), p, "POST", "/api/work-orders/"+order+"/runs", string(body))
		if denied.Code != 409 || !strings.Contains(denied.Body.String(), "Decision Desk") {
			t.Fatalf("budget refusal: %d %s", denied.Code, denied.Body.String())
		}
	}
	inTenant(t, d, p, func(tx pgx.Tx) error {
		state, err := escalation.LoadTx(t.Context(), tx, ticket)
		if err != nil {
			return err
		}
		if state.Status != "awaiting_decision" || state.QuestionID == "" || state.Attempts != 1 || state.HeldCost != 15_000_000 {
			t.Fatal("budget refusal was not durably recorded", state)
		}
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM agent_runs WHERE work_order_id=$1`, order).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("refused retry created a run", n)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM desk_askers WHERE request_id=$1`, state.EpisodeID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("budget refusal duplicated human question", n)
		}
		return nil
	})
}

var errRetryRollback = errors.New("retry fixture rollback")

type escalationRetryFixture struct {
	d                                                           *dbtest.DB
	p                                                           tenant.Principal
	project, ticket, order, runner, opus, run, session, account string
}

func setupEscalationRetry(t *testing.T) escalationRetryFixture {
	t.Helper()
	d := dbtest.Open(t)
	p := newPerson(t, d, "stuck-retry")
	project := insertNode(t, d, p, "project", "RTY-1", "Project", nil)
	ticket := insertNode(t, d, p, "ticket", "RTY-2", "Stuck work", &project)
	order := insertNode(t, d, p, "work_order", "RTY-3", "Fix", &ticket)
	// Seed the real registry, then register only a qualified Claude account.
	w := callAs(t, modelregistry.New(d.App), p, "GET", "/api/models", "")
	if w.Code != 200 {
		t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
	}
	var runner, account, opus, sol, run, session string
	inTenant(t, d, p, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Retry runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh'`).Scan(&opus); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='codex-6-1-sol-high'`).Scan(&sol); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,allowed_model_profile_ids) VALUES($1,'retry-claude','claude','retry-runner',$2,'Claude',now(),true,'generation',$3,ARRAY[$4::uuid]) RETURNING id::text`, p.TenantID, runner, p.ID, opus).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,capacity_kind,capacity_source,capacity_read_at) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted','5h','agentd',now())`, p.TenantID, account); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override = "sprint"
		schedule.Reserve = capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status,max_cost_micros) VALUES($1,$2,$3,'ready',10000000)`, p.TenantID, order, p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status,requested_model,started_at,ended_at) VALUES($1,$2,$3,$4,'failed','gpt-6.1-sol',now()-interval '1 minute',now()) RETURNING id::text`, p.TenantID, order, runner, sol).Scan(&run); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,ticket_node_id,work_order_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,stopped_at,stop_reason,model,reasoning_effort,model_profile_id) VALUES($1,$2,$3,$4,$5,$6,'codex','fixture','managed','worker','ship',decode(repeat('01',32),'hex'),decode(repeat('02',32),'hex'),'stopped',now(),'lost_contact','gpt-6.1-sol','high',$7) RETURNING id::text`, p.TenantID, project, runner, run, ticket, order, sol).Scan(&session); err != nil {
			return err
		}
		state := escalation.State{EpisodeID: ticket, Status: "stuck", Reason: "failed_fix_rounds", Revision: 1, MaxAttempts: 2, MaxCost: escalation.MaxCostMicros}
		raw, _ = json.Marshal(state)
		_, err := tx.Exec(t.Context(), `INSERT INTO work_escalations(tenant_id,ticket_node_id,project_id,state) VALUES($1,$2,$3,$4)`, p.TenantID, ticket, project, raw)
		return err
	})

	return escalationRetryFixture{d, p, project, ticket, order, runner, opus, run, session, account}
}

func TestEscalationRetryFreezesReservedPlacement(t *testing.T) {
	f := setupEscalationRetry(t)
	inTenant(t, f.d, f.p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET stop_reason='process_failed' WHERE id=$1`, f.session); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"route_role":"build","area":"backend"}' WHERE id=$1`, f.ticket)
		return err
	})
	body, _ := json.Marshal(map[string]any{"agent_principal_id": f.runner, "model_profile_id": f.opus, "retry_of_run_id": f.run})
	w := callAs(t, agentruns.New(f.d.App), f.p, "POST", "/api/work-orders/"+f.order+"/runs", string(body))
	if w.Code != 201 {
		t.Fatalf("retry: %d %s", w.Code, w.Body.String())
	}
	var run agentruns.Run
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	var trace struct {
		Placement modelregistry.WorkPlacement `json:"work_placement"`
		State     escalation.State            `json:"escalation"`
	}
	if err := json.Unmarshal(run.Trace, &trace); err != nil {
		t.Fatal(err)
	}
	if run.ProfileID == nil || *run.ProfileID != f.opus || trace.Placement.PlannedProfileID == nil || *trace.Placement.PlannedProfileID != f.opus || trace.Placement.Blocked != "" || len(trace.Placement.QualifyingAccountIDs) != 1 {
		t.Fatalf("frozen placement did not describe admitted profile: run=%s trace=%s", w.Body.String(), run.Trace)
	}
	if trace.State.Attempts != 1 || trace.State.HeldCost != 10000000 {
		t.Fatal("retry not charged", trace.State)
	}
	// The recorded reservation must also survive the live claim gate, while a
	// human-decision transition after reservation closes that same launch.
	dbtest.BindRole(t, f.d, f.p.TenantID, f.runner, "member")
	agent := tenant.Principal{TenantID: f.p.TenantID, ID: f.runner, Kind: tenant.Agent, Scopes: []string{"run.claim", "run.read"}}
	sum := sha256.Sum256([]byte(f.ticket))
	inTenant(t, f.d, f.p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'retry test','retryfixture',$3,ARRAY['run.claim'])`, f.p.TenantID, f.runner, hex.EncodeToString(sum[:]))
		return err
	})
	claim := func(body string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		agentruns.New(f.d.App).Mount(mux)
		r := httptest.NewRequest("POST", "/api/runs/"+run.ID+"/claim", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer aeon_retryfixture_"+f.ticket)
		r = r.WithContext(tenant.WithPrincipal(r.Context(), agent))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	body, _ = json.Marshal(map[string]any{"run_id": run.ID, "daemon_id": "retry-runner", "account_ids": []string{f.account}, "estimated_units": map[string]int{"requests": 1}})
	routed := callAs(t, agentaccounts.New(f.d.App), agent, "POST", "/api/agent-accounts/route", string(body))
	if routed.Code != 200 {
		t.Fatalf("account reservation: %d %s", routed.Code, routed.Body.String())
	}
	var route agentaccounts.RouteResult
	if err := json.Unmarshal(routed.Body.Bytes(), &route); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range route.Reservations {
		ids = append(ids, r.ReservationID)
	}
	body, _ = json.Marshal(map[string]any{"daemon_id": "retry-runner", "daemon_generation": "generation", "reservation_ids": ids})
	inTenant(t, f.d, f.p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_escalations SET state=jsonb_set(state,'{status}','"awaiting_decision"') WHERE ticket_node_id=$1`, f.ticket)
		return err
	})
	denied := claim(string(body))
	if denied.Code != 409 || !strings.Contains(denied.Body.String(), "Decision Desk") {
		t.Fatalf("reserved retry ignored live decision gate: %d %s", denied.Code, denied.Body.String())
	}
	inTenant(t, f.d, f.p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_escalations SET state=jsonb_set(state,'{status}','"stuck"') WHERE ticket_node_id=$1`, f.ticket)
		return err
	})
	claimed := claim(string(body))
	if claimed.Code != 200 {
		t.Fatalf("charged retry claim: %d %s", claimed.Code, claimed.Body.String())
	}
	if err := json.Unmarshal(claimed.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "starting" {
		t.Fatal("charged retry failed to launch", run.ID, run.Status)
	}
}

func TestEscalationRetainsOriginalModelAcrossSelections(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			f := setupEscalationRetry(t)
			inTenant(t, f.d, f.p, func(tx pgx.Tx) error {
				var fable string
				if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='claude-fable-xhigh'`).Scan(&fable); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model_profile_id=$2,model='fable',reasoning_effort='xhigh',stop_reason='process_failed' WHERE id=$1`, f.session, fable); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET model_profile_id=$2,requested_model='fable' WHERE id=$1`, f.run, fable); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET allowed_model_profile_ids=ARRAY[$1::uuid,$2::uuid] WHERE account_key='retry-claude'`, f.opus, fable); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state,reason) VALUES($1,'build-hard',99,$2,'available','Explicit fixture build authority')`, f.p.TenantID, fable); err != nil {
					return err
				}
				// Observe first while Fable is the original writer. It must survive a later session.
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				if err := escalation.ObserveTx(t.Context(), tx, f.p, f.ticket, f.project, f.ticket, "fix_round", json.RawMessage(`{"round":3}`)); err != nil {
					return err
				}
				return nil
			})
			inTenant(t, f.d, f.p, func(tx pgx.Tx) error {
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				cost := int64(10000000)
				rejection, err := escalation.ReserveTx(t.Context(), tx, f.p, f.ticket, f.project, f.run, f.opus, &cost)
				if err != nil {
					return err
				}
				if rejection != nil {
					return rejection
				}
				state, err := escalation.LoadTx(t.Context(), tx, f.ticket)
				if err != nil {
					return err
				}
				raw, _ := json.Marshal(map[string]any{"escalation": state.Public()})
				var retry string
				if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status,requested_model,started_at,ended_at,retry_of_run_id,trace) VALUES($1,$2,$3,$4,'failed','opus',now(),now(),$5,$6) RETURNING id::text`, f.p.TenantID, f.order, f.runner, f.opus, f.run, raw).Scan(&retry); err != nil {
					return err
				}
				if legacy {
					if _, err := tx.Exec(t.Context(), `UPDATE work_escalations SET state=state-'original_profile_id' WHERE ticket_node_id=$1`, f.ticket); err != nil {
						return err
					}
				}
				_, err = tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,stopped_at,stop_reason,model,reasoning_effort,model_profile_id,created_at,run_id) VALUES($1,$2,$3,$4,'claude','fixture','managed','worker','ship',decode(repeat('03',32),'hex'),decode(repeat('04',32),'hex'),'stopped',now(),'process_failed','opus','xhigh',$5,now()+interval '1 second',$6)`, f.p.TenantID, f.project, f.runner, f.ticket, f.opus, retry)
				return err
			})
			inTenant(t, f.d, f.p, func(tx pgx.Tx) error {
				q := modelregistry.WorkQuery{TicketID: f.ticket, Role: "build", Area: "backend"}
				pick, err := modelregistry.ResolveWork(t.Context(), tx, f.p, q, time.Now())
				if err != nil {
					return err
				}
				if pick.Profile != nil || pick.Trace.Blocked != "no allowed stronger route" {
					t.Fatal("original model returned to routing", pick)
				}
				if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
					return err
				}
				if err := escalation.ObserveTx(t.Context(), tx, f.p, f.ticket, f.project, f.ticket, "fix_round", json.RawMessage(`{"round":4}`)); err != nil {
					return err
				}
				state, err := escalation.LoadTx(t.Context(), tx, f.ticket)
				if err != nil {
					return err
				}
				if state.Status != "awaiting_decision" || state.PlannedProfile != "" {
					t.Fatal("original model returned to episode plan", state)
				}
				return nil
			})
		})
	}
}

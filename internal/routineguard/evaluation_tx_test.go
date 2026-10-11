// SPDX-License-Identifier: AGPL-3.0-only
package routineguard_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks R1/R7/R15: a verdict request not surviving before dispatch, replay
// clearing an exact hold, changed actions reusing evidence, and owner revocation
// authorizing a final write. Real RLS/fences are exercised; no model is launched.
func TestEvaluationRequestPersistsBeforeDispatchAndRechecksOwner(t *testing.T) {
	f := newEvaluationFixture(t)
	d, ctx, p, agent := f.db, dbtest.Seed(t.Context()), f.person, f.agent
	b, c, in := f.binding, f.context, f.in
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	prepare := func(at time.Time) (routineguard.EvaluationRequest, error) {
		var request routineguard.EvaluationRequest
		err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTree(t.Context(), tx, p.TenantID); err != nil {
				return err
			}
			var err error
			request, err = routineguard.PrepareEvaluationTx(t.Context(), tx, agent, b, c, at)
			return err
		})
		return request, err
	}
	first, err := prepare(now)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := prepare(now.Add(time.Second))
	if err != nil || replayed.Digest != first.Digest {
		t.Fatalf("request replay changed: %+v %v", replayed, err)
	}
	in(func(tx pgx.Tx) error {
		var count int
		var pending bool
		err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM routine_guard_evaluations WHERE checkpoint='action'),a.state='pending' AND a.approval_id IS NULL AND e.decision#>>'{request,digest}'=$2 AND e.decision->>'wait_reason'='evaluator_unavailable' FROM routine_actions a JOIN routine_guard_evaluations e ON e.id=a.evaluation_id AND e.tenant_id=a.tenant_id WHERE a.id=$1`, b.ActionID, first.Digest).Scan(&count, &pending)
		if err == nil && (count != 1 || !pending) {
			t.Fatal("request not durable/default-off or replay duplicated")
		}
		return err
	})
	// Changed payload/head requires a new request and keeps old audit evidence.
	in(func(tx pgx.Tx) error {
		b.PayloadDigest, b.HeadSHA = strings.Repeat("c", 64), strings.Repeat("d", 40)
		_, err := tx.Exec(t.Context(), `UPDATE routine_actions SET request_digest=$2 WHERE id=$1`, b.ActionID, b.PayloadDigest)
		return err
	})
	changed, err := prepare(now.Add(2 * time.Second))
	if err != nil || changed.Digest == first.Digest {
		t.Fatalf("changed action reused request: %v", err)
	}
	in(func(tx pgx.Tx) error {
		if err := db.LockTree(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		pending := []events.Change{}
		decision, err := routineguard.RecordEvaluationTx(t.Context(), tx, agent, b, c, b.AuthorRunID, []byte("completed without an explicit verdict"), now.Add(3*time.Second), &pending)
		if err == nil && (decision.CanExecute() || len(pending) != 0) {
			t.Fatal("missing result created execution or person approval")
		}
		return err
	})
	in(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET updated_at=updated_at+interval '1 second' WHERE id=$1`, b.TargetID)
		return err
	})
	if _, err = prepare(now.Add(4 * time.Second)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("changed live target wrong failure: %v", err)
	}
	in(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, b.TargetID).Scan(&b.TargetUpdatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID)
		return err
	})
	if _, err = prepare(now.Add(4 * time.Second)); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revoked owner wrong failure: %v", err)
	}
	in(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_guard_evaluations WHERE checkpoint='action'`).Scan(&count)
		if err == nil && count != 3 {
			t.Fatal("revoked owner left evaluation write")
		}
		return err
	})
}

// Each fixture retains its action, author and immutable attempt evidence.
// All simulated time passes through the evaluator's injected clock.
type evaluationFixture struct {
	db            *dbtest.DB
	person, agent tenant.Principal
	binding       routineguard.ActionBinding
	context       routineguard.Context
	in            func(func(pgx.Tx) error)
}

func newEvaluationFixture(t *testing.T) *evaluationFixture {
	t.Helper()
	d := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	p := tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person}
	agent := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.write", "harness.control", "models.read"}}
	var project, parent, workKind, backupOwner string
	in := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, d.App, p.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	in(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'evaluation','Evaluation')`, p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Backup owner') RETURNING id::text`, p.TenantID).Scan(&backupOwner); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Coordinator') RETURNING id::text`, p.TenantID).Scan(&agent.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Evaluation' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Assigned work',$2,$2 FROM node_kinds WHERE slug='work' RETURNING id::text`, p.TenantID, project).Scan(&parent); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, p.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL AND archived_at IS NULL`).Scan(&workKind)
	})
	dbtest.BindRole(t, d, p.TenantID, p.ID, "owner")
	dbtest.BindRole(t, d, p.TenantID, backupOwner, "owner")
	dbtest.BindRole(t, d, p.TenantID, agent.ID, "admin")
	mux := http.NewServeMux()
	recurrences.New(d.App).Mount(mux)
	modelregistry.New(d.App).Mount(mux)
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, out.Code, out.Body.String())
		}
		return out.Body.Bytes()
	}
	call("GET", "/api/models", nil, 200)
	input := recurrences.Input{ProjectID: project, ParentID: parent, Template: recurrences.Template{Title: "Inspect assigned source", Description: "Read code", Criteria: []string{"Report findings"}, EstimateHours: 1, Priority: "high", Type: "work"}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "Europe/Vienna"}, QueueEach: true, OverlapPolicy: "create", CatchUpPolicy: "one", Definition: &recurrences.Definition{Scope: recurrences.DefinitionScope{Kind: "project", ProjectID: project}, OwnerPrincipalID: p.ID, Assignment: &recurrences.Assignment{Goal: "Review assigned source", Role: "build", WorkKindID: workKind, Sources: []recurrences.SourceReference{}, AllowedActions: []string{"pr.open"}, RuntimeRequirements: recurrences.RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: recurrences.DefinitionBudget{Mode: "off"}}}}
	var recurrence recurrences.Recurrence
	if err := json.Unmarshal(call("POST", "/api/recurrences", input, 201), &recurrence); err != nil {
		t.Fatal(err)
	}
	var occurrence recurrences.Occurrence
	if err := json.Unmarshal(call("POST", "/api/recurrences/"+recurrence.ID+"/run-now", map[string]string{"idempotency_key": "evaluation"}, 200), &occurrence); err != nil {
		t.Fatal(err)
	}
	if occurrence.Run == nil || occurrence.Run.AgentRunID == nil || occurrence.NodeID == nil {
		t.Fatal("missing persisted run fixture")
	}
	b := routineguard.ActionBinding{TenantID: p.TenantID, RunID: occurrence.Run.ID, AuthorRunID: *occurrence.Run.AgentRunID, ProjectID: project, TargetID: *occurrence.NodeID, TargetRevision: 1, Kind: "pr.open", PayloadDigest: strings.Repeat("a", 64), HeadSHA: strings.Repeat("b", 40)}
	in(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, b.TargetID).Scan(&b.TargetUpdatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET model_profile_id=(SELECT id FROM model_profiles WHERE harness='codex' AND enabled ORDER BY created_at,id LIMIT 1),requested_model=(SELECT model FROM model_profiles WHERE harness='codex' AND enabled ORDER BY created_at,id LIMIT 1),effective_model=(SELECT model FROM model_profiles WHERE harness='codex' AND enabled ORDER BY created_at,id LIMIT 1),model_evidence='vendor_reported' WHERE id=$1`, b.AuthorRunID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO routine_actions(tenant_id,run_id,action_key,kind,request_digest,target_node_id,target_revision) VALUES($1,$2,'evaluate','pr.open',$3,$4,1) RETURNING id::text`, p.TenantID, b.RunID, b.PayloadDigest, b.TargetID).Scan(&b.ActionID)
	})
	c := routineguard.Context{Checkpoint: "action", Action: b.Kind, Text: "Inspect assigned source", PayloadBytes: 100}
	return &evaluationFixture{db: d, person: p, agent: agent, binding: b, context: c, in: in}
}

func (f *evaluationFixture) write(t *testing.T, fn func(pgx.Tx) error) error {
	t.Helper()
	return db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (f *evaluationFixture) prepare(t *testing.T, now time.Time) (routineguard.EvaluationRequest, error) {
	t.Helper()
	var request routineguard.EvaluationRequest
	err := f.write(t, func(tx pgx.Tx) error {
		var err error
		request, err = routineguard.PrepareEvaluationTx(t.Context(), tx, f.agent, f.binding, f.context, now)
		return err
	})
	return request, err
}

func (f *evaluationFixture) record(t *testing.T, request routineguard.EvaluationRequest, run string, verdict routineguard.Outcome, now time.Time) (routineguard.Decision, error) {
	t.Helper()
	var decision routineguard.Decision
	err := f.write(t, func(tx pgx.Tx) error {
		pending := []events.Change{}
		output, err := json.Marshal(routineguard.StructuredVerdict{RequestDigest: request.Digest, Verdict: verdict, Reason: "Inspected the exact action"})
		if err != nil {
			return err
		}
		decision, err = routineguard.RecordEvaluationTx(t.Context(), tx, f.agent, f.binding, f.context, run, output, now, &pending)
		if err != nil {
			return err
		}
		for _, change := range pending {
			if _, err = events.Append(t.Context(), tx, f.agent, change); err != nil {
				return err
			}
		}
		return nil
	})
	return decision, err
}

func (f *evaluationFixture) reviewer(t *testing.T, now time.Time) {
	t.Helper()
	f.in(func(tx pgx.Tx) error {
		var profile, account string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh' AND enabled`).Scan(&profile); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,allowed_model_profile_ids) VALUES($1,'evaluation-fixture','claude','fixture',$2,'Fixture reviewer',$3,true,'fixture',$4,ARRAY[$5::uuid]) RETURNING id::text`, f.person.TenantID, f.agent.ID, now, f.person.ID, profile).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,$3,$4,'requests',1000,'unrestricted')`, f.person.TenantID, account, now.Add(-time.Hour), now.Add(48*time.Hour)); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, f.person.TenantID, f.person.ID, account, raw)
		return err
	})
}

func (f *evaluationFixture) attempt(t *testing.T, key, role, profile, account, digest string, receipt any) (string, string) {
	t.Helper()
	var run, attempt string
	f.in(func(tx pgx.Tx) error {
		var work string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Fixture attempt',$2,$3 FROM node_kinds WHERE slug='work_order' RETURNING id::text`, f.person.TenantID, f.binding.TargetID, f.binding.ProjectID).Scan(&work); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,kind) VALUES($1,$2,$3,'review')`, f.person.TenantID, work, f.person.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,account_id,status,requested_model,effective_model,model_evidence) SELECT $1,$2,$3,id,nullif($5,'')::uuid,'completed',model,model,'vendor_reported' FROM model_profiles WHERE id=$4 RETURNING id::text`, f.person.TenantID, work, f.agent.ID, profile, account).Scan(&run); err != nil {
			return err
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,assignment_digest,process_receipt) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, f.person.TenantID, f.binding.RunID, key, role, run, digest, raw).Scan(&attempt)
	})
	return run, attempt
}

func (f *evaluationFixture) evaluator(t *testing.T, request routineguard.EvaluationRequest, key string) string {
	t.Helper()
	grant := routineguard.EvaluationGrant{HoldID: "10000000-0000-4000-8000-000000000099", RequestDigest: request.Digest, Capability: routineguard.EvaluationCapability, InputTokens: 1000, OutputTokens: 1000, Deadline: request.Deadline}
	run, _ := f.attempt(t, key, "evaluation", request.ProfileID, request.AccountID, request.Digest, map[string]any{"evaluation_grant": grant})
	return run
}

func (f *evaluationFixture) action(t *testing.T) (string, string, *string, []byte) {
	t.Helper()
	var state, evaluation string
	var approval *string
	var raw []byte
	f.in(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT a.state,a.evaluation_id::text,a.approval_id::text,e.decision FROM routine_actions a JOIN routine_guard_evaluations e ON e.tenant_id=a.tenant_id AND e.id=a.evaluation_id WHERE a.id=$1`, f.binding.ActionID).Scan(&state, &evaluation, &approval, &raw)
	})
	return state, evaluation, approval, raw
}

// Risk R7: a completed needs-person evaluation expires while a person is
// deciding. Approval time must not be mistaken for evaluator completion time.
func TestEvaluationPersonHoldSurvivesEvaluatorDeadline(t *testing.T) {
	f := newEvaluationFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	f.reviewer(t, now)
	request, err := f.prepare(t, now)
	if err != nil {
		t.Fatal(err)
	}
	run := f.evaluator(t, request, "EVAL-PERSON")
	recorded := now.Add(time.Second)
	decision, err := f.record(t, request, run, routineguard.NeedsPerson, recorded)
	if err != nil || decision.Result != routineguard.NeedsPerson {
		t.Fatalf("verified person verdict: %+v %v", decision, err)
	}
	state, _, approval, raw := f.action(t)
	if state != "needs_person" || approval == nil {
		t.Fatalf("person hold missing: %s %v", state, approval)
	}
	decidedAt := now.Add(time.Hour)
	// Seed the delayed person decision under the real authorization fence.
	// The approval handler uses the DB clock; no wall-clock wait is needed.
	if err := f.write(t, func(tx pgx.Tx) error {
		hold, err := approvals.Review(t.Context(), tx, f.person, *approval)
		if err != nil {
			return err
		}
		if err := approvals.CanDecide(t.Context(), tx, f.person, hold); err != nil {
			return err
		}
		if !hold.ExpiresAt.After(decidedAt) || hold.ExpiresAt.After(recorded.Add(24*time.Hour)) {
			t.Fatalf("person hold cannot accept delayed decision within 24-hour cap: %s", hold.ExpiresAt)
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision,decided_at) VALUES($1,$2,$3,'approved',$4)`, f.person.TenantID, *approval, f.person.ID, decidedAt); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO agent_permission_grants(tenant_id,approval_request_id,agent_principal_id,scope,resource_kind,resource_id,valid_until) SELECT tenant_id,id,agent_principal_id,scope,resource_kind,resource_id,expires_at FROM approval_requests WHERE id=$1`, *approval)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		RecordedAt time.Time `json:"recorded_at"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || !receipt.RecordedAt.Equal(recorded) {
		t.Fatalf("recorded completion clock missing: %s %v", raw, err)
	}
	check := func(at time.Time) (bool, error) {
		var live bool
		err := f.write(t, func(tx pgx.Tx) error {
			var err error
			live, err = routineguard.CheckPersonHoldTx(t.Context(), tx, f.agent, f.binding, f.context, at)
			return err
		})
		return live, err
	}
	if live, err := check(decidedAt); err != nil || !live {
		t.Fatalf("approval after evaluator deadline refused: %v %v", live, err)
	}
	if live, err := check(recorded.Add(24 * time.Hour)); err != nil || live {
		t.Fatalf("expired person hold accepted: %v %v", live, err)
	}
	f.binding.HeadSHA = strings.Repeat("e", 40)
	if live, err := check(now.Add(time.Hour)); live || err == nil || !strings.Contains(err.Error(), "binding or policy changed") {
		t.Fatalf("changed head reused hold: %v %v", live, err)
	}
	f.binding = request.Binding
	f.in(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_guard_evaluations SET decision=jsonb_set(decision,'{recorded_at}',to_jsonb($2::timestamptz)) WHERE id=(SELECT evaluation_id FROM routine_actions WHERE id=$1)`, f.binding.ActionID, request.Deadline)
		return err
	})
	if live, err := check(now.Add(time.Hour)); live || err == nil || err.Error() != "evaluation incomplete or timed out" {
		t.Fatalf("late evaluator receipt accepted: %v %v", live, err)
	}
}

// Risk R7/R15: repeated attempts replace a verified restriction with allow.
// Same-run replay is idempotent; elapsed deadlines cannot clear the receipt.
func TestEvaluationVerifiedVerdictCannotBeShopped(t *testing.T) {
	for _, verdict := range []routineguard.Outcome{routineguard.Block, routineguard.NeedsPerson} {
		t.Run(string(verdict), func(t *testing.T) {
			f := newEvaluationFixture(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			f.reviewer(t, now)
			request, err := f.prepare(t, now)
			if err != nil {
				t.Fatal(err)
			}
			run := f.evaluator(t, request, "EVAL-FIRST")
			decision, err := f.record(t, request, run, verdict, now.Add(time.Second))
			if err != nil || decision.Result != verdict {
				t.Fatalf("verified first verdict: %+v %v", decision, err)
			}
			state, evaluation, approval, raw := f.action(t)
			wantState := "needs_person"
			if verdict == routineguard.Block {
				wantState = "blocked"
			}
			if state != wantState {
				t.Fatalf("wrong first state: %s", state)
			}
			other := f.evaluator(t, request, "EVAL-SECOND")
			if _, err := f.record(t, request, other, routineguard.Allow, now.Add(2*time.Second)); err == nil || err.Error() != "evaluation_result_already_recorded" {
				t.Errorf("replacement evaluator run not refused: %v", err)
			}
			if replay, err := f.record(t, request, run, routineguard.Allow, now.Add(3*time.Second)); err != nil || replay.Result != verdict {
				t.Errorf("same-run replay replaced verdict: %+v %v", replay, err)
			}
			for _, at := range []time.Time{request.Deadline.Add(time.Second), now.Add(25 * time.Hour)} {
				if replay, err := f.prepare(t, at); err != nil || replay.Digest != request.Digest {
					t.Errorf("expired request replaced verified verdict: %s %v", replay.Digest, err)
				}
			}
			gotState, gotEvaluation, gotApproval, gotRaw := f.action(t)
			if gotState != state || gotEvaluation != evaluation || (approval == nil) != (gotApproval == nil) || approval != nil && *approval != *gotApproval || !bytes.Equal(raw, gotRaw) {
				t.Error("verified receipt or person hold changed on replay")
			}
			f.binding.HeadSHA = strings.Repeat("e", 40)
			changed, err := f.prepare(t, request.Deadline.Add(2*time.Second))
			if err != nil || changed.Digest == request.Digest {
				t.Fatalf("changed head did not reopen evaluation: %v", err)
			}
			if state, _, approval, _ := f.action(t); state != "pending" || approval != nil {
				t.Fatalf("changed binding inherited restriction/hold: %s %v", state, approval)
			}
		})
	}
}

// Risk R15: using an unrelated model-family attempt as the action author makes
// a same-family evaluator appear independent. The persisted attempt owns it.
func TestEvaluationAuthorMatchesPersistedActionAttempt(t *testing.T) {
	f := newEvaluationFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var codex, claude string
	f.in(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT model_profile_id::text FROM agent_runs WHERE id=$1),(SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh')`, f.binding.AuthorRunID).Scan(&codex, &claude)
	})
	actual, actualAttempt := f.attempt(t, "AUTHOR-BUILDER", "build", codex, "", strings.Repeat("c", 64), map[string]any{})
	unrelated, _ := f.attempt(t, "OTHER-LEAD", "lead", claude, "", strings.Repeat("d", 64), map[string]any{})
	evaluator, evaluationAttempt := f.attempt(t, "OTHER-EVALUATOR", "evaluation", claude, "", strings.Repeat("e", 64), map[string]any{})
	main := f.binding.AuthorRunID
	for _, claimed := range []string{actual, unrelated, evaluator} {
		f.binding.AuthorRunID = claimed
		if _, err := f.prepare(t, now); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("unowned attempt accepted for main-authored action: %s %v", claimed, err)
		}
	}
	f.in(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_actions SET attempt_id=$2 WHERE id=$1`, f.binding.ActionID, actualAttempt)
		return err
	})
	for _, claimed := range []string{main, unrelated, evaluator} {
		f.binding.AuthorRunID = claimed
		if _, err := f.prepare(t, now); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("unowned run accepted for attempt-authored action: %s %v", claimed, err)
		}
	}
	f.binding.AuthorRunID = actual
	request, err := f.prepare(t, now)
	if err != nil || request.Binding.AuthorRunID != actual {
		t.Fatalf("recorded author refused: %+v %v", request, err)
	}
	if family, err := request.Author.Family(); err != nil || family != "openai" {
		t.Fatalf("wrong action author evidence: %s %v", family, err)
	}
	f.in(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_actions SET attempt_id=$2 WHERE id=$1`, f.binding.ActionID, evaluationAttempt)
		return err
	})
	f.binding.AuthorRunID = evaluator
	if _, err := f.prepare(t, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("evaluation attempt accepted as action author: %v", err)
	}
}

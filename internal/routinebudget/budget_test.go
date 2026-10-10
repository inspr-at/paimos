// SPDX-License-Identifier: AGPL-3.0-only
package routinebudget

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	t                                                              *testing.T
	d                                                              *dbtest.DB
	owner, agent                                                   tenant.Principal
	project, parent, work, account, computer, profile, run, action string
	routine                                                        recurrences.Recurrence
	broker                                                         Broker
	gate                                                           agentaccounts.RoutineGateContract
	mux                                                            *http.ServeMux
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) tx(fn func(pgx.Tx) error) error {
	return db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.owner.TenantID, fn)
}
func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.d.Admin.Exec(f.t.Context(), q, args...)
	must(f.t, err)
}
func (f *fixture) call(method, path string, in, out any) {
	f.t.Helper()
	raw, err := json.Marshal(in)
	must(f.t, err)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(f.t.Context(), f.owner))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	if w.Code < 200 || w.Code >= 300 {
		f.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	if out != nil {
		must(f.t, json.Unmarshal(w.Body.Bytes(), out))
	}
}
func setup(t *testing.T, mode string, paid bool) *fixture {
	t.Helper()
	f := &fixture{t: t, d: dbtest.Open(t), mux: http.NewServeMux(), owner: tenant.Principal{Kind: tenant.Person, BrowserSession: true}, agent: tenant.Principal{Kind: tenant.Agent, Scopes: []string{"run.claim", "run.report"}}}
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('routine-budget','Budget') RETURNING id::text`).Scan(&f.owner.TenantID))
	f.agent.TenantID = f.owner.TenantID
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, f.owner.TenantID).Scan(&f.owner.ID))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Worker') RETURNING id::text`, f.owner.TenantID).Scan(&f.agent.ID))
	f.agent.KeyCreatorID = f.owner.ID
	dbtest.BindRole(t, f.d, f.owner.TenantID, f.owner.ID, "owner")
	dbtest.BindRole(t, f.d, f.owner.TenantID, f.agent.ID, "admin")
	node := func(kind string, parent *string) string {
		var id string
		must(t, f.tx(func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Budget fixture',$2,1024 FROM node_kinds k WHERE tenant_id=$1 AND slug=$3 RETURNING id::text`, f.owner.TenantID, parent, kind).Scan(&id)
		}))
		return id
	}
	f.project = node("project", nil)
	f.parent = node("work", &f.project)
	f.exec(`UPDATE nodes SET fields=fields||'{"project_key":"BUD"}'::jsonb WHERE id=$1`, f.project)
	var kind string
	must(t, f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.owner.TenantID)
		return err
	}))
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE tenant_id=$1 AND slug='backend' AND project_id IS NULL`, f.owner.TenantID).Scan(&kind))
	f.exec(`INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by) VALUES($1,$2,$3,'{"recovery":{"max_attempts":5,"agent_hours":10}}',$3)`, f.owner.TenantID, f.project, f.owner.ID)
	f.exec(`INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by) VALUES($1,NULL,NULL,'{"recovery":{"max_attempts":5,"agent_hours":10}}',$2)`, f.owner.TenantID, f.owner.ID)
	q := modelregistry.Qualification{ID: "10000000-0000-4000-8000-000000000030", ProjectID: f.project, Runtime: modelregistry.ExecutionRuntime{ServerDigest: strings.Repeat("a", 64), DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), Capabilities: []string{"routine_native_coding_v1"}, HostMappingDigest: strings.Repeat("d", 64), BudgetModes: []string{"off", "tokens", "money", "both"}}, CoordinatorAcceptance: strings.Repeat("e", 64), OPSAttestation: strings.Repeat("f", 64)}
	runtime := func(ctx context.Context, tx pgx.Tx, _ string) (modelregistry.ExecutionRuntime, error) {
		r := q.Runtime
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&r.ObservedAt)
		return r, err
	}
	f.gate = agentaccounts.RoutineGateContract{QualificationID: q.ID, CapabilityDigest: q.Runtime.CapabilityDigest, AccountMaxAgeMS: 600000, HostMaxAgeMS: 60000}
	f.broker = Broker{Runtime: runtime, Gates: func(_ context.Context, _ pgx.Tx, _ string) (agentaccounts.RoutineGateContract, error) {
		return f.gate, nil
	}}
	recurrences.New(f.d.App).WithExecutionRuntime(runtime).Mount(f.mux)
	n := int64(100)
	budget := recurrences.DefinitionBudget{Mode: mode}
	if mode == "tokens" || mode == "both" {
		budget.TokenCeiling = &n
	}
	if mode == "money" || mode == "both" {
		budget.MoneyCeilingMicroUSD = &n
	}
	in := recurrences.Input{ProjectID: f.project, ParentID: f.parent, QueueEach: true, OverlapPolicy: "create", CatchUpPolicy: "one", Template: recurrences.Template{Title: "Budget work", Description: "Assigned workspace", Criteria: []string{"Validate"}, EstimateHours: 1, Priority: "high", Type: "ticket"}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "UTC"}, Definition: &recurrences.Definition{Scope: recurrences.DefinitionScope{Kind: "personal"}, OwnerPrincipalID: f.owner.ID, Assignment: &recurrences.Assignment{Goal: "Bounded fixture", Sources: []recurrences.SourceReference{}, Role: "build", WorkKindID: kind, AllowedActions: []string{"work.create"}, RuntimeRequirements: recurrences.RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: budget}}}
	f.call("POST", "/api/recurrences", in, &f.routine)
	must(t, f.tx(func(tx pgx.Tx) error {
		var err error
		q.OwnerPersonID, q.PolicyDigest, err = modelregistry.QualificationPolicyTx(t.Context(), tx, f.owner.TenantID, f.project)
		if err != nil {
			return err
		}
		return modelregistry.RecordQualificationTx(t.Context(), tx, f.owner, q)
	}))
	rev, enable := int64(0), true
	must(t, f.tx(func(tx pgx.Tx) error {
		_, err := modelregistry.WriteExecutionSettingsTx(t.Context(), tx, f.owner, f.project, modelregistry.ExecutionSettingsInput{ExpectedRevision: &rev, AutomaticLaunchEnabled: &enable, QualificationID: &q.ID}, runtime)
		return err
	}))
	path := "/api/recurrences/" + f.routine.ID
	f.call("POST", path+"/resume", map[string]any{"expected_revision": f.routine.Revision}, nil)
	f.call("GET", path, nil, &f.routine)
	f.call("PUT", path+"/execution-consent", map[string]any{"expected_revision": f.routine.Revision, "execute_consent": true}, nil)
	f.call("GET", path, nil, &f.routine)
	var occurrence recurrences.Occurrence
	f.call("POST", path+"/run-now", map[string]string{"idempotency_key": "budget"}, &occurrence)
	if occurrence.Run == nil || occurrence.NodeID == nil {
		t.Fatal("missing actual occurrence lineage")
	}
	f.run = occurrence.Run.ID
	f.work = *occurrence.NodeID
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM routine_actions WHERE run_id=$1`, f.run).Scan(&f.action))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'budget-profile','1','codex','openai','fixture-model','high','standard') RETURNING id::text`, f.owner.TenantID).Scan(&f.profile))
	billing := "subscription"
	if paid {
		billing = "api"
	}
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id,capacity_owner,linked_at,last_probe_at,last_probe_ok,last_daemon_generation,billing_mode,max_parallel_runs) VALUES($1,'budget','codex','budget-daemon','Budget',$2,$3,$3,clock_timestamp()-interval '1 hour',clock_timestamp(),true,'budget-generation',$4,20) RETURNING id::text`, f.owner.TenantID, f.agent.ID, f.owner.ID, billing).Scan(&f.account))
	if paid {
		f.gate.PaidAccountIDs = []string{f.account}
	}
	var request, key string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'Fixture',gen_random_uuid()::text,'fixture-not-a-credential',$3) RETURNING id::text`, f.owner.TenantID, f.agent.ID, f.owner.ID).Scan(&key))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,gen_random_uuid(),'887887887',repeat('a',64),repeat('b',64),repeat('c',64),'{}','fixture','redeemed',$2) RETURNING id::text`, f.owner.TenantID, f.owner.ID).Scan(&request))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,capacity_signals,capacity_reported_at) VALUES($1,gen_random_uuid(),$2,$3,$4,'budget-daemon',repeat('c',64),'{"load":5,"cores":18,"memory_pressure":"normal","power":"plugged_in","thermal":"normal"}',clock_timestamp()) RETURNING id::text`, f.owner.TenantID, request, f.agent.ID, key).Scan(&f.computer))
	f.exec(`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at) VALUES($1,$2,$3,$4,$5,clock_timestamp()+interval '1 hour',clock_timestamp())`, f.owner.TenantID, f.account, f.computer, request, f.profile)
	f.exec(`INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source) VALUES($1,$2,clock_timestamp()-interval '1 minute',clock_timestamp()+interval '7 days','percent',100,20,'unrestricted',clock_timestamp(),true,'weekly','codex','harness')`, f.owner.TenantID, f.account)
	f.exec(`INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','codex',10080,20,clock_timestamp()+interval '7 days',clock_timestamp(),'harness')`, f.owner.TenantID, f.account)
	// Daily admission uses the same measured reset identity and observation time.
	f.exec(`UPDATE account_allowance_windows w SET ends_at=r.resets_at,capacity_read_at=r.read_at FROM account_capacity_readings r WHERE w.account_id=$1 AND r.account_id=w.account_id`, f.account)
	schedule := capacity.DefaultSchedule()
	schedule.Override = "sprint"
	schedule.Reserve = "off"
	raw, err := json.Marshal(schedule)
	must(t, err)
	f.exec(`INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, f.owner.TenantID, f.owner.ID, f.account, raw)
	return f
}
func (f *fixture) attempt(role string, maximum Amount) Reservation {
	f.t.Helper()
	var run, order, attempt string
	// Fixture-only initial holder; later actors are explicit siblings under the
	// generated work. Production child creation is tested through CreateDeferred.
	must(f.t, f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Actor order',$2,1024 FROM node_kinds k WHERE tenant_id=$1 AND slug='work_order' RETURNING id::text`, f.owner.TenantID, f.work).Scan(&order)
	}))
	f.exec(`INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,assignee_principal_id) VALUES($1,$2,$3,$4)`, f.owner.TenantID, order, f.owner.ID, f.agent.ID)
	must(f.t, f.d.Admin.QueryRow(f.t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,account_id,status) VALUES($1,$2,$3,$4,$5,'queued') RETURNING id::text`, f.owner.TenantID, order, f.agent.ID, f.profile, f.account).Scan(&run))
	must(f.t, f.d.Admin.QueryRow(f.t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,principal_id,assignment_digest) SELECT tenant_id,id,gen_random_uuid()::text,$2,$3,$4,assignment_digest FROM routine_runs WHERE id=$1 RETURNING id::text`, f.run, role, run, f.agent.ID).Scan(&attempt))
	return Reservation{RunID: f.run, AttemptID: attempt, ActionID: f.action, GrantKey: "envelope", AgentRunID: run, AccountID: f.account, Maximum: maximum}
}
func reserve(t *testing.T, f *fixture, pool *pgxpool.Pool, ctx context.Context, in Reservation) (Grant, error) {
	t.Helper()
	var g Grant
	err := db.InTenant(tenant.WithPrincipal(ctx, f.owner), pool, f.owner.TenantID, func(tx pgx.Tx) error { var err error; g, err = f.broker.ReserveTx(ctx, tx, f.owner, in); return err })
	return g, err
}
func wantError(t *testing.T, err error, want string) {
	t.Helper()
	var e *workorders.Error
	if !errors.As(err, &e) || e.Status != 409 || e.Message != want {
		t.Fatalf("got %v, want 409 %s", err, want)
	}
}
func (f *fixture) balance() Balance {
	f.t.Helper()
	var b Balance
	must(f.t, f.tx(func(tx pgx.Tx) error { var err error; b, err = loadBalance(f.t.Context(), tx, f.run); return err }))
	return b
}
func settlement(event string, a Amount) Settlement {
	return Settlement{EventKey: event, Usage: a, UsageComplete: true, TokensKnown: true, PaidCostKnown: true, ExitConfirmed: true, EvidenceDigest: strings.Repeat("e", 64)}
}

// Risks: money/limit errors, duplicate charges and concurrent actors minting a
// larger ceiling. Existing account/pairing guards cover ordinary admission;
// this new shared trust boundary needs real overlapping budget transactions.
func TestRoutineBudgetConcurrentCeilingsAndSettlement(t *testing.T) {
	for _, tc := range []struct {
		mode, reason string
		a            Amount
	}{
		{"tokens", "token_budget_exhausted", Amount{Tokens: 60, RecoveryMS: 1000}},
		{"money", "money_budget_exhausted", Amount{PaidMicroUSD: 60, RecoveryMS: 1000}},
		{"both", "token_budget_exhausted", Amount{Tokens: 60, PaidMicroUSD: 10, RecoveryMS: 1000}},
		{"both", "money_budget_exhausted", Amount{Tokens: 10, PaidMicroUSD: 60, RecoveryMS: 1000}},
	} {
		t.Run(tc.mode+tc.reason, func(t *testing.T) {
			f := setup(t, tc.mode, tc.a.PaidMicroUSD > 0)
			one, two := f.attempt("build", tc.a), f.attempt("review", tc.a)
			pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.HasPrefix(q, "UPDATE routine_budget_balances SET") })
			first := make(chan error, 1)
			var g Grant
			go func() { var err error; g, err = reserve(t, f, pool, ctx, one); first <- err }()
			pid := barrier.Wait(t, ctx)
			second := make(chan error, 1)
			done := make(chan struct{})
			go func() { _, err := reserve(t, f, f.d.App, ctx, two); second <- err; close(done) }()
			if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock == "" {
				t.Fatal("budget contenders never overlapped")
			}
			barrier.Release()
			must(t, dbtest.Await(t, ctx, first))
			wantError(t, dbtest.Await(t, ctx, second), tc.reason)
			b := f.balance()
			if b.Held != tc.a || b.Settled != (Amount{}) {
				t.Fatal(b)
			}
			replay, err := reserve(t, f, f.d.App, t.Context(), one)
			must(t, err)
			if replay.ID != g.ID {
				t.Fatal("duplicate envelope")
			}
			call := one
			call.GrantKey = "provider-call"
			call.Maximum = Amount{Tokens: tc.a.Tokens / 2, PaidMicroUSD: tc.a.PaidMicroUSD / 2, RecoveryMS: 100}
			var child Grant
			must(t, f.tx(func(tx pgx.Tx) error {
				var err error
				child, err = f.broker.SubgrantTx(t.Context(), tx, f.owner, g.ID, call)
				return err
			}))
			partial := settlement("partial-call", Amount{})
			partial.TokensKnown = false
			must(t, f.tx(func(tx pgx.Tx) error {
				_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, child.ID, partial)
				return err
			}))
			if f.balance().Held != tc.a {
				t.Fatal("unknown call released parent envelope")
			}
			wantError(t, f.tx(func(tx pgx.Tx) error {
				_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, g.ID, settlement("early-exit", Amount{}))
				return err
			}), "child_settlement_unknown")
			excessive := call
			excessive.GrantKey = "excess"
			excessive.Maximum = tc.a
			wantError(t, f.tx(func(tx pgx.Tx) error {
				_, err := f.broker.SubgrantTx(t.Context(), tx, f.owner, g.ID, excessive)
				return err
			}), "subgrant_budget_exhausted")
			s := settlement("call-paid", call.Maximum)
			must(t, f.tx(func(tx pgx.Tx) error {
				_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, child.ID, s)
				return err
			}))
			rootUsage := call.Maximum
			rootUsage.RecoveryMS = 500
			s = settlement("actor-exit", rootUsage)
			must(t, f.tx(func(tx pgx.Tx) error {
				_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, g.ID, s)
				return err
			}))
			must(t, f.tx(func(tx pgx.Tx) error {
				_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, g.ID, s)
				return err
			}))
			changed := s
			changed.Usage.Tokens++
			wantError(t, f.tx(func(tx pgx.Tx) error {
				_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, g.ID, changed)
				return err
			}), "settlement_replay_conflict")
			b = f.balance()
			if b.Held != (Amount{}) || b.Settled != rootUsage {
				t.Fatal("double charge or leaked hold", b)
			}
			_, err = reserve(t, f, f.d.App, t.Context(), two)
			must(t, err)
		})
	}
}

// Risks: false known-zero usage, premature process-slot release and bound child
// work escaping the shared ceiling through ordinary/manual order or claim paths.
func TestRoutineBudgetUnknownRetainsSlotAndChildBinding(t *testing.T) {
	f := setup(t, "off", false)
	f.exec(`INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working','{"total":1,"limits":{}}')`, f.owner.TenantID, f.owner.ID)
	in := f.attempt("lead", Amount{Tokens: 1000, RecoveryMS: 1000})
	g, err := reserve(t, f, f.d.App, t.Context(), in)
	must(t, err)
	must(t, db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		if err := db.LockCurrentTree(t.Context(), tx); err != nil {
			return err
		}
		return agentaccounts.ValidateReservedCapacity(t.Context(), tx, in.AgentRunID, in.AccountID, f.broker.StartCheck(f.agent))
	}))
	// Reparenting cannot turn an already-bound routine order into an ordinary
	// claim. The first validation acquires all fences before the fixture move.
	wantError(t, f.tx(func(tx pgx.Tx) error {
		if err := agentaccounts.ValidateReservedCapacity(t.Context(), tx, in.AgentRunID, in.AccountID, f.broker.StartCheck(f.agent)); err != nil {
			return err
		}
		var leaf string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Ordinary leaf',$2,2048 FROM node_kinds k WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, f.owner.TenantID, f.project).Scan(&leaf); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=(SELECT work_order_id FROM agent_runs WHERE id=$1)`, in.AgentRunID, leaf); err != nil {
			return err
		}
		return agentaccounts.ValidateReservedCapacity(t.Context(), tx, in.AgentRunID, in.AccountID, f.broker.StartCheck(f.agent))
	}), "routine_order_outside_lineage")
	unknown := settlement("lost-exit", Amount{})
	unknown.ExitConfirmed = false
	must(t, f.tx(func(tx pgx.Tx) error {
		out, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, g.ID, unknown)
		if err == nil && out.State != "unknown" {
			t.Fatal(out)
		}
		return err
	}))
	if f.balance().Held != in.Maximum {
		t.Fatal("unknown exit freed hold")
	}
	next := f.attempt("evaluate", Amount{RecoveryMS: 1000})
	wantError(t, f.tx(func(tx pgx.Tx) error { _, err := f.broker.ReserveTx(t.Context(), tx, f.owner, next); return err }), "dial_planned total reached")
	wantError(t, db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		if err := db.LockCurrentTree(t.Context(), tx); err != nil {
			return err
		}
		return agentaccounts.ValidateReservedCapacity(t.Context(), tx, in.AgentRunID, in.AccountID)
	}), "routine_start_check_unavailable")
	wantError(t, db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		if err := db.LockCurrentTree(t.Context(), tx); err != nil {
			return err
		}
		return agentaccounts.ValidateReservedCapacity(t.Context(), tx, in.AgentRunID, in.AccountID, f.broker.StartCheck(f.agent))
	}), "routine_grant_unavailable")
	child := workorders.CreateInput{Parent: &f.work, Title: "Bound child", Criteria: []string{"Verify"}}
	wantError(t, f.tx(func(tx pgx.Tx) error {
		_, _, err := workorders.CreateDeferred(t.Context(), tx, f.owner, child)
		return err
	}), "routine_child_binding_required")
	child.RoutineRunID = f.run
	must(t, f.tx(func(tx pgx.Tx) error {
		_, _, err := workorders.CreateDeferred(t.Context(), tx, f.owner, child)
		return err
	}))
	unknown.ExitConfirmed = true
	unknown.UsageComplete = false
	must(t, f.tx(func(tx pgx.Tx) error {
		_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, g.ID, unknown)
		return err
	}))
	if f.balance().Held != in.Maximum {
		t.Fatal("partial usage freed hold")
	}
	unused := settlement("proven-unused", Amount{})
	unused.ExitConfirmed = false
	unused.ProvenUnused = true
	must(t, f.tx(func(tx pgx.Tx) error {
		_, err := f.broker.SettleTx(t.Context(), tx, f.owner, f.run, g.ID, unused)
		return err
	}))
	if f.balance().Held != (Amount{}) {
		t.Fatal("proven unused hold not released")
	}
	_, err = reserve(t, f, f.d.App, t.Context(), next)
	must(t, err)
}

// Risks: stale/unknown account or host facts, off bypassing admission, unapproved
// paid account launches, owner revocation races and tenant/personal leakage.
func TestRoutineBudgetAdmissionAndOwnership(t *testing.T) {
	f := setup(t, "both", true)
	in := f.attempt("build", Amount{Tokens: 10, PaidMicroUSD: 10, RecoveryMS: 1000})
	original := f.gate
	f.gate.HostMaxAgeMS = 0
	_, err := reserve(t, f, f.d.App, t.Context(), in)
	wantError(t, err, "admission_contract_unqualified")
	f.gate = original
	f.gate.PaidAccountIDs = nil
	_, err = reserve(t, f, f.d.App, t.Context(), in)
	wantError(t, err, "execution_account_unqualified")
	f.gate = original
	f.exec(`UPDATE agent_pairing_computers SET capacity_reported_at=clock_timestamp()-interval '61 seconds' WHERE id=$1`, f.computer)
	_, err = reserve(t, f, f.d.App, t.Context(), in)
	wantError(t, err, "host_inputs_unreadable")
	f.exec(`UPDATE agent_pairing_computers SET capacity_reported_at=clock_timestamp() WHERE id=$1`, f.computer)
	f.exec(`UPDATE account_allowance_windows SET capacity_read_at=clock_timestamp()-interval '11 minutes' WHERE account_id=$1`, f.account)
	_, err = reserve(t, f, f.d.App, t.Context(), in)
	wantError(t, err, "account_inputs_unreadable")
	f.exec(`UPDATE account_allowance_windows SET capacity_read_at=clock_timestamp() WHERE account_id=$1`, f.account)
	g, err := reserve(t, f, f.d.App, t.Context(), in)
	must(t, err)
	must(t, db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		if err := db.LockCurrentTree(t.Context(), tx); err != nil {
			return err
		}
		return agentaccounts.ValidateReservedCapacity(t.Context(), tx, in.AgentRunID, in.AccountID, f.broker.StartCheck(f.agent))
	}))
	var administrator string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Remaining administrator') RETURNING id::text`, f.owner.TenantID).Scan(&administrator))
	dbtest.BindRole(t, f.d, f.owner.TenantID, administrator, "owner")
	// Even another workspace owner cannot read this personal routine's ledger.
	privateCall := in
	privateCall.GrantKey = "private-unused-call"
	privateCall.Maximum = Amount{RecoveryMS: 1}
	must(t, f.tx(func(tx pgx.Tx) error {
		child, err := f.broker.SubgrantTx(t.Context(), tx, f.owner, g.ID, privateCall)
		if err != nil {
			return err
		}
		unused := settlement("private-unused", Amount{})
		unused.ProvenUnused = true
		_, err = f.broker.SettleTx(t.Context(), tx, f.owner, f.run, child.ID, unused)
		return err
	}))
	other := tenant.Principal{ID: administrator, TenantID: f.owner.TenantID, Kind: tenant.Person}
	// Hidden personal receipts must still prevent an ordinary visible-project
	// writer from creating a child that omits the shared run binding.
	wantError(t, db.InTenant(tenant.WithPrincipal(t.Context(), other), f.d.App, other.TenantID, func(tx pgx.Tx) error {
		if err := db.LockCurrentTree(t.Context(), tx); err != nil {
			return err
		}
		_, _, err := workorders.CreateDeferred(t.Context(), tx, other, workorders.CreateInput{Parent: &f.work, Title: "Unbound personal child", Criteria: []string{"Verify"}})
		return err
	}), "routine_child_binding_required")
	for _, table := range []string{"routine_budget_balances", "routine_budget_grants", "routine_budget_settlements", "routine_budget_orders"} {
		var retained int
		must(t, f.d.Admin.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&retained))
		if retained == 0 {
			t.Fatal("privacy fixture lost asserted data", table)
		}
		must(t, db.InTenant(tenant.WithPrincipal(t.Context(), other), f.d.App, other.TenantID, func(tx pgx.Tx) error {
			var n int
			err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
			if err == nil && n != 0 {
				t.Fatal("personal ledger leaked", table)
			}
			return err
		}))
	}
	// Revoke on a real tenant fence before a competing provider-call transaction.
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.HasPrefix(q, "UPDATE principals SET status=") })
	revoked := make(chan error, 1)
	go func() {
		revoked <- db.InTenant(dbtest.Seed(ctx), pool, f.owner.TenantID, func(tx pgx.Tx) error {
			if err := db.LockCurrentTree(ctx, tx); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, f.owner.ID)
			return err
		})
	}()
	pid := barrier.Wait(t, ctx)
	blocked := make(chan error, 1)
	done := make(chan struct{})
	call := in
	call.GrantKey = "after-revocation"
	go func() {
		blocked <- db.InTenant(dbtest.Seed(ctx), f.d.App, f.owner.TenantID, func(tx pgx.Tx) error { _, err := f.broker.SubgrantTx(ctx, tx, f.owner, g.ID, call); return err })
		close(done)
	}()
	if dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done) == "" {
		t.Fatal("revocation did not overlap write")
	}
	barrier.Release()
	must(t, dbtest.Await(t, ctx, revoked))
	err = dbtest.Await(t, ctx, blocked)
	if !errors.Is(err, authz.ErrForbidden) {
		var e *workorders.Error
		if !errors.As(err, &e) || e.Message != "owner_unavailable" {
			t.Fatalf("wrong revocation refusal: %v", err)
		}
	}
	if f.balance().Held != in.Maximum {
		t.Fatal("revocation released unknown hold")
	}
	var foreign string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('foreign-budget','Foreign') RETURNING id::text`).Scan(&foreign))
	must(t, db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreign, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_budget_grants`).Scan(&n)
		if err == nil && n != 0 {
			t.Fatal("cross-tenant budget leak")
		}
		return err
	}))
}

// Risk: integer wraparound turning a finite money/token/recovery ceiling into
// extra room. Arithmetic is checked before any ledger mutation.
// Risk: moving the routine root into an ordinary project must not erase its
// frozen budget lineage or permit either unbound or wrongly projected orders.
func TestRoutineBudgetMovedRootKeepsChildBinding(t *testing.T) {
	f := setup(t, "off", false)
	in := f.attempt("lead", Amount{RecoveryMS: 1000})
	_, err := reserve(t, f, f.d.App, t.Context(), in)
	must(t, err)
	var project, parent string
	must(t, f.tx(func(tx pgx.Tx) error {
		if err := db.LockCurrentTree(t.Context(), tx); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Ordinary project',2048 FROM node_kinds k WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.owner.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,position) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Moved output',$2,1024 FROM node_kinds k WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, f.owner.TenantID, project).Scan(&parent); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$2 WHERE id=$1`, f.work, parent)
		return err
	}))
	var moved, frozen string
	var targetBalances int
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT n.project_id::text,r.output_project_id::text,(SELECT count(*) FROM routine_budget_balances b JOIN routine_runs rr ON rr.id=b.run_id WHERE rr.output_project_id=$2) FROM nodes n JOIN routine_runs r ON r.work_node_id=n.id WHERE n.id=$1`, f.work, project).Scan(&moved, &frozen, &targetBalances))
	if moved != project || frozen != f.project || targetBalances != 0 {
		t.Fatal("fixture did not move root into a project without budgets", moved, frozen, targetBalances)
	}
	for _, binding := range []string{"", f.run} {
		t.Run("binding="+binding, func(t *testing.T) {
			wantError(t, f.tx(func(tx pgx.Tx) error {
				if err := db.LockCurrentTree(t.Context(), tx); err != nil {
					return err
				}
				_, _, err := workorders.CreateDeferred(t.Context(), tx, f.owner, workorders.CreateInput{Parent: &f.work, Title: "Moved routine child", Criteria: []string{"Verify"}, RoutineRunID: binding})
				return err
			}), "routine_order_outside_lineage")
		})
	}
}

// Risk: caller RLS hides personal ledger rows from the assigned daemon. Its
// authenticated receipts must reconcile once without opening ordinary reads
// or accepting another agent, a missing scope, or a different tenant.
func TestRoutineBudgetDaemonSettlementKeepsLedgerPrivate(t *testing.T) {
	f := setup(t, "both", true)
	in := f.attempt("build", Amount{Tokens: 10, PaidMicroUSD: 10, RecoveryMS: 1000})
	root, err := reserve(t, f, f.d.App, t.Context(), in)
	must(t, err)
	call := in
	call.GrantKey = "provider-call"
	var child Grant
	must(t, f.tx(func(tx pgx.Tx) error {
		var err error
		child, err = f.broker.SubgrantTx(t.Context(), tx, f.owner, root.ID, call)
		return err
	}))
	checkPrivate := func(tx pgx.Tx) error {
		for _, table := range []string{"routine_runs", "routine_budget_balances", "routine_budget_grants", "routine_budget_settlements", "routine_budget_orders"} {
			var n int
			if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Fatal("daemon ordinary reads exposed personal ledger", table, n)
			}
		}
		return nil
	}
	report := func(p tenant.Principal, id string, receipt Settlement) error {
		return db.InTenant(tenant.WithPrincipal(t.Context(), p), f.d.App, p.TenantID, func(tx pgx.Tx) error {
			if err := checkPrivate(tx); err != nil {
				return err
			}
			_, err := f.broker.SettleTx(t.Context(), tx, p, f.run, id, receipt)
			if err != nil {
				return err
			}
			return checkPrivate(tx)
		})
	}
	unknown := settlement("unknown-exit", Amount{})
	unknown.ExitConfirmed = false
	must(t, report(f.agent, root.ID, unknown))
	if f.balance().Held != in.Maximum {
		t.Fatal("unknown daemon exit freed hold")
	}
	missingScope := f.agent
	missingScope.Scopes = []string{"run.claim"}
	if err := report(missingScope, child.ID, settlement("missing-scope", Amount{})); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("missing run.report scope: %v", err)
	}
	other := f.agent
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Other reporter') RETURNING id::text`, f.owner.TenantID).Scan(&other.ID))
	dbtest.BindRole(t, f.d, other.TenantID, other.ID, "admin")
	if err := report(other, child.ID, settlement("wrong-agent", Amount{})); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("unassigned reporter: %v", err)
	}
	foreign := f.agent
	foreign.TenantID = "20000000-0000-4000-8000-000000000001"
	err = db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		_, err := f.broker.SettleTx(t.Context(), tx, foreign, f.run, child.ID, settlement("wrong-tenant", Amount{}))
		return err
	})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("cross-tenant reporter: %v", err)
	}
	// Reconciliation remains allowed after launch consent is revoked.
	f.exec(`UPDATE routine_runs SET execute_consent=false WHERE id=$1`, f.run)
	usage := Amount{Tokens: 7, PaidMicroUSD: 4, RecoveryMS: 100}
	must(t, report(f.agent, child.ID, settlement("call-used", usage)))
	final := settlement("exit-confirmed", usage)
	must(t, report(f.agent, root.ID, final))
	must(t, report(f.agent, root.ID, final))
	if got := f.balance(); got.Held != (Amount{}) || got.Settled != usage {
		t.Fatal("daemon receipt failed to reconcile exactly once", got)
	}
	var count int
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM routine_budget_settlements WHERE run_id=$1`, f.run).Scan(&count))
	if count != 2 {
		t.Fatal("receipt replay duplicated settlement", count)
	}
}

// Risk: retired history and removed windows retained by live limit rules are
// still read/locked by shared admission; overflow must fail before that work.
func TestRoutineBudgetAdmissionBoundsHistoricalWindows(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "retired", true: "removed-rule-source"}[removed], func(t *testing.T) {
			f := setup(t, "off", false)
			in := f.attempt("build", Amount{RecoveryMS: 1000})
			check := func(rejectReads bool) error {
				return f.tx(func(tx pgx.Tx) error {
					if _, err := begin(t.Context(), tx, f.owner, f.run); err != nil {
						return err
					}
					var now time.Time
					if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
						return err
					}
					_, _, _, err := agentaccounts.RequireRoutineCapacityTx(t.Context(), admissionWindowGuardTx{Tx: tx, rejectReads: rejectReads}, f.owner.ID, in.AgentRunID, f.account, f.gate, now)
					return err
				})
			}
			f.exec(`INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source,capacity_retired) SELECT $1,$2,clock_timestamp()-interval '8 days'-n*interval '1 minute',clock_timestamp()-interval '1 day'-n*interval '1 minute','percent',100,20,'unrestricted',clock_timestamp(),true,'weekly','codex','harness',true FROM generate_series(1,4095) n`, f.owner.TenantID, f.account)
			must(t, check(false)) // Exactly 4096 consumed rows remain admissible.
			var last string
			must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,capacity_retired,removed_at) VALUES($1,$2,clock_timestamp()-interval '9 days',clock_timestamp()-interval '2 days','requests',1000000,'unrestricted',true,CASE WHEN $3 THEN clock_timestamp() END) RETURNING id::text`, f.owner.TenantID, f.account, removed).Scan(&last))
			if removed {
				f.exec(`INSERT INTO account_limit_rules(tenant_id,account_id,amount,unit,period,from_window_id,created_by) VALUES($1,$2,1000000,'requests','week',$3,$4)`, f.owner.TenantID, f.account, last, f.owner.ID)
			}
			wantError(t, check(true), "account_inputs_unreadable")
		})
	}
}

type admissionWindowGuardTx struct {
	pgx.Tx
	rejectReads bool
}

func (tx admissionWindowGuardTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx.rejectReads && strings.Contains(strings.ToLower(sql), "from account_allowance_windows") {
		return nil, errors.New("window overflow reached materialization or row locking")
	}
	return tx.Tx.Query(ctx, sql, args...)
}

func TestRoutineBudgetCheckedArithmeticAndModes(t *testing.T) {
	max := int64(math.MaxInt64)
	for _, a := range []Amount{{Tokens: max}, {PaidMicroUSD: max}, {RecoveryMS: max}} {
		_, err := a.add(Amount{Tokens: 1, PaidMicroUSD: 1, RecoveryMS: 1})
		wantError(t, err, "budget_arithmetic_overflow")
	}
	for _, mode := range []string{"off", "tokens", "money", "both"} {
		b := Balance{Mode: mode, RecoveryMS: 100}
		ceiling := int64(10)
		if mode == "tokens" || mode == "both" {
			b.TokenCeiling = &ceiling
		}
		if mode == "money" || mode == "both" {
			b.MoneyCeiling = &ceiling
		}
		_, err := b.reserve(Amount{Tokens: 11, PaidMicroUSD: 11, RecoveryMS: 1})
		switch mode {
		case "off":
			must(t, err)
		case "tokens", "both":
			wantError(t, err, "token_budget_exhausted")
		case "money":
			wantError(t, err, "money_budget_exhausted")
		}
		_, err = b.reserve(Amount{RecoveryMS: 101})
		wantError(t, err, "recovery_budget_exhausted")
	}
}

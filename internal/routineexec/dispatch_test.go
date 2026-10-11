// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/engineadmission"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/routinebudget"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	t                                                              *testing.T
	d                                                              *dbtest.DB
	owner, agent                                                   tenant.Principal
	project, parent, work, account, computer, profile, run, action string
	routine                                                        recurrences.Recurrence
	broker                                                         routinebudget.Broker
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
	f := &fixture{t: t, d: dbtest.Open(t), mux: http.NewServeMux(), owner: tenant.Principal{Kind: tenant.Person, BrowserSession: true}, agent: tenant.Principal{Kind: tenant.Agent, Scopes: []string{"run.claim", "run.telemetry"}}}
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
	f.broker = routinebudget.Broker{Runtime: runtime, Gates: func(_ context.Context, _ pgx.Tx, _ string) (agentaccounts.RoutineGateContract, error) {
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
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id,capacity_owner,linked_at,last_probe_at,last_probe_ok,last_daemon_generation,billing_mode,max_parallel_runs) VALUES($1,'budget','codex','budget-daemon','Budget',$2,$3,$3,clock_timestamp()-interval '1 hour',clock_timestamp(),true,'budget-generation',$4,1) RETURNING id::text`, f.owner.TenantID, f.agent.ID, f.owner.ID, billing).Scan(&f.account))
	if paid {
		f.gate.PaidAccountIDs = []string{f.account}
	}
	var request, key string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'Fixture',gen_random_uuid()::text,'fixture-not-a-credential',$3) RETURNING id::text`, f.owner.TenantID, f.agent.ID, f.owner.ID).Scan(&key))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,gen_random_uuid(),'887887887',repeat('a',64),repeat('b',64),repeat('c',64),'{"platform":"darwin","arch":"arm64"}','fixture','redeemed',$2) RETURNING id::text`, f.owner.TenantID, f.owner.ID).Scan(&request))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,capacity_signals,capacity_reported_at) VALUES($1,gen_random_uuid(),$2,$3,$4,'budget-daemon',repeat('c',64),'{"load":5,"cores":18,"memory_pressure":"normal","power":"plugged_in","thermal":"normal","unattended":{"login_session":"ready","idle_sleep":"disabled","filevault":"on"}}',clock_timestamp()) RETURNING id::text`, f.owner.TenantID, request, f.agent.ID, key).Scan(&f.computer))
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

type dispatchFixture struct {
	*fixture
	e                        *Executor
	options                  Options
	lead                     tenant.Principal
	session, lease, agentRun string
	mode                     *atomic.Int32
}

func setupDispatch(t *testing.T) *dispatchFixture {
	t.Helper()
	return setupDispatchOn(t, setup(t, "off", false))
}

func setupDispatchOn(t *testing.T, f *fixture) *dispatchFixture {
	t.Helper()
	var coordinator, session, key, agentRun string
	lease := "dispatch-test-owned-worker-lease-000000000"
	leaseDigest := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	refDigest := sha256.Sum256([]byte("aeon.harness.ref\x00dispatch-reference"))
	f.owner.BrowserSession = true
	f.exec(`UPDATE agent_pairing_requests SET computer_id=$2 WHERE id=(SELECT request_id FROM agent_pairing_computers WHERE id=$2) AND tenant_id=$1`, f.owner.TenantID, f.computer)
	must(t, f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Routine lead') RETURNING id::text`, f.owner.TenantID).Scan(&coordinator); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='admin'`, f.owner.TenantID, coordinator); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'Coordinator',gen_random_uuid()::text,'fixture-not-a-credential',$3,$4) RETURNING id::text`, f.owner.TenantID, coordinator, authz.CoordinatorKeyScopes, f.owner.ID).Scan(&key); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,phase,heartbeat_at,ref_digest,lease_digest) VALUES($1,$2,$3,$4,'codex','local','unmanaged','coordinator','working',clock_timestamp(),$5,$6) RETURNING id::text`, f.owner.TenantID, f.project, coordinator, f.owner.ID, refDigest[:], leaseDigest[:]).Scan(&session); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state,dispatch_key_id) VALUES($1,$2,$3,$4,1,'working',$5)`, f.owner.TenantID, f.project, f.owner.ID, session, key); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2)`, f.owner.TenantID, f.profile); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT agent_run_id::text FROM routine_runs WHERE id=$1`, f.run).Scan(&agentRun)
	}))
	var mode atomic.Int32
	admission := func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at)
		g := harness.LeadGate{State: "available", CheckedAt: at}
		checks := harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}
		if mode.Load() == 1 {
			checks.Host.CheckedAt = at.Add(-time.Minute)
		}
		return checks, err
	}
	options := Options{Runtime: f.broker.Runtime, Gates: f.broker.Gates, LeadAdmission: admission,
		Facts: func(ctx context.Context, tx pgx.Tx, tid, project string) (engineadmission.RoutineFacts, error) {
			if tid != f.owner.TenantID || project != f.project {
				return engineadmission.RoutineFacts{}, errors.New("foreign fact binding")
			}
			green, allowed, wip := mode.Load() != 2, mode.Load() != 3, 0
			out := engineadmission.RoutineFacts{ProjectID: project, MainGreen: &green, CIAllowed: &allowed, WorkPRs: &wip}
			err := tx.QueryRow(ctx, `SELECT clock_timestamp(),coalesce((SELECT (snapshot->>'revision')::bigint FROM delivery_work_settings WHERE project_id=$1),0)`, project).Scan(&out.ObservedAt, &out.PolicyRevision)
			if mode.Load() == 4 {
				out.ObservedAt = out.ObservedAt.Add(-time.Minute)
			}
			if mode.Load() == 5 {
				out.MainGreen = nil
			}
			return out, err
		}}
	e := New(f.d.App, options)
	return &dispatchFixture{fixture: f, e: e, options: options, lead: tenant.Principal{ID: coordinator, TenantID: f.owner.TenantID, Kind: tenant.Agent, Scopes: authz.CoordinatorKeyScopes, KeyID: key, AuthKeyID: key, KeyCreatorID: f.owner.ID}, session: session, lease: lease, agentRun: agentRun, mode: &mode}
}

// Risks: duplicate assignments/charges/queue turns, stale or revoked authority,
// missing mandatory readings, bypassing Arion/host holds and misleading partial
// recovery. Existing consent/budget/pairing guards are reused. The PostgreSQL
// barrier proves scheduler and real coordinator pickup overlap on one intent.
func TestRoutineDispatchRaceRecoveryAndAdmission(t *testing.T) {
	f := setup(t, "off", false)
	// Observe the committed turn writes from both independent connections;
	// the queue event count alone cannot detect a replay advancing a turn.
	f.exec(`CREATE TABLE routine_dispatch_turn_observations(tenant_id uuid,project_id uuid,at timestamptz)`)
	f.exec(`GRANT SELECT,INSERT ON routine_dispatch_turn_observations TO PUBLIC`)
	f.exec(`CREATE FUNCTION observe_routine_dispatch_turn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 INSERT INTO routine_dispatch_turn_observations VALUES(NEW.tenant_id,NEW.project_id,NEW.last_turn_at); RETURN NEW; END $$`)
	f.exec(`CREATE TRIGGER observe_routine_dispatch_turn AFTER UPDATE OF last_turn_at ON project_leads
 FOR EACH ROW WHEN (NEW.last_turn_at IS DISTINCT FROM OLD.last_turn_at) EXECUTE FUNCTION observe_routine_dispatch_turn()`)
	d := setupDispatchOn(t, f)
	e, options, mode := d.e, d.options, d.mode
	coordinator, session, key, agentRun, lease := d.lead.ID, d.session, d.lead.KeyID, d.agentRun, d.lease
	dispatch := func(ctx context.Context, pool *pgxpool.Pool, id, expectedSession string, generation int64) (agentruns.RoutineQueueResult, error) {
		var out agentruns.RoutineQueueResult
		err := db.InTenant(db.AllProjects(ctx, "routine dispatch fixture"), pool, f.owner.TenantID, func(tx pgx.Tx) error {
			var err error
			out, err = e.DispatchTx(ctx, tx, f.owner.TenantID, id, expectedSession, generation)
			return err
		})
		return out, err
	}
	assertEmpty := func() {
		t.Helper()
		must(t, f.tx(func(tx pgx.Tx) error {
			var empty bool
			if err := tx.QueryRow(t.Context(), `SELECT (SELECT queue_routed_at IS NULL AND account_id IS NULL FROM agent_runs WHERE id=$1)
 AND NOT EXISTS(SELECT 1 FROM routine_budget_grants WHERE run_id=$2)
 AND NOT EXISTS(SELECT 1 FROM routine_launch_intents WHERE routine_run_id=$2 AND kind='worker')
 AND (SELECT last_turn_at IS NULL FROM project_leads WHERE project_id=$3)`, agentRun, f.run, f.project).Scan(&empty); err != nil {
				return err
			}
			if !empty {
				t.Fatal("WAIT left a provisional route, hold, launch or turn")
			}
			return nil
		}))
	}
	wait := func(want string, expectedSession string, generation int64) {
		t.Helper()
		out, err := dispatch(t.Context(), f.d.App, f.run, expectedSession, generation)
		must(t, err)
		if out.Routed || out.WaitReason != want {
			t.Fatalf("dispatch=%+v, want WAIT %s", out, want)
		}
		assertEmpty()
	}
	// A disabled project does not alter even its outbox receipt.
	f.exec(`UPDATE project_routine_execution_settings SET automatic_launch_enabled=false WHERE project_id=$1`, f.project)
	pass, err := e.RunTenant(t.Context(), f.owner.TenantID)
	must(t, err)
	if pass.Scanned != 0 || pass.Partial {
		t.Fatalf("default-off pass=%+v", pass)
	}
	wait("automatic_launch_disabled", "", 0)
	f.exec(`UPDATE project_routine_execution_settings SET automatic_launch_enabled=true WHERE project_id=$1`, f.project)
	f.exec(`UPDATE recurrence_definitions SET execute_consent=false WHERE recurrence_id=$1`, f.routine.ID)
	wait("execution_consent_required", "", 0)
	f.exec(`UPDATE recurrence_definitions SET execute_consent=true WHERE recurrence_id=$1`, f.routine.ID)
	// Revoke this owner's binding while retaining a different active workspace
	// owner, as required by the production last-owner guard.
	var otherOwner string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other owner') RETURNING id::text`, f.owner.TenantID).Scan(&otherOwner))
	dbtest.BindRole(t, f.d, f.owner.TenantID, otherOwner, "owner")
	f.exec(`DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.owner.TenantID, f.owner.ID)
	wait("owner_unavailable", "", 0)
	dbtest.BindRole(t, f.d, f.owner.TenantID, f.owner.ID, "owner")
	wait("stale_lead_generation", session, 2)
	mode.Store(1)
	wait("lead admission wait: host_unavailable", "", 0)
	mode.Store(2)
	wait("main_red", "", 0)
	mode.Store(3)
	wait("ci_admission_wait", "", 0)
	mode.Store(4)
	wait("ci_admission_unavailable", "", 0)
	mode.Store(5)
	wait("ci_admission_unavailable", "", 0)
	mode.Store(0)
	f.exec(`INSERT INTO delivery_work_settings(tenant_id,project_id,snapshot) VALUES($1,$2,'{"freeze":true,"revision":1}')`, f.owner.TenantID, f.project)
	wait("release_freeze", "", 0)
	f.exec(`UPDATE delivery_work_settings SET snapshot='{"freeze":false,"revision":2}' WHERE project_id=$1`, f.project)
	f.exec(`UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{unattended,login_session}','"required"') WHERE id=$1`, f.computer)
	wait("unattended_login_required", "", 0)
	f.exec(`UPDATE agent_pairing_computers SET capacity_signals=jsonb_set(capacity_signals,'{unattended,login_session}','"ready"'),capacity_reported_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, f.computer)
	wait("unattended_host_unreachable", "", 0)
	f.exec(`UPDATE agent_pairing_computers SET capacity_reported_at=clock_timestamp() WHERE id=$1`, f.computer)
	f.exec(`UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE id=$1`, key)
	wait("owner_or_dispatch_authority_revoked", "", 0)
	f.exec(`UPDATE agent_keys SET revoked_at=NULL WHERE id=$1`, key)
	// An absent lead keeps one generation-bound demand and never a worker hold.
	f.exec(`UPDATE project_leads SET session_id=NULL,state='waiting_for_room' WHERE project_id=$1`, f.project)
	wait("lead_demand_pending", "", 0)
	wait("lead_demand_pending", "", 0)
	var demands int
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM routine_launch_intents WHERE project_id=$1 AND kind='lead'`, f.project).Scan(&demands))
	if demands != 1 {
		t.Fatalf("duplicate lead demand: %d", demands)
	}
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='routine.lead_demand_created'`, f.project).Scan(&demands))
	if demands != 1 {
		t.Fatalf("duplicate/missing lead demand audit: %d", demands)
	}
	f.exec(`UPDATE project_leads SET session_id=$2,state='working' WHERE project_id=$1`, f.project, session)
	// Refuse a nonroutine ID at the internal boundary.
	_, err = dispatch(t.Context(), f.d.App, agentRun, "", 0)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ordinary run used routine path: %v", err)
	}
	assertEmpty()
	// The scheduler pauses while owning the tenant fence; the coordinator's
	// real HTTP pickup must contend before either can return a route.
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.HasPrefix(q, "INSERT INTO routine_attempts") })
	first := make(chan error, 1)
	go func() { _, err := dispatch(ctx, pool, f.run, "", 0); first <- err }()
	pid := barrier.Wait(t, ctx)
	mux := http.NewServeMux()
	module := agentruns.NewWithLeadAdmission(f.d.App, options.LeadAdmission)
	must(t, e.Bind(module))
	module.Mount(mux)
	second := make(chan *httptest.ResponseRecorder, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p := tenant.Principal{ID: coordinator, TenantID: f.owner.TenantID, Kind: tenant.Agent, Scopes: authz.CoordinatorKeyScopes, KeyID: key, AuthKeyID: key, KeyCreatorID: f.owner.ID}
		r := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(`{}`)).WithContext(tenant.WithPrincipal(ctx, p))
		r.Header.Set("X-Aeon-Lead-Session", session)
		r.Header.Set("X-Aeon-Lead-Generation", "1")
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		second <- w
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock == "" {
		t.Fatal("scheduler and coordinator never overlapped")
	}
	barrier.Release()
	must(t, dbtest.Await(t, ctx, first))
	w := dbtest.Await(t, ctx, second)
	if w.Code != 200 || !strings.Contains(w.Body.String(), agentRun) {
		t.Fatalf("coordinator replay=%d %s", w.Code, w.Body.String())
	}
	var turn time.Time
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT last_turn_at FROM project_leads WHERE project_id=$1`, f.project).Scan(&turn))
	// Restart/lost reply uses precisely the prior intent, reservation and turn.
	e = New(f.d.App, options)
	out, err := dispatch(t.Context(), f.d.App, f.run, "", 0)
	must(t, err)
	if !out.Routed || !out.Replay || out.RunID != agentRun {
		t.Fatalf("restart replay=%+v", out)
	}
	var unchanged bool
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT last_turn_at=$3 FROM project_leads WHERE project_id=$1)
 AND (SELECT count(*)=1 FROM routine_budget_grants WHERE run_id=$2)
 AND (SELECT count(*)=1 FROM routine_attempts WHERE run_id=$2)
 AND (SELECT count(*)=1 FROM routine_launch_intents WHERE routine_run_id=$2 AND kind='worker')
 AND (SELECT count(*)=1 FROM account_reservations WHERE run_id=$4)
	AND (SELECT count(*)=1 FROM routine_dispatch_turn_observations WHERE project_id=$1)
 AND (SELECT count(*)=1 FROM events WHERE node_id=$5 AND type='queue.routed')`, f.project, f.run, turn, agentRun, f.work).Scan(&unchanged))
	if !unchanged {
		t.Fatal("replay duplicated or changed a dispatch receipt")
	}
	// A capped recovery pass reports partial and writes explicit preparation
	// waits, retaining original receipts and never starting a second worker.
	for i := 0; i < 33; i++ {
		f.call("POST", "/api/recurrences/"+f.routine.ID+"/run-now", map[string]string{"idempotency_key": fmt.Sprintf("scan-%d", i)}, nil)
	}
	pass, err = e.RunTenant(t.Context(), f.owner.TenantID)
	must(t, err)
	if !pass.Partial || pass.Scanned != batchSize || pass.Dispatched != 0 || pass.Waiting != batchSize {
		t.Fatalf("capped recovery=%+v", pass)
	}
}

// Risk: a paired account has exactly one slot. Dispatch and both launch checks
// must reuse its held slot and allowance, while another owned account cannot
// supply capacity for the selected account.
func TestRoutineDispatchReservedAccountAdmission(t *testing.T) {
	d := setupDispatch(t)
	f := d.fixture
	f.exec(`UPDATE agent_accounts SET quota_fingerprint=repeat('e',64),quota_pool_fingerprint=repeat('e',64) WHERE id=$1`, f.account)
	f.exec(`INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model) VALUES($1,$2,clock_timestamp()-interval '1 hour',clock_timestamp()+interval '1 day','requests',1,0,'unrestricted')`, f.owner.TenantID, f.account)
	var out agentruns.RoutineQueueResult
	must(t, f.tx(func(tx pgx.Tx) error {
		var err error
		out, err = d.e.DispatchTx(t.Context(), tx, f.owner.TenantID, f.run, d.session, 1)
		return err
	}))
	if !out.Routed || out.WaitReason != "" {
		t.Fatalf("one-slot paired dispatch=%+v", out)
	}
	var grant agentsetup.RoutineGrant
	var raw []byte
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT binding FROM routine_launch_intents WHERE routine_run_id=$1 AND kind='worker'`, f.run).Scan(&raw))
	must(t, json.Unmarshal(raw, &grant))
	must(t, f.tx(func(tx pgx.Tx) error { return d.e.LaunchChecks().Admission(t.Context(), tx, f.owner, grant) }))
	// The ordinary advisory can see a spare owned account; the run-bound gate
	// must still refuse its selected account when that account is unavailable.
	var spare string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,label,registered_by_principal_id,owner_person_id,capacity_owner,linked_at,last_probe_at,last_probe_ok,last_daemon_generation)
 SELECT tenant_id,'spare',harness,daemon_id,'Spare',registered_by_principal_id,owner_person_id,capacity_owner,linked_at,clock_timestamp(),true,last_daemon_generation FROM agent_accounts WHERE id=$1 RETURNING id::text`, f.account).Scan(&spare))
	f.exec(`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at)
 SELECT tenant_id,$2,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at FROM agent_pairing_enrollments WHERE account_id=$1`, f.account, spare)
	f.exec(`INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source)
 SELECT tenant_id,$2,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source FROM account_allowance_windows WHERE account_id=$1 AND unit='percent'`, f.account, spare)
	f.exec(`INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) SELECT tenant_id,principal_id,scope,$2::uuid::text,$2,schedule FROM account_capacity_schedules WHERE account_id=$1`, f.account, spare)
	// A sibling sharing the quota still consumes the one paired slot. Exclude
	// only this run, never all work on the selected account or quota pool.
	f.exec(`UPDATE agent_accounts SET quota_fingerprint=repeat('e',64),quota_pool_fingerprint=repeat('e',64) WHERE id=$1`, spare)
	var sibling string
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,account_id,status) SELECT tenant_id,work_order_id,agent_principal_id,model_profile_id,$2,'queued' FROM agent_runs WHERE id=$1 RETURNING id::text`, d.agentRun, spare).Scan(&sibling))
	err := f.tx(func(tx pgx.Tx) error {
		return engineadmission.RequireRoutineTx(t.Context(), tx, f.owner, f.run, d.agentRun, "codex", 1, d.options.Facts)
	})
	var refusal *workorders.Error
	if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Message != "account_capacity" {
		t.Fatalf("sibling quota occupancy omitted: %v", err)
	}
	f.exec(`UPDATE agent_runs SET status='cancelled' WHERE id=$1`, sibling)
	f.exec(`UPDATE agent_accounts SET quota_pool_fingerprint='' WHERE id=$1`, spare)
	f.exec(`UPDATE agent_accounts SET state='unavailable' WHERE id=$1`, f.account)
	must(t, f.tx(func(tx pgx.Tx) error {
		var now time.Time
		if err := tx.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		capacity, err := agentaccounts.EngineCapacityTx(t.Context(), tx, f.owner, f.owner.ID, "codex", 1, now, f.project)
		if err != nil {
			return err
		}
		if capacity.Reason != "allowed" {
			t.Fatalf("spare account not admitted: %+v", capacity)
		}
		return nil
	}))
	err = f.tx(func(tx pgx.Tx) error {
		return engineadmission.RequireRoutineTx(t.Context(), tx, f.owner, f.run, d.agentRun, "codex", 1, d.options.Facts)
	})
	if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Message != "account_state" {
		t.Fatalf("spare account admitted selected unavailable account: %v", err)
	}
	f.exec(`UPDATE agent_accounts SET state='available' WHERE id=$1`, f.account)
	module := agentruns.NewWithLeadAdmission(f.d.App, d.options.LeadAdmission)
	must(t, d.e.Bind(module))
	mux := http.NewServeMux()
	module.Mount(mux)
	var handoff agentruns.WorkerHandoff
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT trace->'worker_assignment' FROM agent_runs WHERE id=$1`, d.agentRun).Scan(&raw))
	must(t, json.Unmarshal(raw, &handoff))
	rows, err := f.d.Admin.Query(t.Context(), `SELECT id::text FROM account_reservations WHERE run_id=$1 AND state='active' ORDER BY id`, d.agentRun)
	must(t, err)
	var reservations []string
	for rows.Next() {
		var id string
		must(t, rows.Scan(&id))
		reservations = append(reservations, id)
	}
	must(t, rows.Err())
	rows.Close()
	if len(reservations) != 2 {
		t.Fatalf("expected measured and manual holds, got %v", reservations)
	}
	worktree := strings.Repeat("7", 64)
	body, err := json.Marshal(map[string]any{"daemon_id": grant.DaemonID, "daemon_generation": grant.DaemonGeneration, "reservation_ids": reservations,
		"routine": agentsetup.RoutinePickup{LaunchID: grant.LaunchID, WorktreeID: worktree, Capability: grant.Capability, DaemonDigest: grant.DaemonDigest, CapabilityDigest: grant.CapabilityDigest, ComputerID: grant.ComputerID},
		"handoff": agentruns.WorkerPickup{TicketRevision: handoff.TicketRevision, WorkOrderRevision: handoff.WorkOrderRevision, BriefSHA256: handoff.BriefSHA256, WorktreeID: worktree}})
	must(t, err)
	req := httptest.NewRequest("POST", "/api/runs/"+d.agentRun+"/claim", strings.NewReader(string(body))).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
	req.Header.Set(agentpairing.LedgerGenerationHeader, grant.LedgerGeneration)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("reserved one-slot daemon claim=%d %s", w.Code, w.Body.String())
	}
	var claimed bool
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT status='starting' AND account_id=$2::uuid FROM agent_runs WHERE id=$1) AND (SELECT state='claimed' FROM routine_launch_intents WHERE agent_run_id=$1)`, d.agentRun, f.account).Scan(&claimed))
	if !claimed {
		t.Fatal("daemon claim did not retain the selected account and launch")
	}
}

// Risks: one routine's WAIT must neither stall ordinary lead pickup with the
// project flag off nor consume the turn of a later consented routine. Scheduler
// dispatch has no HTTP principal; queue admission must use the checked lead.
func TestRoutineQueueWaitsDoNotBlockLaterWork(t *testing.T) {
	d := setupDispatch(t)
	f := d.fixture
	module := agentruns.NewWithLeadAdmission(f.d.App, d.options.LeadAdmission)
	must(t, d.e.Bind(module))
	mux := http.NewServeMux()
	module.Mount(mux)
	pickup := func(target string) (string, string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/queue/next", strings.NewReader(target)).WithContext(tenant.WithPrincipal(t.Context(), d.lead))
		req.Header.Set("X-Aeon-Lead-Session", d.session)
		req.Header.Set("X-Aeon-Lead-Generation", "1")
		req.Header.Set("X-Aeon-Worker-Lease", d.lease)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("pickup=%d %s", w.Code, w.Body.String())
		}
		var out struct {
			Entry *struct {
				NodeID string `json:"node_id"`
			} `json:"entry"`
			WaitReason string `json:"wait_reason"`
		}
		must(t, json.Unmarshal(w.Body.Bytes(), &out))
		if out.Entry != nil {
			return out.Entry.NodeID, out.WaitReason
		}
		return "", out.WaitReason
	}
	var ordinary string
	must(t, f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,body,parent_id,fields) SELECT $1,id,aeon_next_node_key($1,short_prefix),'Ordinary work','Validate ordinary routing',$2,'{"status":"open","priority":"high","estimate_hours":1,"acceptance_criteria":["Validate"]}' FROM node_kinds WHERE slug='work' RETURNING id::text`, f.owner.TenantID, f.parent).Scan(&ordinary); err != nil {
			return err
		}
		_, err := workqueue.EnqueueSystem(t.Context(), tx, f.owner, ordinary)
		return err
	}))
	f.exec(`UPDATE project_routine_execution_settings SET automatic_launch_enabled=false WHERE project_id=$1`, f.project)
	if node, reason := pickup(`{}`); node != ordinary || reason != "" {
		t.Fatalf("disabled routine blocked ordinary work: node=%s wait=%s", node, reason)
	}
	if node, reason := pickup(`{}`); node != ordinary || reason != "" {
		t.Fatalf("ordinary replay blocked: node=%s wait=%s", node, reason)
	}
	f.exec(`UPDATE agent_runs SET status='cancelled' WHERE queue_node_id=$1`, ordinary)
	if node, reason := pickup(`{}`); node != "" || reason != "automatic_launch_disabled" {
		t.Fatalf("first routine wait not reported: node=%s wait=%s", node, reason)
	}
	if node, reason := pickup(fmt.Sprintf(`{"agent_principal_id":%q,"model_profile_id":%q}`, f.agent.ID, f.profile)); node != "" || reason != "routine_registry_route_required" {
		t.Fatalf("registry wait not reported: node=%s wait=%s", node, reason)
	}
	// A second definition keeps live consent while the earlier one waits.
	f.exec(`UPDATE project_routine_execution_settings SET automatic_launch_enabled=true WHERE project_id=$1`, f.project)
	var definition recurrences.Recurrence
	f.call("POST", "/api/recurrences", f.routine.Input, &definition)
	path := "/api/recurrences/" + definition.ID
	f.call("POST", path+"/resume", map[string]any{"expected_revision": definition.Revision}, nil)
	f.call("GET", path, nil, &definition)
	f.call("PUT", path+"/execution-consent", map[string]any{"expected_revision": definition.Revision, "execute_consent": true}, nil)
	var later recurrences.Occurrence
	f.call("POST", path+"/run-now", map[string]string{"idempotency_key": "later"}, &later)
	// A dispatchable earlier routine retains its turn, but the eligibility
	// probe cannot leave a route, hold, event or launch behind.
	var beforeEvents int
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, f.owner.TenantID).Scan(&beforeEvents))
	var waiting agentruns.RoutineQueueResult
	if _, ok := tenant.PrincipalFrom(t.Context()); ok {
		t.Fatal("scheduler regression requires a context without an HTTP principal")
	}
	must(t, f.tx(func(tx pgx.Tx) error {
		var err error
		waiting, err = d.e.DispatchTx(t.Context(), tx, f.owner.TenantID, later.Run.ID, d.session, 1)
		return err
	}))
	if waiting.Routed || waiting.WaitReason != "queue_order_wait" {
		t.Fatalf("eligible earlier routine lost its turn: %+v", waiting)
	}
	var clean bool
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT NOT EXISTS(SELECT 1 FROM routine_budget_grants WHERE run_id IN($1,$2)) AND NOT EXISTS(SELECT 1 FROM routine_launch_intents WHERE routine_run_id IN($1,$2)) AND NOT EXISTS(SELECT 1 FROM agent_runs WHERE id IN($3,$4) AND (queue_routed_at IS NOT NULL OR account_id IS NOT NULL)) AND (SELECT count(*)=$5 FROM events WHERE tenant_id=$6)`, f.run, later.Run.ID, d.agentRun, *later.Run.AgentRunID, beforeEvents, f.owner.TenantID).Scan(&clean))
	if !clean {
		t.Fatal("rollback-only earlier routine probe committed dispatch artifacts")
	}
	f.exec(`UPDATE recurrence_definitions SET execute_consent=false WHERE recurrence_id=$1`, f.routine.ID)
	var out agentruns.RoutineQueueResult
	must(t, f.tx(func(tx pgx.Tx) error {
		var err error
		out, err = d.e.DispatchTx(t.Context(), tx, f.owner.TenantID, later.Run.ID, d.session, 1)
		return err
	}))
	if !out.Routed || out.WaitReason != "" || out.RunID != *later.Run.AgentRunID {
		t.Fatalf("earlier no-consent routine blocked later routine: %+v", out)
	}
}

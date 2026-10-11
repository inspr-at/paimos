// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	"github.com/inspr-at/paimos/internal/events"
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
	token                                                          string
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
	return setupWithActions(t, mode, paid, []string{"work.create"}, "personal")
}

func setupWithActions(t *testing.T, mode string, paid bool, actions []string, scopeKind string) *fixture {
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
	scope := recurrences.DefinitionScope{Kind: scopeKind}
	if scopeKind == "project" {
		scope.ProjectID = f.project
	}
	in := recurrences.Input{ProjectID: f.project, ParentID: f.parent, QueueEach: true, OverlapPolicy: "create", CatchUpPolicy: "one", Template: recurrences.Template{Title: "Budget work", Description: "Assigned workspace", Criteria: []string{"Validate"}, EstimateHours: 1, Priority: "high", Type: "ticket"}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "UTC"}, Definition: &recurrences.Definition{Scope: scope, OwnerPrincipalID: f.owner.ID, Assignment: &recurrences.Assignment{Goal: "Bounded fixture", Sources: []recurrences.SourceReference{}, Role: "build", WorkKindID: kind, AllowedActions: actions, RuntimeRequirements: recurrences.RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: budget}}}
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
	consent := map[string]any{"expected_revision": f.routine.Revision, "execute_consent": true}
	if scopeKind == "project" {
		var service, role string
		must(t, f.tx(func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_access_configured) VALUES($1,'agent','Controlled routine service',true) RETURNING id::text`, f.owner.TenantID).Scan(&service); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'controlled_routine','Controlled routine') RETURNING id::text`, f.owner.TenantID).Scan(&role); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.owner.TenantID, role, []string{"nodes.read", "run.create", "work_orders.write", "nodes.write"}); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.owner.TenantID, service, role, f.project)
			return err
		}))
		consent["execution_principal_id"] = service
	}
	f.call("PUT", path+"/execution-consent", consent, nil)
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
	var request, key, prefix string
	// The daemon's claim re-reads its exact scoped key from the bearer token.
	secret := "routine-dispatch-fixture-not-a-credential"
	sum := sha256.Sum256([]byte(secret))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'Fixture',replace(gen_random_uuid()::text,'-',''),$3,$4,$5) RETURNING id::text,prefix`, f.owner.TenantID, f.agent.ID, hex.EncodeToString(sum[:]), f.agent.Scopes, f.owner.ID).Scan(&key, &prefix))
	f.token = "aeon_" + prefix + "_" + secret
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,gen_random_uuid(),'887887887',repeat('a',64),repeat('b',64),repeat('c',64),'{"platform":"darwin","arch":"arm64"}','fixture','redeemed',$2) RETURNING id::text`, f.owner.TenantID, f.owner.ID).Scan(&request))
	must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,capacity_signals,capacity_reported_at,ledger_generation,ledger_enrolled_at) VALUES($1,gen_random_uuid(),$2,$3,$4,'budget-daemon',repeat('c',64),'{"load":5,"cores":18,"memory_pressure":"normal","power":"plugged_in","thermal":"normal","unattended":{"login_session":"ready","idle_sleep":"disabled","filevault":"on"}}',clock_timestamp(),gen_random_uuid()::text,clock_timestamp()) RETURNING id::text`, f.owner.TenantID, request, f.agent.ID, key).Scan(&f.computer))
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
		// The Default's Models order runs the budget profile (a build ladder
		// before Models v2 migration 1333).
		if err := dbtest.ModelsOrder(t.Context(), tx, f.owner.TenantID, "other", "first", "wait", f.profile); err != nil {
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
	// Main creates new intent as created; released pending intent keeps the
	// same admission checks, while an unknown state must still refuse dispatch.
	f.exec(`UPDATE routine_runs SET state='pending' WHERE id=$1`, f.run)
	wait("main_red", "", 0)
	f.exec(`UPDATE routine_runs SET state='future_state' WHERE id=$1`, f.run)
	wait("routine_run_unavailable", "", 0)
	f.exec(`UPDATE routine_runs SET state='created' WHERE id=$1`, f.run)
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
	// A daemon without ledger enrolment cannot satisfy the launch binding; the
	// pass waits with that reason instead of failing on the missing generation.
	var ledger string
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT ledger_generation FROM agent_pairing_computers WHERE id=$1`, f.computer).Scan(&ledger))
	f.exec(`UPDATE agent_pairing_computers SET ledger_generation=NULL,ledger_enrolled_at=NULL WHERE id=$1`, f.computer)
	wait("routine_ledger_enrollment_required", "", 0)
	f.exec(`UPDATE agent_pairing_computers SET ledger_generation=$2,ledger_enrolled_at=clock_timestamp() WHERE id=$1`, f.computer, ledger)
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
	// The known dispatched state retains its held grant; unknown states never
	// inherit that allowance merely because dispatch already succeeded.
	f.exec(`UPDATE routine_runs SET state='future_state' WHERE id=$1`, f.run)
	if err := f.tx(func(tx pgx.Tx) error { return d.e.LaunchChecks().Admission(t.Context(), tx, f.owner, grant) }); err != nil {
		var refusal *workorders.Error
		if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Message != "routine_run_unavailable" {
			t.Fatalf("unknown routine state refused for the wrong reason: %v", err)
		}
	} else {
		t.Fatal("unknown routine state allowed daemon admission")
	}
	f.exec(`UPDATE routine_runs SET state='dispatched' WHERE id=$1`, f.run)
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
	module := agentruns.NewWithLeadAdmission(f.d.App, d.options.LeadAdmission, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, run agentruns.Run, _ agentruns.Telemetry, pending *[]events.Change) error {
		return agentaccounts.SettleDeferred(ctx, tx, p, run.ID, pending)
	})
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
	req.Header.Set("Authorization", "Bearer "+f.token)
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
	// The owner's personal routine stays hidden from the daemon's own view,
	// yet its exact launch and exit evidence must record and release holds.
	telemetry := func(body string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/runs/"+d.agentRun+"/telemetry", strings.NewReader(body)).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set(agentpairing.LedgerGenerationHeader, grant.LedgerGeneration)
		req.Header.Set(agentruns.DaemonHeader, grant.DaemonID)
		req.Header.Set(agentruns.GenerationHeader, grant.DaemonGeneration)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("routine telemetry %s=%d %s", body, w.Code, w.Body.String())
		}
	}
	telemetry(`{"sequence":1,"kind":"started"}`)
	telemetry(`{"sequence":2,"kind":"finished","status":"failed","process_state":"exited"}`)
	var exited, evidence, settled bool
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT state='exit_observed' AND exit_observed_at IS NOT NULL FROM routine_launch_intents WHERE agent_run_id=$1),
 (SELECT count(*)=2 FROM routine_launch_evidence e JOIN routine_launch_intents l ON l.id=e.launch_id WHERE l.agent_run_id=$1 AND e.kind IN ('launched','exit_observed')),
 NOT EXISTS(SELECT 1 FROM account_reservations WHERE run_id=$1 AND state='active')`, d.agentRun).Scan(&exited, &evidence, &settled))
	if !exited || !evidence || !settled {
		t.Fatalf("personal routine exit observed=%v evidence=%v settled=%v", exited, evidence, settled)
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

// Risk: a committed claim disappears from discovery after its reply is lost;
// an unlaunched actor leaks its session/hold, or abandonment clears a started
// actor, another daemon's claim, or partial settlement after a callback failure.
func TestLeadRuntimeClaimReplayAndAbandon(t *testing.T) {
	for _, mode := range []string{"expired_reply", "unlaunched", "started", "settlement_failure", "unsettled"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t, "off", false)
			f.agent.Scopes = []string{"harness.worker", "run.claim", "run.telemetry"}
			// A real scoped key is required by the handler's final-write auth boundary.
			keySecret := "fixture-lead-runtime-proof"
			keyHash := sha256.Sum256([]byte(keySecret))
			must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'Lead replay','lead-replay',$3,$4,$5) RETURNING id::text`, f.owner.TenantID, f.agent.ID, fmt.Sprintf("%x", keyHash[:]), f.agent.Scopes, f.owner.ID).Scan(&f.agent.AuthKeyID))

			// Use a project-scoped routine; every daemon read remains under real RLS.
			f.exec(`UPDATE recurrence_definitions SET scope_type='project',scope_project_id=$2 WHERE recurrence_id=$1`, f.routine.ID, f.project)
			f.exec(`UPDATE routine_runs SET scope_type='project',scope_project_id=$2 WHERE id=$1`, f.run, f.project)
			var actor, order, session, attempt, budget string
			must(t, f.d.Admin.QueryRow(t.Context(), `SELECT agent_run_id::text,work_order_id::text FROM routine_runs WHERE id=$1`, f.run).Scan(&actor, &order))
			f.exec(`UPDATE agent_runs SET agent_principal_id=$2,status='starting',model_profile_id=$3,account_id=$4 WHERE id=$1`, actor, f.agent.ID, f.profile, f.account)
			ref, lease := strings.Repeat("r", 32), strings.Repeat("l", 32)
			refHash := sha256.Sum256(append([]byte("aeon.harness.ref"), append([]byte{0}, []byte(ref)...)...))
			leaseHash := sha256.Sum256(append([]byte("aeon.harness.lease"), append([]byte{0}, []byte(lease)...)...))
			must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,run_id,work_order_id,harness,host,management,role,phase,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,$6,'codex','local','managed','coordinator','starting',$7,$8) RETURNING id::text`, f.owner.TenantID, f.project, f.agent.ID, f.owner.ID, actor, order, refHash[:], leaseHash[:]).Scan(&session))
			f.exec(`INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state,revision) VALUES($1,$2,$3,$4,1,'starting',2)`, f.owner.TenantID, f.project, f.owner.ID, session)
			f.exec(`INSERT INTO project_lead_generations(tenant_id,project_id,generation,session_id) VALUES($1,$2,1,$3)`, f.owner.TenantID, f.project, session)
			must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,agent_run_id,principal_id,assignment_digest) SELECT tenant_id,id,'lead-recovery','lead',$2,$3,assignment_digest FROM routine_runs WHERE id=$1 RETURNING id::text`, f.run, actor, f.agent.ID).Scan(&attempt))
			f.exec(`INSERT INTO routine_budget_balances(tenant_id,run_id,owner_principal_id,mode,recovery_ceiling_ms,held_ms) VALUES($1,$2,$3,'off',120000,60000)`, f.owner.TenantID, f.run, f.owner.ID)
			must(t, f.d.Admin.QueryRow(t.Context(), `INSERT INTO routine_budget_grants(tenant_id,run_id,owner_principal_id,attempt_id,action_id,grant_key,agent_run_id,account_id,harness,billing_mode,maximum_tokens,maximum_microusd,maximum_ms) VALUES($1,$2,$3,$4,$5,'lead',$6,$7,'codex','subscription',0,0,60000) RETURNING id::text`, f.owner.TenantID, f.run, f.owner.ID, attempt, f.action, actor, f.account).Scan(&budget))
			demand := agentsetup.LeadDemand{ID: "10000000-0000-4000-8000-000000000090", ProjectID: f.project, OwnerID: f.owner.ID, RoutineRunID: f.run, Revision: 1}
			pickup := agentsetup.LeadPickup{Demand: demand, DaemonID: "budget-daemon", DaemonGeneration: "budget-generation", LedgerGeneration: "10000000-0000-4000-8000-000000000091", AccountID: f.account, ComputerID: f.computer, DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), Capability: agentsetup.LeadExecutionCapability, WorktreeID: strings.Repeat("d", 64), Ref: ref, Lease: lease}
			launch := agentsetup.LeadLaunch{Demand: demand, SessionID: session, Generation: 1, ProfileID: f.profile, Harness: "codex", Model: "fixture-model", Effort: "high", Host: "local", MaxDurationSeconds: 60, Grant: agentsetup.RoutineGrant{LaunchID: demand.ID, TenantID: f.owner.TenantID, OwnerID: f.owner.ID, ProjectID: f.project, RoutineRunID: f.run, AgentRunID: actor, PrincipalID: f.agent.ID, ExecutionPrincipalID: f.owner.ID, AccountID: f.account, ComputerID: f.computer, DaemonID: pickup.DaemonID, DaemonGeneration: pickup.DaemonGeneration, LedgerGeneration: pickup.LedgerGeneration, DaemonDigest: pickup.DaemonDigest, CapabilityDigest: pickup.CapabilityDigest, BudgetGrantID: budget, ExpiresAt: time.Unix(100, 0).UTC()}}
			launch.Grant.QualificationID = f.gate.QualificationID
			launch.Grant.DefinitionRevision, launch.Grant.ConsentRevision, launch.Grant.ExecutionRevision = 1, 1, 1
			launch.Grant.HostMappingDigest = strings.Repeat("d", 64)
			launch.Grant.Capability = agentsetup.RoutineExecutionCapability
			launch.Grant.RuntimeCapability = "routine_native_coding_v1"
			launch.Grant.BudgetMode = "off"
			must(t, f.d.Admin.QueryRow(t.Context(), `SELECT assignment_digest FROM routine_runs WHERE id=$1`, f.run).Scan(&launch.Grant.AssignmentDigest))
			must(t, launch.Grant.Validate())
			binding, err := json.Marshal(map[string]any{"launch": launch, "worktree_id": pickup.WorktreeID, "ref_digest": refHash[:], "lease_digest": leaseHash[:]})
			must(t, err)
			f.exec(`INSERT INTO routine_launch_intents(tenant_id,id,routine_run_id,agent_run_id,project_id,owner_principal_id,worker_principal_id,kind,lead_generation,binding,state,worktree_id) VALUES($1,$2,$3,$4,$5,$6,$7,'lead',0,$8,'claimed',$9)`, f.owner.TenantID, demand.ID, f.run, actor, f.project, f.owner.ID, f.agent.ID, binding, pickup.WorktreeID)
			f.exec(`INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES($1,$2,'claimed',$3)`, f.owner.TenantID, demand.ID, binding)
			fail := mode == "settlement_failure"
			calls := 0
			checks := harness.LeadRuntimeChecks{Abandon: func(ctx context.Context, tx pgx.Tx, p tenant.Principal, l agentsetup.LeadLaunch) error {
				calls++
				if p.ID != f.agent.ID || l.SessionID != session || l.Grant.BudgetGrantID != budget {
					return errors.New("wrong actor settled")
				}
				if mode == "unsettled" {
					return nil
				}
				_, err := f.broker.SettleTx(ctx, tx, p, f.run, budget, routinebudget.Settlement{EventKey: "lead-abandon", UsageComplete: true, TokensKnown: true, PaidCostKnown: true, ProvenUnused: true, EvidenceDigest: strings.Repeat("a", 64)})
				if err != nil {
					return err
				}
				if fail {
					return workorders.Fail(409, "fixture_settlement_failed")
				}
				return nil
			}}
			mux := http.NewServeMux()
			harness.New(f.d.App).(*harness.Module).WithLeadRuntime(checks).Mount(mux)
			call := func(suffix string, body any, want int) []byte {
				t.Helper()
				raw, err := json.Marshal(body)
				must(t, err)
				method := "POST"
				if suffix == "" {
					method = "GET"
				}
				req := httptest.NewRequest(method, "/api/projects/"+f.project+"/lead-runtime"+suffix, strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
				req.Header.Set("Authorization", "Bearer aeon_lead-replay_"+keySecret)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != want {
					t.Fatalf("%s: got %d want %d: %s", suffix, w.Code, want, w.Body.String())
				}
				return w.Body.Bytes()
			}
			// The real server hides the claimed demand. Private claim replay must still
			// retrieve exactly the old recipe, including its expired (unrenewed) grant.
			if out := string(call("", nil, 200)); !strings.Contains(out, `"state":"reused"`) || !strings.Contains(out, `"demand":null`) {
				t.Fatal(out)
			}
			var replay agentsetup.LeadLaunch
			must(t, json.Unmarshal(call("/claim", pickup, 200), &replay))
			if !reflect.DeepEqual(replay, launch) {
				t.Fatal("claim replay changed its recipe")
			}
			forged := pickup
			forged.DaemonGeneration = "other-generation"
			if out := string(call("/report", map[string]any{"pickup": forged, "kind": "abandon"}, 409)); !strings.Contains(out, "lead_writer_conflict") {
				t.Fatal(out)
			}
			if mode == "started" {
				f.exec(`INSERT INTO routine_launch_evidence(tenant_id,launch_id,kind,receipt) VALUES($1,$2,'started','{}')`, f.owner.TenantID, demand.ID)
			}
			report := map[string]any{"pickup": pickup, "kind": "abandon"}
			want := 200
			if mode == "started" || fail || mode == "unsettled" {
				want = 409
			}
			out := string(call("/report", report, want))
			if mode == "started" && !strings.Contains(out, "lead_start_already_observed") || fail && !strings.Contains(out, "fixture_settlement_failed") || mode == "unsettled" && !strings.Contains(out, "lead_abandon_hold_unsettled") {
				t.Fatal(out)
			}
			assertState := func(abandoned bool) {
				t.Helper()
				var state, runState, leadState, grantState string
				var stopped, terminal bool
				var held int64
				must(t, f.d.Admin.QueryRow(t.Context(), `SELECT i.state,i.exit_observed_at IS NOT NULL,r.status,l.state,aeon_work_session_stopped(s.stopped_at,s.stop_reason),g.state,b.held_ms FROM routine_launch_intents i JOIN agent_runs r ON r.id=i.agent_run_id JOIN project_leads l ON l.project_id=i.project_id JOIN harness_sessions s ON s.id=l.session_id JOIN routine_budget_grants g ON g.id=$2 JOIN routine_budget_balances b ON b.run_id=i.routine_run_id WHERE i.id=$1`, demand.ID, budget).Scan(&state, &terminal, &runState, &leadState, &stopped, &grantState, &held))
				if abandoned {
					if state != "abandoned" || !terminal || runState != "cancelled" || leadState != "waiting_for_room" || !stopped || grantState != "settled" || held != 0 {
						t.Fatalf("abandon incomplete: %s %v %s %s %v %s %d", state, terminal, runState, leadState, stopped, grantState, held)
					}
				} else {
					if state != "claimed" || terminal || runState != "starting" || leadState != "starting" || stopped || grantState != "held" || held != 60000 {
						t.Fatal("refusal did not retain all owners")
					}
				}
			}
			assertState(want == 200)
			if fail {
				fail = false
				call("/report", report, 200)
				assertState(true)
			}
			if mode != "started" && mode != "unsettled" {
				before := calls
				call("/report", report, 200)
				assertState(true)
				if calls != before {
					t.Fatal("replay settled twice")
				}
			}
		})
	}
}

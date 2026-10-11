// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

// Risks: stale consent/assignment, mixed owner/host/account, old capability,
// duplicate claims after response loss and releasing unknown process holds.
// Real S04 consent, S05 lineage, pairing/account fences and final claim are
// reused. A PostgreSQL barrier proves two claims contend on the same intent.
func TestRoutineLaunchClaimReplayAndCurrentAuthority(t *testing.T) {
	f := setup(t)
	f.person.BrowserSession = true
	// The fixture supplies authenticated principals directly; mirror its real
	// scoped key for the final RequireTx checks normally populated by auth.
	f.agent.Scopes = []string{"work_orders.read", "work_orders.write", "run.read", "run.create", "run.claim", "run.telemetry"}
	project, parent, service, role, computer, request, ledger, grantID := uuid(), uuid(), uuid(), uuid(), uuid(), uuid(), uuid(), uuid()
	var workKind string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		queries := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title,fields) SELECT $1,$2,'RT-1',id,'Routine project','{"project_key":"RT"}' FROM node_kinds WHERE slug='project'`, []any{f.person.TenantID, project}},
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,'RT-2',id,'Routine parent',$3 FROM node_kinds WHERE slug='work'`, []any{f.person.TenantID, parent, project}},
			{`INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by) VALUES($1,$2,$3,'{}',$3)`, []any{f.person.TenantID, project, f.person.ID}},
			{`SELECT aeon_seed_work_kinds($1)`, []any{f.person.TenantID}},
			{`INSERT INTO principals(tenant_id,id,kind,name,agent_access_configured) VALUES($1,$2,'agent','Routine service',true)`, []any{f.person.TenantID, service}},
			{`INSERT INTO roles(tenant_id,id,key,name) VALUES($1,$2,'routine_test','Routine service')`, []any{f.person.TenantID, role}},
			{`INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','nodes.write','run.create','work_orders.write'])`, []any{f.person.TenantID, role}},
			{`INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, []any{f.person.TenantID, service, role, project}},
		}
		for _, query := range queries {
			if _, err := tx.Exec(t.Context(), query.sql, query.args...); err != nil {
				return err
			}
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL`).Scan(&workKind)
	})
	runtime := modelregistry.ExecutionRuntime{ServerDigest: strings.Repeat("a", 64), DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), HostMappingDigest: strings.Repeat("d", 64), Capabilities: []string{"routine_native_coding_v1"}, BudgetModes: []string{"off"}}
	reader := func(ctx context.Context, tx pgx.Tx, _ string) (modelregistry.ExecutionRuntime, error) {
		out := runtime
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.ObservedAt)
		return out, err
	}
	q := modelregistry.Qualification{ID: uuid(), ProjectID: project, Runtime: runtime, CoordinatorAcceptance: strings.Repeat("e", 64), OPSAttestation: strings.Repeat("f", 64)}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var err error
		q.OwnerPersonID, q.PolicyDigest, err = modelregistry.QualificationPolicyTx(t.Context(), tx, f.person.TenantID, project)
		if err != nil {
			return err
		}
		if err = modelregistry.RecordQualificationTx(t.Context(), tx, f.person, q); err != nil {
			return err
		}
		rev, enabled := int64(0), true
		_, err = modelregistry.WriteExecutionSettingsTx(t.Context(), tx, f.person, project, modelregistry.ExecutionSettingsInput{ExpectedRevision: &rev, AutomaticLaunchEnabled: &enabled, QualificationID: &q.ID}, reader)
		return err
	})
	f.mux = http.NewServeMux()
	workorders.New(f.d.App).Mount(f.mux)
	recurrences.New(f.d.App).WithExecutionRuntime(reader).Mount(f.mux)
	var admitted atomic.Bool
	admitted.Store(true)
	checks := agentruns.RoutineLaunchChecks{Runtime: reader, Admission: func(_ context.Context, _ pgx.Tx, _ tenant.Principal, g agentsetup.RoutineGrant) error {
		if !admitted.Load() || g.BudgetGrantID != grantID {
			return workorders.Fail(409, "routine_admission_wait")
		}
		return nil
	}}
	usage := func(ctx context.Context, tx pgx.Tx, p tenant.Principal, run agentruns.Run, _ agentruns.Telemetry, pending *[]events.Change) error {
		return agentaccounts.SettleDeferred(ctx, tx, p, run.ID, pending)
	}
	m := agentruns.New(f.d.App, usage)
	if err := agentruns.BindRoutineLaunch(m, checks); err != nil {
		t.Fatal(err)
	}
	m.Mount(f.mux)
	in := recurrences.Input{ProjectID: project, ParentID: parent, Template: recurrences.Template{Title: "Routine {{occurrence}}", Description: "Synthetic launch boundary", Criteria: []string{"Verify ownership"}, EstimateHours: 1, Priority: "high", Type: "ticket"}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "UTC"}, OverlapPolicy: "create", CatchUpPolicy: "one", Definition: &recurrences.Definition{Scope: recurrences.DefinitionScope{Kind: "project", ProjectID: project}, OwnerPrincipalID: f.person.ID, Assignment: &recurrences.Assignment{Goal: "Synthetic own-workspace task", Sources: []recurrences.SourceReference{}, Role: "build", WorkKindID: workKind, AllowedActions: []string{"work.update"}, RuntimeRequirements: recurrences.RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: recurrences.DefinitionBudget{Mode: "off"}}}}
	var recurrence recurrences.Recurrence
	f.call(t, f.person, "POST", "/api/recurrences", in, 201, &recurrence)
	f.call(t, f.person, "POST", "/api/recurrences/"+recurrence.ID+"/resume", map[string]any{"expected_revision": recurrence.Revision}, 200, &recurrence)
	f.call(t, f.person, "PUT", "/api/recurrences/"+recurrence.ID+"/execution-consent", map[string]any{"expected_revision": recurrence.Revision, "execute_consent": true, "execution_principal_id": service}, 200, &recurrence)
	var occurrence recurrences.Occurrence
	f.call(t, f.person, "POST", "/api/recurrences/"+recurrence.ID+"/run-now", map[string]string{"idempotency_key": "launch"}, 200, &occurrence)
	if occurrence.Run == nil || occurrence.NodeID == nil {
		t.Fatal("fixture lacks durable routine intent")
	}
	o := f.order(t, nil)
	run := f.run(t, o)
	ids := f.reserve(t, run)
	var account string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT account_id::text FROM agent_runs WHERE id=$1`, run.ID).Scan(&account); err != nil {
			return err
		}
		queries := []struct {
			sql  string
			args []any
		}{
			{`UPDATE nodes SET parent_id=$2 WHERE id=$1`, []any{o.NodeID, *occurrence.NodeID}},
			{`UPDATE routine_runs SET work_order_id=$2,agent_run_id=$3 WHERE id=$1`, []any{occurrence.Run.ID, o.NodeID, run.ID}},
			{`INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,$2,'123456789',$3,$3,$3,'{"platform":"darwin","arch":"arm64"}',$3,'redeemed',$4)`, []any{f.person.TenantID, request, strings.Repeat("e", 64), f.person.ID}},
			{`INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,ledger_generation) SELECT $1,$2,$3,$4,id,'daemon-test',$5,$6 FROM agent_keys WHERE principal_id=$4 LIMIT 1`, []any{f.person.TenantID, computer, request, f.agent.ID, strings.Repeat("e", 64), ledger}},
			{`UPDATE agent_pairing_requests SET computer_id=$2 WHERE id=$1`, []any{request, computer}},
			{`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,ongoing_approved_at,verification_expires_at) VALUES($1,$2,$3,$4,$5,clock_timestamp(),clock_timestamp()+interval '1 hour')`, []any{f.person.TenantID, account, computer, request, f.profile}},
		}
		for _, query := range queries {
			if _, err := tx.Exec(t.Context(), query.sql, query.args...); err != nil {
				return err
			}
		}
		return nil
	})
	g := agentsetup.RoutineGrant{LedgerGeneration: ledger, LaunchID: uuid(), TenantID: f.person.TenantID, OwnerID: f.person.ID, ProjectID: project, RoutineRunID: occurrence.Run.ID, AgentRunID: run.ID, PrincipalID: f.agent.ID, ExecutionPrincipalID: service, AccountID: account, ComputerID: computer, DaemonID: "daemon-test", DaemonGeneration: "generation-1", AssignmentDigest: occurrence.Run.AssignmentDigest, DefinitionRevision: recurrence.Revision, ConsentRevision: recurrence.Revision, ExecutionRevision: 1, QualificationID: q.ID, DaemonDigest: runtime.DaemonDigest, CapabilityDigest: runtime.CapabilityDigest, HostMappingDigest: runtime.HostMappingDigest, Capability: agentsetup.RoutineExecutionCapability, RuntimeCapability: "routine_native_coding_v1", BudgetMode: "off", BudgetGrantID: grantID, ExpiresAt: time.Now().Add(90 * time.Second)}
	// A qualified coding grant must not masquerade as a browser assignment.
	// Browser routing additionally requires trusted exact native launcher facts
	// and current unattended/enrollment signals inside the final transaction.
	f.tx(t, f.person, func(tx pgx.Tx) error {
		browser := g
		browser.RuntimeCapability = agentsetup.RoutineBrowserCapability
		if err := agentruns.PrepareRoutineLaunchTx(t.Context(), tx, f.person, browser, checks); err == nil || !strings.Contains(err.Error(), "routine_assignment_changed") {
			t.Fatalf("coding/browser requirement binding: %v", err)
		}
		if err := workqueue.RequireNativeBrowserRouteTx(t.Context(), tx, browser, nil); err == nil || !strings.Contains(err.Error(), "native_browser_launcher_unavailable") {
			t.Fatalf("missing native launcher: %v", err)
		}
		// Savepoint changes are synthetic qualification, never persisted into
		// the fixture's ordinary coding route or its model registry pins.
		local, err := tx.Begin(t.Context())
		if err != nil {
			return err
		}
		defer local.Rollback(t.Context())
		if _, err = local.Exec(t.Context(), `UPDATE agent_accounts SET harness='claude' WHERE id=$1`, g.AccountID); err != nil {
			return err
		}
		if _, err = local.Exec(t.Context(), `UPDATE model_profiles SET harness='claude' WHERE id=$1`, f.profile); err != nil {
			return err
		}
		if _, err = local.Exec(t.Context(), `UPDATE agent_pairing_computers SET capacity_signals='{"unattended":{"login_session":"ready","idle_sleep":"disabled","filevault":"off"}}',capacity_reported_at=clock_timestamp() WHERE id=$1`, g.ComputerID); err != nil {
			return err
		}
		read := func(_ context.Context, _ pgx.Tx, grant agentsetup.RoutineGrant) (agentsetup.NativeBrowserRuntime, error) {
			return agentsetup.NativeBrowserRuntime{TenantID: grant.TenantID, OwnerID: grant.OwnerID, ProjectID: grant.ProjectID, AccountID: grant.AccountID, ComputerID: grant.ComputerID,
				DaemonID: grant.DaemonID, DaemonGeneration: grant.DaemonGeneration, DaemonDigest: grant.DaemonDigest, CapabilityDigest: grant.CapabilityDigest, HostMappingDigest: grant.HostMappingDigest,
				ChromeDigest: strings.Repeat("1", 64), InterpreterDigest: strings.Repeat("2", 64), LauncherDigest: strings.Repeat("3", 64), Platform: "darwin", Adapter: "claude",
				Capabilities: []string{agentsetup.RoutineExecutionCapability, agentsetup.RoutineBrowserCapability}, ReportedAt: grant.ExpiresAt.Add(-90 * time.Second),
				OwnerApproved: true, LauncherReady: true, Qualified: true, Native: true, ReadOnlyNetwork: true, EphemeralProfile: true}, nil
		}
		if err := workqueue.RequireNativeBrowserRouteTx(t.Context(), local, browser, read); err != nil {
			return err
		}
		if _, err = local.Exec(t.Context(), `UPDATE agent_pairing_computers SET capacity_reported_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, g.ComputerID); err != nil {
			return err
		}
		if err := workqueue.RequireNativeBrowserRouteTx(t.Context(), local, browser, read); err == nil || !strings.Contains(err.Error(), "unattended_host_unreachable") {
			t.Fatalf("stale native host was available: %v", err)
		}
		return nil
	})
	f.tx(t, f.person, func(tx pgx.Tx) error { return agentruns.PrepareRoutineLaunchTx(t.Context(), tx, f.person, g, checks) })
	pickup := agentsetup.RoutinePickup{LaunchID: g.LaunchID, WorktreeID: strings.Repeat("7", 64), Capability: g.Capability, DaemonDigest: g.DaemonDigest, CapabilityDigest: g.CapabilityDigest, ComputerID: g.ComputerID}
	claim := func(ctx context.Context, mux *http.ServeMux, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/runs/"+run.ID+"/claim", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, f.agent))
		r.Header.Set(agentpairing.LedgerGenerationHeader, ledger)
		r.Header.Set("Authorization", "Bearer "+f.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	body := claimBody(ids)
	refused := claim(t.Context(), f.mux, body)
	if refused.Code != 409 || !strings.Contains(refused.Body.String(), "routine_daemon_update_required") {
		t.Fatalf("old daemon: %d %s", refused.Code, refused.Body.String())
	}
	body["routine"] = pickup
	// The routing entry must also refuse old capability before creating or
	// changing any reservations. Test through the real account HTTP handler.
	agentaccounts.New(f.d.App).Mount(f.mux)
	routeRaw, _ := json.Marshal(map[string]any{"run_id": run.ID, "daemon_id": "daemon-test", "account_ids": []string{account}, "estimated_units": map[string]int64{"cost_micros": 100}})
	routeRequest := httptest.NewRequest("POST", "/api/agent-accounts/route", strings.NewReader(string(routeRaw))).WithContext(tenant.WithPrincipal(t.Context(), f.agent))
	routeRequest.Header.Set(agentpairing.LedgerGenerationHeader, ledger)
	routeRequest.Header.Set("Authorization", "Bearer "+f.token)
	routeResult := httptest.NewRecorder()
	f.mux.ServeHTTP(routeResult, routeRequest)
	if routeResult.Code != 409 || !strings.Contains(routeResult.Body.String(), "routine_daemon_update_required") {
		t.Fatalf("old route capability: %d %s", routeResult.Code, routeResult.Body.String())
	}
	for _, mutate := range []func(*agentsetup.RoutinePickup){func(p *agentsetup.RoutinePickup) { p.Capability = "old" }, func(p *agentsetup.RoutinePickup) { p.ComputerID = uuid() }, func(p *agentsetup.RoutinePickup) { p.DaemonDigest = strings.Repeat("0", 64) }} {
		changed := pickup
		mutate(&changed)
		body["routine"] = changed
		result := claim(t.Context(), f.mux, body)
		if result.Code != 409 || !strings.Contains(result.Body.String(), "routine_daemon_update_required") {
			t.Fatal("unqualified pickup accepted", result.Body.String())
		}
	}
	body["routine"] = pickup
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrence_definitions SET assignment=jsonb_set(assignment,'{goal}','"changed"') WHERE recurrence_id=$1`, recurrence.ID)
		return err
	})
	if result := claim(t.Context(), f.mux, body); result.Code != 409 || !strings.Contains(result.Body.String(), "routine_assignment_changed") {
		t.Fatal("changed assignment accepted", result.Body.String())
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrence_definitions SET assignment=(SELECT assignment FROM routine_runs WHERE id=$2) WHERE recurrence_id=$1`, recurrence.ID, g.RoutineRunID)
		return err
	})
	admitted.Store(false)
	refused = claim(t.Context(), f.mux, body)
	admitted.Store(true)
	if refused.Code != 409 || !strings.Contains(refused.Body.String(), "routine_admission_wait") {
		t.Fatalf("unknown admission: %d %s", refused.Code, refused.Body.String())
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "INSERT INTO routine_launch_evidence") })
	raceMux := http.NewServeMux()
	raceModule := agentruns.New(pool)
	if err := agentruns.BindRoutineLaunch(raceModule, checks); err != nil {
		t.Fatal(err)
	}
	raceModule.Mount(raceMux)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- claim(ctx, raceMux, body) }()
	holder := barrier.Wait(t, ctx)
	second := make(chan *httptest.ResponseRecorder, 1)
	done := make(chan struct{})
	go func() { second <- claim(ctx, f.mux, body); close(done) }()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, holder, done); lock == "" {
		t.Fatal("claims did not overlap under tenant fence")
	}
	barrier.Release()
	for _, result := range []*httptest.ResponseRecorder{dbtest.Await(t, ctx, first), dbtest.Await(t, ctx, second)} {
		if result.Code != 200 {
			t.Fatalf("qualified claim: %d %s", result.Code, result.Body.String())
		}
	}
	if f.count(t, f.person, `SELECT count(*) FROM routine_launch_evidence WHERE kind='claimed'`) != 1 {
		t.Fatal("claim replay appended another receipt")
	}
	changed := pickup
	changed.WorktreeID = strings.Repeat("8", 64)
	body["routine"] = changed
	if result := claim(t.Context(), f.mux, body); result.Code != 409 || !strings.Contains(result.Body.String(), "routine_writer_conflict") {
		t.Fatal("replay replaced writer", result.Body.String())
	}
	body["routine"] = pickup
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE recurrence_definitions SET execute_consent=false WHERE recurrence_id=$1`, recurrence.ID)
		return err
	})
	if result := claim(t.Context(), f.mux, body); result.Code != 409 || !strings.Contains(result.Body.String(), "routine_assignment_changed") {
		t.Fatal("revoked assignment claimed", result.Body.String())
	}
	if f.count(t, f.person, `SELECT count(*) FROM routine_launch_intents WHERE state='claimed' AND exit_observed_at IS NULL`) != 1 {
		t.Fatal("revocation erased an unknown writer")
	}
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/telemetry", map[string]any{"sequence": 1, "kind": "finished", "status": "ownership_lost", "process_state": "unconfirmed"}, 200, nil)
	if f.count(t, f.person, `SELECT count(*) FROM routine_launch_intents WHERE state='claimed' AND exit_observed_at IS NULL`) != 1 || f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run.ID) != 1 {
		t.Fatal("unknown exit released writer/account hold")
	}
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		err := agentaccounts.Release(t.Context(), tx, f.agent, run.ID, "daemon-test", "generation-1")
		if err == nil || !strings.Contains(err.Error(), "routine exit is unconfirmed") {
			t.Fatalf("unknown exit release: %v", err)
		}
		return nil
	})
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/telemetry", map[string]any{"sequence": 2, "kind": "finished", "status": "failed", "process_state": "exited"}, 200, nil)
	if f.count(t, f.person, `SELECT count(*) FROM routine_launch_evidence WHERE kind='exit_observed'`) != 1 || f.count(t, f.person, `SELECT count(*) FROM routine_launch_intents WHERE state='exit_observed' AND exit_observed_at IS NOT NULL`) != 1 {
		t.Fatal("exact observed exit not retained")
	}
	if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='settled'`, run.ID) != 1 {
		t.Fatal("observed exit failed to settle existing allowance reservation")
	}
	// Tenant RLS preserves intent/evidence ownership even with system visibility.
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_launch_intents`).Scan(&n)
		if n != 0 {
			return errors.New("foreign launch intent leaked")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type fixedAuthority struct{ authorization Authorization }

func (a fixedAuthority) AuthorizeTx(context.Context, pgx.Tx, nativecontract.Actor, string, nativecontract.StartIntent) (Authorization, error) {
	return a.authorization, nil
}

type observedStart struct {
	value            Observation
	entered, release chan struct{}
}

func (o *observedStart) Observe(ctx context.Context, _ nativecontract.DaemonActor, p PendingStart) (Observation, error) {
	if o.entered != nil {
		close(o.entered)
		select {
		case <-o.release:
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		}
	}
	v := o.value
	v.OperationID = p.OperationID
	return v, nil
}

type startFixture struct {
	d             *dbtest.DB
	s             *Store
	person, agent tenant.Principal
	actor         nativecontract.Actor
	daemon        nativecontract.DaemonActor
	intent        nativecontract.StartIntent
	observer      *observedStart
	now           time.Time
	project       string
}

func newStartFixture(t *testing.T) *startFixture {
	t.Helper()
	f := &startFixture{d: dbtest.Open(t), now: time.Now().UTC().Truncate(time.Millisecond)}
	uid := func() string {
		id, err := uuid()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.person = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	f.agent = tenant.Principal{ID: uid(), TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.read", "harness.read", "harness.worker", "harness.control", "run.create"}, KeyCreatorID: f.person.ID}
	f.project = uid()
	computer, account, request, ledger := uid(), uid(), uid(), uid()
	profile := uid()
	grantID, policyID := uid(), uid()
	seed := dbtest.Seed(t.Context())
	err := db.InTenant(seed, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Native')`, []any{f.person.TenantID, "native-" + f.person.TenantID}},
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','owner'),($1,$3,'agent','paired')`, []any{f.person.TenantID, f.person.ID, f.agent.ID}},
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'NS-1',id,'Native' FROM node_kinds WHERE slug='project'`, []any{f.person.TenantID, f.project}},
			{`INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'native-test','1','claude','anthropic','claude-opus-4-6','high','strong')`, []any{f.person.TenantID, profile}},
		} {
			if _, e := tx.Exec(seed, q.sql, q.args...); e != nil {
				return e
			}
		}
		if e := tx.QueryRow(seed, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'native fixture','native_fixture',$3,$4,$5) RETURNING id::text`, f.person.TenantID, f.agent.ID, strings.Repeat("a", 64), f.agent.Scopes, f.person.ID).Scan(&f.agent.KeyID); e != nil {
			return e
		}
		policy, _ := json.Marshal(hostcapacity.Default())
		load := 1.0
		signals, _ := json.Marshal(hostcapacity.Signals{Cores: 8, Load: &load, MemoryPressure: "normal", Power: "plugged_in", Thermal: "normal"})
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by)
 VALUES($1,$2,'123456789',$3,$4,$5,'{"platform":"darwin","arch":"arm64"}','fixture','redeemed',$6)`, []any{f.person.TenantID, request, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), f.person.ID}},
			{`INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,ledger_generation,capacity_policy,capacity_signals,capacity_reported_at)
 VALUES($1,$2,$3,$4,$5,'daemon-native',$6,$7,$8,$9,$10)`, []any{f.person.TenantID, computer, request, f.agent.ID, f.agent.KeyID, strings.Repeat("c", 64), ledger, policy, signals, f.now}},
			{`INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,owner_person_id,linked_at,link_revision,allowed_model_profile_ids,last_probe_at,last_probe_ok,last_daemon_generation)
 VALUES($1,$2,'own-account','claude','daemon-native',$3,'Own',$4,$6,1,ARRAY[$5::uuid],$6,true,'generation-1')`, []any{f.person.TenantID, account, f.agent.ID, f.person.ID, profile, f.now}},
			{`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,ongoing_approved_at,verification_expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$6+interval '1 hour')`, []any{f.person.TenantID, account, computer, request, profile, f.now}},
		} {
			if _, e := tx.Exec(seed, q.sql, q.args...); e != nil {
				return e
			}
		}
		schedule := capacity.DefaultSchedule("UTC")
		schedule.Reserve = capacity.ReserveOff
		schedule.Nights = true
		for i := range schedule.Week {
			schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
		}
		scheduleRaw, _ := json.Marshal(schedule)
		if _, e := tx.Exec(seed, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3,$3::uuid,$4)`, f.person.TenantID, f.person.ID, account, scheduleRaw); e != nil {
			return e
		}
		return tx.QueryRow(seed, `SELECT revision FROM nodes WHERE id=$1`, f.project).Scan(&f.intent.ExpectedProjectRevision)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "admin")
	dbtest.BindRole(t, f.d, f.person.TenantID, f.agent.ID, "member")
	f.actor = nativecontract.Actor{TenantID: f.person.TenantID, PrincipalID: f.person.ID, PrincipalKind: "person", OwnerPersonID: f.person.ID}
	f.intent = nativecontract.StartIntent{RequestID: uid(), ExpectedProjectRevision: f.intent.ExpectedProjectRevision, Role: "coordinator", Profile: "native", Harness: "claude", ComputerID: computer, AccountID: account, WorkspaceID: "approved-workspace", ModelProfileID: profile, PolicyID: policyID}
	f.daemon = nativecontract.DaemonActor{Actor: nativecontract.Actor{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, PrincipalKind: "agent", OwnerPersonID: f.person.ID, KeyCeiling: f.agent.Scopes}, ComputerID: computer, DaemonID: "daemon-native", DaemonGeneration: "generation-1", LedgerGeneration: ledger, CapabilityDigest: strings.Repeat("e", 64)}
	viewer := "https://computer.ts.net/lead"
	ref, lease := sha256.Sum256([]byte("local native session proof")), sha256.Sum256([]byte("local worker lease proof"))
	f.observer = &observedStart{value: Observation{PrincipalID: f.agent.ID, ComputerID: computer, DaemonID: f.daemon.DaemonID, DaemonGeneration: f.daemon.DaemonGeneration, LedgerGeneration: ledger, CapabilityDigest: f.daemon.CapabilityDigest, AccountID: account, WorkspaceID: f.intent.WorkspaceID, LedgerReservationID: uid(), LedgerClear: true, SessionRefDigest: ref[:], WorkerLeaseDigest: lease[:], ObservedAt: f.now, ExpiresAt: f.now.Add(20 * time.Second), TerminalViewerURL: &viewer}}
	policy := nativecontract.Policy{GrantID: grantID, GrantRevision: 1, ExpiresAt: f.now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64), PermissionPolicy: "supervised", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 10000}
	launch := nativecontract.LaunchConfig{ModelProfileID: profile, Model: "claude-opus-4-6", ServiceTier: "default", ToolPolicyDigest: policy.ToolPolicyDigest, SettingsPolicyDigest: policy.SettingsPolicyDigest}
	f.s, err = New(f.d.App, Options{Authority: fixedAuthority{Authorization{ProjectID: f.project, OwnerPersonID: f.person.ID, PersonInvoked: true, Policy: policy, Launch: launch, Capabilities: nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("e", 64)}}}, Observer: f.observer, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *startFixture) personCtx(t *testing.T) context.Context {
	return WithProject(tenant.WithPrincipal(t.Context(), f.person), f.project)
}
func (f *startFixture) daemonCtx(t *testing.T) context.Context {
	return tenant.WithPrincipal(t.Context(), f.agent)
}
func (f *startFixture) begin(t *testing.T) nativecontract.OperationResult {
	t.Helper()
	result, err := f.s.Start(f.personCtx(t), f.actor, f.intent)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *startFixture) claim(result nativecontract.OperationResult) nativecontract.Claim {
	return nativecontract.Claim{RequestID: "20000000-0000-4000-8000-000000000001", OperationID: result.Receipt.OperationID, DaemonID: f.daemon.DaemonID, DaemonGeneration: f.daemon.DaemonGeneration, LedgerGeneration: f.daemon.LedgerGeneration, ExpectedRevision: result.Session.Revision, CapabilityDigest: f.daemon.CapabilityDigest, ClaimKind: "start"}
}
func (f *startFixture) seed(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

// Risk: an old CLI/person registration or queued grant remains authority after
// revocation. The local-read barrier proves revocation completes BEFORE claim.
func TestStartClaimRevocationBarrier(t *testing.T) {
	f := newStartFixture(t)
	accepted := f.begin(t)
	retry, err := f.s.Start(f.personCtx(t), f.actor, f.intent)
	if err != nil || retry.Receipt.OperationID != accepted.Receipt.OperationID || retry.Session.OwnedSessionID != accepted.Session.OwnedSessionID {
		t.Fatalf("idempotent start: %v", err)
	}
	f.observer.entered = make(chan struct{})
	f.observer.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, e := f.s.Claim(f.daemonCtx(t), f.daemon, f.claim(accepted)); done <- e }()
	guard, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-f.observer.entered:
	case e := <-done:
		t.Fatalf("claim refused before barrier: %v", e)
	case <-guard.Done():
		t.Fatal("claim did not reach local read barrier")
	}
	baseline := 0
	f.seed(t, func(tx pgx.Tx) error {
		ctx := dbtest.Seed(t.Context())
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type LIKE 'native_session.%'`).Scan(&baseline); err != nil {
			return err
		}
		// Preserve the paired daemon's worker/read/control creator ceiling.
		// Revoke ONLY the original actor's start authority, so removing its
		// final RequireTx check really makes this test fail.
		var role string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'native_no_start','Native without start') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','harness.read','harness.worker','harness.control'])`, f.person.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.person.ID, role)
		return err
	})
	close(f.observer.release)
	select {
	case err = <-done:
	case <-guard.Done():
		t.Fatal("claim did not finish after revocation")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonPermissionRevoked {
		t.Fatalf("wrong refusal after start revocation: %v", err)
	}
	f.observer.entered = nil
	if _, err = f.s.Claim(f.daemonCtx(t), f.daemon, f.claim(accepted)); !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonPermissionRevoked {
		t.Fatalf("retry regained authority: %v", err)
	}
	f.seed(t, func(tx pgx.Tx) error {
		var intents, registrations, roots, events, claimed int
		err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM native_session_intents),(SELECT count(*) FROM harness_sessions),(SELECT count(*) FROM native_session_roots),(SELECT count(*) FROM events WHERE type LIKE 'native_session.%'),(SELECT count(*) FROM native_session_intents WHERE claim_request_id IS NOT NULL)`).Scan(&intents, &registrations, &roots, &events, &claimed)
		if err != nil {
			return err
		}
		if intents != 1 || registrations != 0 || roots != 0 || claimed != 0 || events != baseline {
			t.Fatalf("revoked claim wrote intent=%d registration=%d root=%d claimed=%d event=%d baseline=%d", intents, registrations, roots, claimed, events, baseline)
		}
		return nil
	})
}

func TestStartRegistrationIdempotencyAndPersonViewer(t *testing.T) {
	f := newStartFixture(t)
	accepted := f.begin(t)
	claim := f.claim(accepted)
	registered, err := f.s.Claim(f.daemonCtx(t), f.daemon, claim)
	if err != nil {
		t.Fatal(err)
	}
	if !registered.LaunchAuthorized || registered.RootReservationID == nil || registered.ExpectedBinding == nil || registered.ExpectedBinding.RunID != nil {
		t.Fatal("root claim did not durably bind a real root")
	}
	f.now = f.now.Add(time.Second)
	retry, err := f.s.Claim(f.daemonCtx(t), f.daemon, claim)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ExpectedBinding.HarnessSessionID != registered.ExpectedBinding.HarnessSessionID || retry.ExpectedBinding.RuntimeEpoch != registered.ExpectedBinding.RuntimeEpoch || retry.RootReservationID == nil || *retry.RootReservationID != *registered.RootReservationID || !retry.ExpiresAt.Equal(registered.ExpiresAt) || retry.AuthorizationTTLMS >= registered.AuthorizationTTLMS {
		t.Fatal("retry replaced binding/hold or renewed launch authority")
	}
	person, err := f.s.Read(f.personCtx(t), f.actor, *registered.ExpectedBinding)
	if err != nil {
		t.Fatal(err)
	}
	if person.TerminalViewerURL == nil || *person.TerminalViewerURL != *f.observer.value.TerminalViewerURL {
		t.Fatal("authorized person lost terminal URL")
	}
	agent, err := f.s.Read(f.daemonCtx(t), f.daemon.Actor, *registered.ExpectedBinding)
	if err != nil {
		t.Fatal(err)
	}
	if agent.TerminalViewerURL != nil {
		t.Fatal("terminal URL leaked to agent")
	}
	changed := claim
	changed.DaemonGeneration = "generation-2"
	var failure *Failure
	if _, err = f.s.Claim(f.daemonCtx(t), f.daemon, changed); !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonWrongGeneration {
		t.Fatalf("wrong generation did not refuse for generation: %v", err)
	}
	f.seed(t, func(tx pgx.Tx) error {
		var registeredBy string
		var roots, intents, harnesses, runs int
		if err := tx.QueryRow(t.Context(), `SELECT agent_principal_id::text FROM harness_sessions WHERE id=$1`, registered.ExpectedBinding.HarnessSessionID).Scan(&registeredBy); err != nil {
			return err
		}
		if registeredBy != f.agent.ID {
			t.Fatal("registered against old person principal")
		}
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM native_session_roots),(SELECT count(*) FROM native_session_intents),(SELECT count(*) FROM harness_sessions),(SELECT count(*) FROM agent_runs)`).Scan(&roots, &intents, &harnesses, &runs); err != nil {
			return err
		}
		if roots != 1 || intents != 1 || harnesses != 1 || runs != 0 {
			t.Fatalf("duplicate/fake rows: %d/%d/%d/%d", roots, intents, harnesses, runs)
		}
		return nil
	})
}

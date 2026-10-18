// SPDX-License-Identifier: AGPL-3.0-only

package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeinteraction"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type testAuthority struct{ revoked, protected atomic.Bool }

func (a *testAuthority) AuthorizeRequestTx(context.Context, pgx.Tx, tenant.Principal, nativecontract.Approval) (bool, error) {
	if a.revoked.Load() {
		return false, fault(403, nativecontract.ReasonPermissionRevoked)
	}
	return a.protected.Load(), nil
}

func (a *testAuthority) AuthorizeTx(context.Context, pgx.Tx, tenant.Principal, nativecontract.Binding, string) error {
	if a.revoked.Load() {
		return fault(403, nativecontract.ReasonPermissionRevoked)
	}
	return nil
}

type fixture struct {
	d                    *dbtest.DB
	s                    *Store
	now                  time.Time
	person, other, agent tenant.Principal
	binding              nativecontract.Binding
	authority            *testAuthority
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	uid := func() string {
		id, err := uuid()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Match clock_timestamp()/timestamptz precision so a SQL round trip cannot
	// shave a millisecond off an unchanged lease. The sample includes submicrosecond
	// precision on every host, including Macs whose wall clock is already coarser.
	now := time.Now().UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
	f := &fixture{d: dbtest.Open(t), now: now.Truncate(time.Microsecond), authority: &testAuthority{}}
	tenantID := uid()
	f.person = tenant.Principal{ID: uid(), TenantID: tenantID, Kind: tenant.Person, BrowserSession: true}
	f.other = tenant.Principal{ID: uid(), TenantID: tenantID, Kind: tenant.Person, BrowserSession: true}
	f.agent = tenant.Principal{ID: uid(), TenantID: tenantID, Kind: tenant.Agent, Scopes: []string{"harness.read", "harness.worker", "questions.ask", "questions.read"}}
	f.binding = nativecontract.Binding{TenantID: tenantID, ProjectID: uid(), OwnedSessionID: uid(), HarnessSessionID: uid(), RuntimeEpoch: uid(), DaemonID: "fixture-daemon", DaemonGeneration: "generation-1", BindingRevision: 2}
	view := nativecontract.SessionView{OwnedSessionID: f.binding.OwnedSessionID, CurrentBinding: &f.binding, OwnerPersonID: f.person.ID, Role: "coordinator", Profile: "native", Harness: "claude", State: "running", Mode: "structured", Revision: 2, Generation: 1, ConversationRef: f.binding.OwnedSessionID, Capabilities: nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64), Approvals: true, Questions: true}}
	view.Capabilities.Take = true
	view.Capabilities.TakeSupported = true
	used := 10.0
	view.Quota = &nativecontract.Quota{AccountID: uid(), ProfileBindingRevision: 1, Source: "claude_stream", Scope: "account", State: "known", UsedPercent: &used, ObservedAt: f.now, FreshUntil: f.now.Add(time.Minute)}
	policy := nativecontract.Policy{GrantID: uid(), GrantRevision: 1, ExpiresAt: f.now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64), PermissionPolicy: "supervised", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 10000}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, tenantID, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Native interactions')`, []any{tenantID, "interaction-" + tenantID}},
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','owner'),($1,$3,'person','other'),($1,$4,'agent','paired')`, []any{tenantID, f.person.ID, f.other.ID, f.agent.ID}},
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'NI-1',id,'Native interaction project' FROM node_kinds WHERE slug='project'`, []any{tenantID, f.binding.ProjectID}},
			{`INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,owner_principal_id) VALUES($1,$2,$3,$4,'claude','fixture','managed','coordinator',$5,$6,$7)`, []any{tenantID, f.binding.HarnessSessionID, f.binding.ProjectID, f.agent.ID, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), f.person.ID}},
			{`INSERT INTO native_session_bindings(tenant_id,id,project_id,owner_person_id,actor_principal_id,paired_principal_id,computer_id,current_harness_session_id,runtime_epoch,daemon_id,daemon_generation,ledger_generation,binding_revision,view) VALUES($1,$2,$3,$4,$4,$5,$6,$7,$8,$9,$10,'ledger-1',2,$11)`, []any{tenantID, f.binding.OwnedSessionID, f.binding.ProjectID, f.person.ID, f.agent.ID, uid(), f.binding.HarnessSessionID, f.binding.RuntimeEpoch, f.binding.DaemonID, f.binding.DaemonGeneration, mustJSON(view)}},
			{`INSERT INTO native_session_intents(tenant_id,id,request_id,owned_session_id,policy) VALUES($1,$2,$3,$4,$5)`, []any{tenantID, uid(), uid(), f.binding.OwnedSessionID, mustJSON(policy)}},
		} {
			if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return tx.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','native_fixture',$3,$4,$5) RETURNING id::text`, tenantID, f.agent.ID, strings.Repeat("c", 64), f.agent.Scopes, f.person.ID).Scan(&f.agent.KeyID)
	})
	if err != nil {
		t.Fatal(err)
	}
	f.agent.AuthKeyID = f.agent.KeyID
	f.agent.KeyCreatorID = f.person.ID
	for _, p := range []tenant.Principal{f.person, f.other, f.agent} {
		dbtest.BindRole(t, f.d, tenantID, p.ID, "admin")
	}
	f.s, err = New(f.d.App, Options{Authority: f.authority, Clock: func(context.Context, pgx.Tx) (time.Time, error) { return f.now, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
func actor(p tenant.Principal) nativecontract.Actor {
	return nativecontract.Actor{TenantID: p.TenantID, PrincipalID: p.ID, PrincipalKind: string(p.Kind), KeyCeiling: p.Scopes}
}
func (f *fixture) ctx(t *testing.T, p tenant.Principal) context.Context {
	return tenant.WithPrincipal(t.Context(), p)
}
func (f *fixture) attach(mode string) nativecontract.AttachIntent {
	r, _ := uuid()
	c, _ := uuid()
	return nativecontract.AttachIntent{RequestID: r, ClientID: c, ExpectedBinding: f.binding, Mode: mode, View: "structured"}
}
func expectFailure(t *testing.T, err error, status int, reason nativecontract.Reason) {
	t.Helper()
	var e *nativeinteraction.Failure
	if !errors.As(err, &e) || e.Status != status || e.Reason != reason {
		t.Fatalf("want %d/%s, got %v", status, reason, err)
	}
}

// Risk: overlapping tabs or an expired epoch could authorize a second
// controller. Observe the actual competing DB lock; deadlines only bound hangs.
func TestControllerEpochFencesConcurrentAttach(t *testing.T) {
	f := newFixture(t)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool { return strings.Contains(q, "FROM native_session_controllers WHERE") })
	f.s.pool = pool
	t.Cleanup(barrier.Release)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	first, second := f.attach("control"), f.attach("control")
	type result struct {
		out nativecontract.AttachResult
		err error
	}
	one, two := make(chan result, 1), make(chan result, 1)
	go func() {
		out, err := f.s.Attach(tenant.WithPrincipal(ctx, f.person), actor(f.person), first)
		one <- result{out, err}
	}()
	pid := barrier.Wait(t, ctx)
	done := make(chan struct{})
	go func() {
		out, err := f.s.Attach(f.ctx(t, f.other), actor(f.other), second)
		two <- result{out, err}
		close(done)
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, pid, done); lock == "" {
		t.Fatal("concurrent attach did not reach the held fence")
	}
	barrier.Release()
	a, b := dbtest.Await(t, ctx, one), dbtest.Await(t, ctx, two)
	if a.err != nil || a.out.Controller == nil || a.out.ControlCapability == nil {
		t.Fatalf("control attach failed: %v", a.err)
	}
	if a.out.Snapshot.Capabilities.Take || a.out.Snapshot.Capabilities.TakeSupported || a.out.Snapshot.Quota == nil {
		t.Fatal("owner public projection lost quota or advertised unqualified web take")
	}
	expectFailure(t, b.err, 409, nativecontract.ReasonControllerConflict)
	view, err := f.s.Attach(f.ctx(t, f.other), actor(f.other), f.attach("view"))
	if err != nil || view.ControlCapability != nil || view.Controller == nil || view.Controller.Epoch != a.out.Controller.Epoch {
		t.Fatalf("read-only attach changed control: %v", err)
	}
	if view.Snapshot.Quota != nil || view.Snapshot.Capabilities.Take || view.Snapshot.Capabilities.TakeSupported {
		t.Fatal("non-owner public projection exposed private quota or local take")
	}
	// Idempotent reconnect returns the same epoch/capability without renewing.
	f.now = f.now.Add(5 * time.Second)
	retry, err := f.s.Attach(f.ctx(t, f.person), actor(f.person), first)
	if err != nil || retry.Controller == nil || retry.ControlCapability == nil || retry.ViewerID != a.out.ViewerID || retry.Controller.Epoch != a.out.Controller.Epoch || retry.ExpiresInMS != 25000 || !retry.ExpiresAt.Equal(a.out.ExpiresAt) || !retry.Controller.ExpiresAt.Equal(a.out.Controller.ExpiresAt) || *retry.ControlCapability != *a.out.ControlCapability {
		t.Fatalf("retry implicitly acquired/renewed: %v; remaining_ms=%d; initial_expires=%s; retry_expires=%s", err, retry.ExpiresInMS, a.out.ExpiresAt.Format(time.RFC3339Nano), retry.ExpiresAt.Format(time.RFC3339Nano))
	}
	renew := nativecontract.AttachRenew{RequestID: second.RequestID, ExpectedBinding: f.binding, ViewerID: a.out.ViewerID, ControllerEpoch: &a.out.Controller.Epoch}
	_, err = f.s.Renew(f.ctx(t, f.person), actor(f.person), renew, "wrong proof")
	expectFailure(t, err, 403, nativecontract.ReasonWrongPrincipal)
	current, err := f.s.Renew(f.ctx(t, f.person), actor(f.person), renew, *a.out.ControlCapability)
	if err != nil || current.ExpiresInMS != 30000 {
		t.Fatalf("renew: %v", err)
	}
	check := func(bound nativecontract.Binding) error {
		return db.InTenant(f.ctx(t, f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
			if err := agentpairing.Lock(f.ctx(t, f.person), tx); err != nil {
				return err
			}
			return f.s.CheckControlTx(f.ctx(t, f.person), tx, f.person, bound, current.Controller.Epoch, current.ViewerID, *current.ControlCapability)
		})
	}
	if err = check(f.binding); err != nil {
		t.Fatal("final control claim: ", err)
	}
	f.authority.revoked.Store(true)
	expectFailure(t, check(f.binding), 403, nativecontract.ReasonPermissionRevoked)
	f.authority.revoked.Store(false)
	wrongBinding := f.binding
	wrongBinding.RuntimeEpoch = "20000000-0000-4000-8000-000000000001"
	expectFailure(t, check(wrongBinding), 409, nativecontract.ReasonWrongGeneration)
	f.now = f.now.Add(LeaseTTL)
	expectFailure(t, check(f.binding), 410, nativecontract.ReasonControllerExpired)
	_, err = f.s.Renew(f.ctx(t, f.person), actor(f.person), renew, *a.out.ControlCapability)
	expectFailure(t, err, 410, nativecontract.ReasonControllerExpired)
	fresh, err := f.s.Attach(f.ctx(t, f.other), actor(f.other), second)
	if err != nil || fresh.Controller.Epoch == a.out.Controller.Epoch {
		t.Fatalf("fresh epoch: %v", err)
	}
	if err = f.s.Detach(f.ctx(t, f.other), actor(f.other), nativecontract.Detach{RequestID: first.RequestID, ExpectedBinding: f.binding, ViewerID: fresh.ViewerID}); err != nil {
		t.Fatal(err)
	}
	var state string
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT view->>'state' FROM native_session_bindings WHERE id=$1`, f.binding.OwnedSessionID).Scan(&state)
	})
	if err != nil || state != "running" {
		t.Fatalf("detach affected native lifetime: %s %v", state, err)
	}
}

func (f *fixture) request(t *testing.T, kind string, stepUp bool, summary ...string) nativecontract.Approval {
	t.Helper()
	id, _ := uuid()
	req, _ := uuid()
	in := nativecontract.Approval{ApprovalID: id, RequestID: req, Binding: f.binding, TurnRef: "turn-1", NativeRequestRef: "native-" + id, Kind: kind, EffectDigest: strings.Repeat("a", 64), Summary: "Read only the authorized file", AllowedDecisions: []string{"allow_once", "deny"}, StepUpRequired: stepUp, ExpiresAt: f.now.Add(time.Minute), ExpiresInMS: 60000}
	if kind == "question" {
		in.AllowedDecisions = []string{"answer", "deny"}
		in.Summary = "Which authorized result should be used?"
	}
	if len(summary) > 0 {
		in.Summary = summary[0]
	}
	var computer string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT computer_id::text FROM native_session_bindings WHERE id=$1`, f.binding.OwnedSessionID).Scan(&computer)
	})
	if err != nil {
		t.Fatal(err)
	}
	d := nativecontract.DaemonActor{Actor: actor(f.agent), ComputerID: computer, DaemonID: f.binding.DaemonID, DaemonGeneration: f.binding.DaemonGeneration, LedgerGeneration: "ledger-1"}
	a, err := f.s.Request(f.ctx(t, f.agent), d, in)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fixture) decision(t *testing.T, a nativecontract.Approval, choice, answer string) nativecontract.ApprovalDecision {
	t.Helper()
	// Decide through the existing authenticated human handler, not by seeding
	// an allow row. A text/inbox message would never create this authority.
	var revision int64
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT revision FROM desk_questions WHERE node_id=$1`, *a.DeskItemID).Scan(&revision)
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := uuid()
	input := questions.DecisionInput{RequestID: id, ExpectedRevision: revision, OptionID: choice, Outcome: "once"}
	if choice == "answer" {
		input.Answer = answer
	}
	mux := http.NewServeMux()
	questions.New(f.d.App).Mount(mux)
	r := httptest.NewRequest("POST", "/api/questions/"+*a.DeskItemID+"/decision", bytes.NewReader(mustJSON(input))).WithContext(f.ctx(t, f.person))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("existing desk decision failed (%d): %s", w.Code, w.Body.String())
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (view->>'revision')::bigint FROM native_session_bindings WHERE id=$1`, f.binding.OwnedSessionID).Scan(&revision)
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := uuid()
	d := nativecontract.ApprovalDecision{RequestID: req, ExpectedBinding: f.binding, ApprovalID: a.ApprovalID, ExpectedRevision: revision, EffectDigest: a.EffectDigest, Decision: choice}
	if choice == "answer" {
		d.Answer = &answer
	}
	return d
}

// Risk: a human answer for another effect/runtime, a bearer agent, or a
// revoked grant could otherwise authorize native tools. Only once is accepted.
func TestDeskDecisionExactEffectExpiryAndRevocation(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "tool", false)
	id, _ := uuid()
	raw := nativecontract.ApprovalDecision{RequestID: id, ExpectedBinding: f.binding, ApprovalID: a.ApprovalID, ExpectedRevision: 3, EffectDigest: a.EffectDigest, Decision: "allow_once"}
	_, err := f.s.Decide(f.ctx(t, f.agent), actor(f.agent), raw)
	expectFailure(t, err, 403, nativecontract.ReasonWrongPrincipal)
	_, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), raw)
	expectFailure(t, err, 403, nativecontract.ReasonGrantMissing)
	d := f.decision(t, a, "allow_once", "")
	wrong := d
	wrong.EffectDigest = strings.Repeat("b", 64)
	_, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), wrong)
	expectFailure(t, err, 409, nativecontract.ReasonIdentityMismatch)
	f.authority.revoked.Store(true)
	_, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), d)
	expectFailure(t, err, 403, nativecontract.ReasonPermissionRevoked)
	f.authority.revoked.Store(false)
	result, err := f.s.Decide(f.ctx(t, f.person), actor(f.person), d)
	if err != nil || result.Replay {
		t.Fatalf("exact resolution: %v", err)
	}
	retry, err := f.s.Decide(f.ctx(t, f.person), actor(f.person), d)
	if err != nil || !retry.Replay {
		t.Fatalf("exact retry: %v", err)
	}
	var count int
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM native_session_interactions WHERE id=$1 AND resolved_at IS NOT NULL`, a.ApprovalID).Scan(&count)
	})
	if err != nil || count != 1 {
		t.Fatalf("resolution cardinality %d: %v", count, err)
	}
	f.authority.protected.Store(true)
	protected := f.request(t, "tool", false, strings.Repeat("x", 2048))
	if !protected.StepUpRequired {
		t.Fatal("producer removed the server's step-up requirement")
	}
	pd := f.decision(t, protected, "allow_once", "")
	_, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), pd)
	expectFailure(t, err, 428, nativecontract.ReasonGrantMissing)
	f.authority.protected.Store(false)
	question := f.request(t, "question", false)
	qd := f.decision(t, question, "answer", "Use the reviewed result exactly.")
	alternate := "Different answer"
	wrong = qd
	wrong.Answer = &alternate
	_, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), wrong)
	expectFailure(t, err, 409, nativecontract.ReasonIdentityMismatch)
	if _, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), qd); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	_, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), pd)
	expectFailure(t, err, 410, nativecontract.ReasonApprovalExpired)
}

// Risk: an API restart must never reconstruct capabilities or old allows.
func TestServerRestartExpiresInteractions(t *testing.T) {
	f := newFixture(t)
	a := f.request(t, "tool", false)
	d := f.decision(t, a, "allow_once", "")
	attached, err := f.s.Attach(f.ctx(t, f.person), actor(f.person), f.attach("control"))
	if err != nil {
		t.Fatal(err)
	}
	f.s, err = New(f.d.App, Options{Authority: f.authority, Clock: func(context.Context, pgx.Tx) (time.Time, error) { return f.now, nil }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Decide(f.ctx(t, f.person), actor(f.person), d)
	expectFailure(t, err, 410, nativecontract.ReasonApprovalExpired)
	_, err = f.s.Renew(f.ctx(t, f.person), actor(f.person), nativecontract.AttachRenew{RequestID: d.RequestID, ExpectedBinding: f.binding, ViewerID: attached.ViewerID, ControllerEpoch: &attached.Controller.Epoch}, *attached.ControlCapability)
	expectFailure(t, err, 410, nativecontract.ReasonControllerExpired)
}

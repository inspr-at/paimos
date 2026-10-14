// SPDX-License-Identifier: AGPL-3.0-only

package nativerecovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativeadmission"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
)

type fixtureProfiles struct{}

func (fixtureProfiles) Resolve(_ context.Context, p nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	return nativecontract.LocalProfile{AccountID: p.AccountID, ProfileBindingRevision: p.BindingRevision,
		AccountHome: "/fixture/account", UserHome: "/fixture/owner", WorkspaceRoot: "/fixture/workspace", ConversationRoot: "/fixture/history",
		Binary: nativecontract.BinaryPin{Path: "/fixture/unexecuted-vendor", SHA256: strings.Repeat("a", 64)}}, nil
}

type fixtureAuthority struct{ revoked atomic.Bool }

func (a *fixtureAuthority) Revalidate(context.Context, nativeadmission.Request) error {
	if a.revoked.Load() {
		return fail(nativecontract.ReasonPermissionRevoked)
	}
	return nil
}
func (*fixtureAuthority) AuthorizeProfileChange(context.Context, nativecontract.ProfileRequest) error {
	panic("recovery must not change vendor profiles")
}

type recoveryFixture struct {
	mu             sync.Mutex // owning mutation fence shared by both coordinators
	now            time.Time
	snapshot       Snapshot
	request        nativeadmission.Request
	gate           *nativeadmission.Gate
	permit         *nativeadmission.Permit
	ledger         *agentsetup.SharedLedger
	authority      *fixtureAuthority
	state          *agentsetup.Store
	receipts       *nativereceipts.Store
	receiptPath    string
	coordinator    *Coordinator
	actor          nativecontract.Actor
	oldBinding     nativecontract.Binding
	absent         bool
	released       bool
	expires        int
	confirms       int
	prepares       int
	spawns         int
	checks         int
	proofEdit      func(*Absence)
	prepareEdit    func(*Successor)
	check          func()
	crashStage     string
	entered        chan struct{}
	continueCrash  chan struct{}
	approvalLive   bool
	controllerLive bool
}

func newFixture(t *testing.T, worker bool) *recoveryFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryFixture{now: time.Date(2026, 10, 14, 7, 0, 0, 0, time.UTC), authority: &fixtureAuthority{}, absent: true, approvalLive: true, controllerLive: true}
	f.ledger, err = agentsetup.OpenSharedLedger(filepath.Join(base, "ledger"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.ledger.Close() })
	data, _, err := f.ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	member, err := agentsetup.LedgerID()
	if err != nil {
		t.Fatal(err)
	}
	label, err := agentsetup.InstanceLabel("recovery-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ledger.Register(agentsetup.LedgerMember{ID: member, Label: label, Root: filepath.Join(base, "owner"), JoinedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	_, err = f.ledger.Import(member, agentsetup.LedgerInstance{Fingerprint: agentsetup.LedgerFingerprint(data.Generation, "instance", member), PID: os.Getpid(), StartedAt: f.now, MaximumAgents: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.ledger.Enrolled(member, data.Generation); err != nil {
		t.Fatal(err)
	}
	admissionState, err := agentsetup.OpenStore(filepath.Join(base, "admission"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admissionState.Close() })
	f.state, err = agentsetup.OpenStore(filepath.Join(base, "recovery"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.state.Close() })
	f.receiptPath = filepath.Join(base, "receipts")
	f.receipts, err = nativereceipts.Open(f.receiptPath, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.receipts.Close() })
	b := nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(),
		RuntimeEpoch: uuid.NewString(), DaemonID: "fixture-daemon", DaemonGeneration: "old-generation", BindingRevision: 1}
	if worker {
		id := uuid.NewString()
		b.RunID = &id
	}
	f.oldBinding = cloneBinding(b)
	policy := nativecontract.Policy{GrantID: uuid.NewString(), GrantRevision: 1, ExpiresAt: f.now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("b", 64),
		SettingsPolicyDigest: strings.Repeat("c", 64), PermissionPolicy: "supervised", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 10, MaximumTokens: 10000}
	owner := uuid.NewString()
	f.snapshot = Snapshot{Writer: nativecontract.WriterRecord{Binding: b, AttemptID: uuid.NewString()}, Revision: 1, OwnerPersonID: owner, Policy: policy,
		History: History{ConversationRef: "opaque-conversation", AccountID: uuid.NewString(), WorkspaceID: "workspace", ResumeRef: "local-native-ref",
			MappingDigest: strings.Repeat("d", 64), AccountBindingDigest: strings.Repeat("e", 64), ProfileRevision: 1},
		Budget: Budget{StartedAt: f.now.Add(-time.Minute), TurnsUsed: 1, TokensUsed: 1000, UsageKnown: true}, Cause: DaemonLost}
	f.actor = nativecontract.Actor{TenantID: b.TenantID, PrincipalID: owner, OwnerPersonID: owner, PrincipalKind: "person", GrantID: policy.GrantID, GrantRevision: policy.GrantRevision}
	f.request = nativeadmission.Request{Binding: b, Profile: nativecontract.ProfileRequest{TenantID: b.TenantID, OwnerPersonID: owner, AccountID: f.snapshot.History.AccountID,
		AccountKey: "owner-account", WorkspaceID: "workspace", Harness: "claude", Policy: policy, BindingRevision: 1},
		Launch: nativecontract.LaunchConfig{ModelProfileID: uuid.NewString(), Model: "fixture-model", ServiceTier: "default", ToolPolicyDigest: policy.ToolPolicyDigest, SettingsPolicyDigest: policy.SettingsPolicyDigest},
		Login:  agentsetup.LedgerLogin{Harness: "claude", Identity: strings.Repeat("f", 64)}, AccountBindingDigest: f.snapshot.History.AccountBindingDigest}
	f.gate, err = nativeadmission.New(nativeadmission.Options{Ledger: f.ledger, State: admissionState, Profiles: fixtureProfiles{}, Authority: f.authority, MemberID: member, Generation: data.Generation, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	f.permit, err = f.gate.Reserve(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	return f
}

func (f *recoveryFixture) restart(t *testing.T) {
	t.Helper()
	var err error
	f.coordinator, err = New(Config{Runtime: f, State: f.state, Receipts: f.receipts, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *recoveryFixture) WithWriter(ctx context.Context, _ nativecontract.Binding, fn func(context.Context) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

func (f *recoveryFixture) Read(_ context.Context, b nativecontract.Binding) (Snapshot, error) {
	if !b.Equal(f.snapshot.Writer.Binding) {
		return Snapshot{}, fail(nativecontract.ReasonWrongGeneration)
	}
	return f.snapshot, nil
}

func (f *recoveryFixture) ExpireCallbacks(_ context.Context, b nativecontract.Binding) error {
	if !b.Equal(f.snapshot.Writer.Binding) {
		return fail(nativecontract.ReasonWrongGeneration)
	}
	f.expires++
	f.approvalLive, f.controllerLive = false, false
	return nil
}

func (f *recoveryFixture) ProveAbsent(_ context.Context, w nativecontract.WriterRecord) (Absence, error) {
	if !f.absent || w.PossibleFork && w.Identity.ProcessIdentityDigest == "" {
		return Absence{}, fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	kind := "never_forked"
	if w.PossibleFork {
		kind = "owned_exit"
	}
	p := Absence{Binding: cloneBinding(w.Binding), AttemptID: w.AttemptID, Kind: kind, AttestationDigest: strings.Repeat("a", 64),
		Evidence: nativecontract.ExitEvidence{ProcessIdentityDigest: w.Identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: true, ObservedAt: f.now}}
	if f.proofEdit != nil {
		f.proofEdit(&p)
	}
	return p, nil
}

func (f *recoveryFixture) ConfirmAbsent(ctx context.Context, w nativecontract.WriterRecord, p Absence) error {
	f.confirms++
	if f.released {
		return nil
	}
	var err error
	if !w.PossibleFork {
		err = f.gate.Abandon(ctx, f.permit)
	} else {
		err = f.gate.ConfirmExit(ctx, f.permit, p.Evidence)
	}
	if err == nil {
		f.released = true
	}
	return err
}

func (f *recoveryFixture) Prepare(ctx context.Context, _ nativecontract.Actor, _ nativecontract.ResumeIntent, effects Effects) (Successor, error) {
	f.prepares++
	b := cloneBinding(f.snapshot.Writer.Binding)
	b.HarnessSessionID, b.RuntimeEpoch, b.DaemonGeneration, b.BindingRevision = uuid.NewString(), uuid.NewString(), uuid.NewString(), b.BindingRevision+1
	profile, err := (fixtureProfiles{}).Resolve(ctx, f.request.Profile)
	if err != nil {
		return Successor{}, err
	}
	policy := f.snapshot.Policy
	if policy.Continuation == "bounded_preapproved" {
		end := f.coordinator.windowEnd(f.snapshot)
		policy.ExpiresAt, policy.MaximumRuntimeSeconds = end, int64(end.Sub(f.now)/time.Second)
		policy.MaximumTurns -= f.snapshot.Budget.TurnsUsed
		policy.MaximumTokens -= f.snapshot.Budget.TokensUsed
	}
	next := Successor{Launch: nativecontract.LaunchSpec{Binding: b, Profile: profile, Policy: policy, Cwd: "/fixture/workspace", ResumeRef: f.snapshot.History.ResumeRef,
		Mode: "structured", OwnedWriterID: uuid.NewString(), Options: f.request.Launch, Lifetime: context.Background()},
		History: f.snapshot.History, RuntimeProofDigest: strings.Repeat("a", 64), AuthorizationDeadline: f.now.Add(time.Minute), ClaimKind: "resume", LaunchAuthorized: true}
	if f.prepareEdit != nil {
		f.prepareEdit(&next)
	}
	return next, nil
}

func (f *recoveryFixture) Revalidate(ctx context.Context, _ nativecontract.Actor, _ nativecontract.ResumeIntent, _ Successor, _ Effects) error {
	f.checks++
	if f.check != nil {
		f.check()
	}
	return f.authority.Revalidate(ctx, f.request)
}

func (f *recoveryFixture) barrier(stage string) {
	if f.crashStage != stage {
		return
	}
	close(f.entered)
	<-f.continueCrash
	panic("fixture crash: " + stage)
}

func (f *recoveryFixture) Attempt(ctx context.Context, next Successor) (nativecontract.NativeIdentity, error) {
	r := f.request
	r.Binding, r.Profile.Policy = next.Launch.Binding, next.Launch.Policy
	p, err := f.gate.Reserve(ctx, r)
	if err != nil {
		return nativecontract.NativeIdentity{}, err
	}
	f.permit, f.request, f.released = p, r, false
	f.snapshot.Writer = nativecontract.WriterRecord{Binding: r.Binding, AttemptID: next.Launch.OwnedWriterID}
	f.snapshot.Revision++
	f.barrier("before_possible_fork")
	var identity nativecontract.NativeIdentity
	err = f.gate.Attempt(ctx, p, func(context.Context, nativecontract.LocalProfile) (nativecontract.NativeIdentity, error) {
		f.snapshot.Writer.PossibleFork = true
		f.barrier("after_possible_fork")
		f.spawns++ // a fake native child, never an executed vendor or model
		f.barrier("after_native_open")
		identity = nativecontract.NativeIdentity{RuntimeEpoch: r.Binding.RuntimeEpoch, PID: 4242, ProcessIdentityDigest: strings.Repeat("1", 64), StartedAt: f.now}
		return identity, nil
	})
	if err == nil {
		f.snapshot.Writer.Identity = identity
	}
	return identity, err
}

func (f *recoveryFixture) startOld(t *testing.T) {
	t.Helper()
	identity := nativecontract.NativeIdentity{RuntimeEpoch: f.oldBinding.RuntimeEpoch, PID: 3131, ProcessIdentityDigest: strings.Repeat("2", 64), StartedAt: f.now.Add(-time.Minute)}
	if err := f.gate.Attempt(t.Context(), f.permit, func(context.Context, nativecontract.LocalProfile) (nativecontract.NativeIdentity, error) {
		return identity, nil
	}); err != nil {
		t.Fatal(err)
	}
	f.snapshot.Writer.PossibleFork, f.snapshot.Writer.Identity = true, identity
}

func (f *recoveryFixture) intent(reason string) nativecontract.ResumeIntent {
	return nativecontract.ResumeIntent{RequestID: uuid.NewString(), ExpectedBinding: cloneBinding(f.snapshot.Writer.Binding), ExpectedRevision: f.snapshot.Revision, Reason: reason, PolicyID: f.snapshot.Policy.GrantID}
}

func reason(t *testing.T, err error, want nativecontract.Reason) {
	t.Helper()
	var native *nativecontract.ValidationError
	var receipt *nativereceipts.Error
	if errors.As(err, &native) && native.Reason == want || errors.As(err, &receipt) && receipt.Reason == want {
		return
	}
	t.Fatalf("got %v; want exact reason %s", err, want)
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-t.Context().Done():
		t.Fatal("barrier cancelled")
	case <-time.After(10 * time.Second):
		t.Fatal("barrier hung")
	}
}

func (f *recoveryFixture) held(t *testing.T) int {
	t.Helper()
	d, _, err := f.ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return len(d.Groups)
}

// Risk: view/API loss is mistaken for exit, releasing a real shared slot or
// reopening native history. Only an exact owned root/group proof settles it.
func TestLossKindsRetainWriterUntilExactAbsence(t *testing.T) {
	f := newFixture(t, false)
	f.startOld(t)
	for _, cause := range []Cause{ViewerDisconnected, APIUnavailable} {
		status, err := f.coordinator.Reconcile(t.Context(), f.oldBinding, cause)
		if err != nil || status.ViewerMayReconnect != (cause == ViewerDisconnected) || status.ExitConfirmed || f.expires != 0 || f.confirms != 0 || f.held(t) != 1 || f.spawns != 0 {
			t.Fatalf("loss changed writer: %+v, %v", status, err)
		}
	}
	f.absent = false
	status, err := f.coordinator.Reconcile(t.Context(), f.oldBinding, DaemonLost)
	reason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	if status.State != "ownership_lost" || f.held(t) != 1 || f.approvalLive || f.controllerLive {
		t.Fatal("daemon loss released uncertainty or retained old callbacks")
	}
	f.absent = true
	for _, edit := range []func(*Absence){
		func(p *Absence) { p.Evidence.GroupAbsent = false },
		func(p *Absence) { p.Evidence.ProcessIdentityDigest = strings.Repeat("3", 64) }, // reused PID
		func(p *Absence) { p.AttemptID = uuid.NewString() },
		func(p *Absence) { p.Binding.RuntimeEpoch = uuid.NewString() },
		func(p *Absence) { p.Evidence.ObservedAt = f.now.Add(time.Second) },
	} {
		f.proofEdit = edit
		_, err = f.coordinator.Reconcile(t.Context(), f.oldBinding, ChildExited)
		reason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
		if f.held(t) != 1 {
			t.Fatal("wrong proof released slot")
		}
	}
	f.proofEdit = nil
	status, err = f.coordinator.Reconcile(t.Context(), f.oldBinding, ChildExited)
	if err != nil || !status.ExitConfirmed || status.State != "awaiting_continue" || f.held(t) != 0 || f.spawns != 0 {
		t.Fatalf("exact exit: %+v, %v", status, err)
	}
}

// Risk: a restart crosses either journal/fork boundary twice. Real admission
// journal/ledger holds survive coordinator recreation; explicit barriers prove
// the state before the simulated crash, rather than timing a racing goroutine.
func TestCrashForkWindowsNeverDuplicateOrReleaseUncertainWriter(t *testing.T) {
	for _, stage := range []string{"before_possible_fork", "after_possible_fork", "after_native_open"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t, true)
			f.crashStage, f.entered, f.continueCrash = stage, make(chan struct{}), make(chan struct{})
			in := f.intent("person_continue")
			done, crashed := make(chan struct{}), make(chan any, 1)
			go func() {
				defer close(done)
				defer func() { crashed <- recover() }()
				_, _ = f.coordinator.Resume(t.Context(), f.actor, in)
			}()
			await(t, f.entered)
			d, _, err := f.ledger.Snapshot()
			if err != nil || len(d.Groups) != 1 {
				t.Fatalf("fork barrier lost occupancy: %+v, %v", d, err)
			}
			want := "pending"
			if stage != "before_possible_fork" {
				want = "launching"
			}
			for _, group := range d.Groups {
				if group.State != want {
					t.Fatalf("barrier ledger state %s; want %s", group.State, want)
				}
			}
			close(f.continueCrash)
			await(t, done)
			if got := <-crashed; got != "fixture crash: "+stage {
				t.Fatalf("wrong crash reason: %v", got)
			}
			f.crashStage = ""
			f.restart(t)
			_, err = f.coordinator.Resume(t.Context(), f.actor, in)
			reason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
			spawns := f.spawns
			if f.approvalLive || f.controllerLive || f.held(t) != 1 {
				t.Fatal("crash replay kept callbacks or released uncertainty")
			}
			_, err = f.coordinator.Resume(t.Context(), f.actor, f.intent("person_continue"))
			if stage == "before_possible_fork" {
				if err != nil || f.spawns != spawns+1 {
					t.Fatalf("verified no-fork manual successor failed: %v", err)
				}
			} else {
				reason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
				if f.spawns != spawns || f.held(t) != 1 {
					t.Fatal("possible fork was duplicated/released")
				}
			}
		})
	}
}

// Risk: stale IDs/history, a drain-only claim, revoked permissions or a grant
// expiring at the disk barrier masquerade as fresh resume authority.
func TestResumeRequiresCurrentProofPolicyHistoryAndExpiresCallbacks(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Successor)
		want nativecontract.Reason
	}{
		{"old runtime", func(s *Successor) { s.Launch.Binding.RuntimeEpoch = "old" }, nativecontract.ReasonInvalidInput},
		{"old harness", func(s *Successor) { s.Launch.Binding.HarnessSessionID = "old" }, nativecontract.ReasonInvalidInput},
		{"missing proof", func(s *Successor) { s.RuntimeProofDigest = "" }, nativecontract.ReasonGrantMissing},
		{"final drain", func(s *Successor) { s.ClaimKind, s.LaunchAuthorized = "final_drain", false }, nativecontract.ReasonGrantMissing},
		{"different account", func(s *Successor) { s.History.AccountID = uuid.NewString() }, nativecontract.ReasonQualificationMissing},
		{"saved native ref only", func(s *Successor) { s.History.MappingDigest = "" }, nativecontract.ReasonQualificationMissing},
		{"history disabled", func(s *Successor) { s.Launch.Policy.LocalHistory = "disabled" }, nativecontract.ReasonQualificationMissing},
		{"policy widened", func(s *Successor) { s.Launch.Policy.PermissionPolicy = "bounded_preapproved" }, nativecontract.ReasonIdentityMismatch},
		{"implicit old input", func(s *Successor) { s.Launch.InitialInput = &nativecontract.Input{} }, nativecontract.ReasonQualificationMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.prepareEdit = tc.edit
			_, err := f.coordinator.Resume(t.Context(), f.actor, f.intent("person_continue"))
			reason(t, err, tc.want)
			if f.spawns != 0 || f.approvalLive || f.controllerLive {
				t.Fatal("refused resume opened a writer or retained stale callbacks")
			}
		})
	}
	for _, revoke := range []bool{true, false} {
		f := newFixture(t, true)
		f.check = func() {
			if f.checks == 2 {
				if revoke {
					f.authority.revoked.Store(true)
				} else {
					f.now = f.now.Add(2 * time.Minute)
				}
			}
		}
		_, err := f.coordinator.Resume(t.Context(), f.actor, f.intent("person_continue"))
		if revoke {
			reason(t, err, nativecontract.ReasonPermissionRevoked)
		} else {
			// The owner's final fork check must also reject a deadline expiring
			// during a revalidation. The production coordinator checks it again.
			reason(t, err, nativecontract.ReasonGrantExpired)
		}
		if f.spawns != 0 {
			t.Fatal("authority changed at final check but a writer opened")
		}
	}
	f := newFixture(t, true)
	in := f.intent("person_continue")
	status, err := f.coordinator.Resume(t.Context(), f.actor, in)
	if err != nil || status.State != "resumed" || status.Binding.RuntimeEpoch == f.oldBinding.RuntimeEpoch || status.Binding.HarnessSessionID == f.oldBinding.HarnessSessionID || f.spawns != 1 || f.held(t) != 1 || f.approvalLive || f.controllerLive {
		t.Fatalf("fresh successor: %+v, %v", status, err)
	}
	f.restart(t)
	replay, err := f.coordinator.Resume(t.Context(), f.actor, in)
	if err != nil || !replay.Binding.Equal(status.Binding) || f.spawns != 1 {
		t.Fatalf("completed replay: %+v, %v", replay, err)
	}
	in.PolicyID = uuid.NewString()
	_, err = f.coordinator.Resume(t.Context(), f.actor, in)
	reason(t, err, nativecontract.ReasonIdentityMismatch)
}

// Risk: restarting the daemon renews the original time/cost grant or resets its
// one-continuation counter. The private decision survives coordinator restart.
func TestBoundedContinuationUsesOriginalWindowAtMostOnce(t *testing.T) {
	f := newFixture(t, false)
	_, err := f.coordinator.Resume(t.Context(), f.actor, f.intent("approved_restart"))
	reason(t, err, nativecontract.ReasonGrantMissing)
	if f.spawns != 0 || f.expires != 0 || f.held(t) != 1 {
		t.Fatal("default policy continued automatically")
	}
	for _, mode := range []string{"expired", "unknown usage", "tokens spent", "window renewed", "valid"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, true)
			f.snapshot.Policy.Continuation = "bounded_preapproved"
			f.actor.PrincipalID, f.actor.PrincipalKind = uuid.NewString(), "agent"
			want := nativecontract.ReasonGrantExpired
			switch mode {
			case "expired":
				f.now = f.now.Add(time.Hour)
			case "unknown usage":
				f.snapshot.Budget.UsageKnown, want = false, nativecontract.ReasonInputOutcomeUnconfirmed
			case "tokens spent":
				f.snapshot.Budget.TokensUsed = f.snapshot.Policy.MaximumTokens
			case "window renewed":
				f.prepareEdit = func(s *Successor) { s.Launch.Policy.ExpiresAt = f.now.Add(time.Hour) }
			}
			in := f.intent("approved_restart")
			_, err := f.coordinator.Resume(t.Context(), f.actor, in)
			if mode != "valid" {
				reason(t, err, want)
				if f.spawns != 0 {
					t.Fatal("unqualified bounded continuation spawned")
				}
				return
			}
			if err != nil || f.spawns != 1 {
				t.Fatalf("bounded continuation: %v", err)
			}
			f.restart(t)
			_, err = f.coordinator.Resume(t.Context(), f.actor, f.intent("approved_restart"))
			reason(t, err, nativecontract.ReasonGrantMissing)
			if f.spawns != 1 || f.held(t) != 1 {
				t.Fatal("restart renewed continuation or freed writer")
			}
		})
	}
}

// Risk: corrupt/unbounded local decision metadata is adopted as proof, or local
// authorization objects leak through a JSON projection. No content is retained.
func TestRecoveryJournalBoundsAndLocalAuthorityRefusal(t *testing.T) {
	f := newFixture(t, false)
	for _, value := range []any{f.snapshot, Absence{}, Successor{}} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("local recovery authority was serialized")
		}
	}
	if err := f.state.Write(journalName(f.oldBinding.OwnedSessionID), []byte(`{"version":1,"tenant_id":"wrong","attempts":[]}`), false); err != nil {
		t.Fatal(err)
	}
	_, err := f.coordinator.Resume(t.Context(), f.actor, f.intent("person_continue"))
	reason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	if f.spawns != 0 || f.held(t) != 1 {
		t.Fatal("corrupt recovery metadata changed occupancy")
	}
}

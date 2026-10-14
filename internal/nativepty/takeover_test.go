// SPDX-License-Identifier: AGPL-3.0-only

package nativepty

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time                       { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }
func (c *testClock) advance(d time.Duration)              { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

type testAuthority struct {
	mu         sync.Mutex
	access     Access
	capability string
	err        error
	calls      int
	before     func()
}

func (a *testAuthority) WithWrite(ctx context.Context, c Caller, b nativecontract.Binding, op string, fn func(Access) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return a.err
	}
	if a.before != nil {
		a.before()
	}
	if c.Capability != a.capability {
		return refuse(nativecontract.ReasonGrantMissing)
	}
	return fn(a.access)
}

type testQualification struct {
	err    error
	calls  int
	before func(string)
}

func (q *testQualification) Require(_ context.Context, _ nativecontract.Actor, _ nativecontract.LaunchSpec, mode string) error {
	q.calls++
	if q.before != nil {
		q.before(mode)
	}
	return q.err
}

type writerFixture struct {
	mu                                 sync.Mutex
	spec                               nativecontract.LaunchSpec
	identity                           nativecontract.NativeIdentity
	active, starts, maximum, callbacks int
	inputOpen                          bool
	exitEntered, exitRelease           chan struct{}
	ptyEntered, ptyRelease             chan struct{}
	unknownExit, unknownPTY            bool
	prepareErr, commitErr              error
	tweak                              func(*nativecontract.LaunchSpec)
	identityTweak                      func(*nativecontract.NativeIdentity)
	pty                                *testPTY
	auth                               *testAuthority
}

func (f *writerFixture) Quiesce(_ context.Context, b nativecontract.Binding, op string) (nativecontract.WriterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputOpen = false
	f.callbacks = 0
	return nativecontract.WriterRecord{Binding: cloneBinding(f.spec.Binding), Identity: f.identity, AttemptID: f.spec.OwnedWriterID, PossibleFork: true}, nil
}
func (f *writerFixture) CloseStructured(ctx context.Context, w nativecontract.WriterRecord) (nativecontract.ExitEvidence, error) {
	if f.exitEntered != nil {
		close(f.exitEntered)
		select {
		case <-ctx.Done():
			return nativecontract.ExitEvidence{}, ctx.Err()
		case <-f.exitRelease:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inputOpen || f.callbacks != 0 {
		return nativecontract.ExitEvidence{}, errors.New("structured writes/callbacks were not quiesced")
	}
	if !f.unknownExit {
		f.active--
	}
	return nativecontract.ExitEvidence{ProcessIdentityDigest: w.Identity.ProcessIdentityDigest, RootExited: !f.unknownExit, GroupAbsent: !f.unknownExit, ObservedAt: time.Now()}, nil
}
func (f *writerFixture) ConfirmExit(_ context.Context, w nativecontract.WriterRecord, e nativecontract.ExitEvidence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active != 0 || !e.RootExited || !e.GroupAbsent || e.ProcessIdentityDigest != w.Identity.ProcessIdentityDigest {
		return refuse(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return nil
}
func (f *writerFixture) PrepareSuccessor(_ context.Context, _ nativecontract.Actor, b nativecontract.Binding, mode string) (nativecontract.LaunchSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prepareErr != nil {
		return nativecontract.LaunchSpec{}, f.prepareErr
	}
	if f.active != 0 {
		return nativecontract.LaunchSpec{}, errors.New("successor admission before owned exit")
	}
	s := cloneSpec(f.spec)
	s.Binding.RuntimeEpoch = uuid.NewString()
	s.Binding.HarnessSessionID = uuid.NewString()
	s.Binding.BindingRevision++
	s.Mode, s.OwnedWriterID, s.Lifetime = mode, uuid.NewString(), context.Background()
	s.Tools.Binding = s.Binding
	s.Tools.Token = bytes.Repeat([]byte{byte(f.starts + 1)}, 32)
	if f.tweak != nil {
		f.tweak(&s)
	}
	return s, nil
}
func (f *writerFixture) CommitRuntime(_ context.Context, b nativecontract.Binding, i nativecontract.NativeIdentity, mode string) (nativecontract.Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitErr != nil {
		return b, f.commitErr
	}
	f.spec.Binding = b
	f.spec.Mode = mode
	f.spec.OwnedWriterID = uuid.NewString()
	f.identity = i
	return b, nil
}
func (f *writerFixture) start(s nativecontract.LaunchSpec) nativecontract.NativeIdentity {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active++
	f.starts++
	f.maximum = max(f.maximum, f.active)
	i := nativecontract.NativeIdentity{Provider: "fixture", NativeSessionRef: s.ResumeRef, RuntimeEpoch: s.Binding.RuntimeEpoch, PID: 100 + f.starts, StartedAt: time.Now(), ProcessIdentityDigest: strings.Repeat(string(rune('a'+f.starts)), 64)}
	if f.identityTweak != nil {
		f.identityTweak(&i)
	}
	return i
}
func (f *writerFixture) OpenPTY(_ context.Context, s nativecontract.PTYLaunchSpec) (nativecontract.PTYRuntime, error) {
	f.pty = &testPTY{fixture: f, identity: f.start(s.LaunchSpec)}
	return f.pty, nil
}
func (f *writerFixture) Open(_ context.Context, s nativecontract.LaunchSpec) (nativecontract.NativeTransport, error) {
	return &testStructured{identity: f.start(s)}, nil
}
func (f *writerFixture) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, f.starts, f.maximum
}

type testPTY struct {
	fixture             *writerFixture
	identity            nativecontract.NativeIdentity
	resizes, writes     int
	closed              bool
	resizeErr, writeErr error
}

func (p *testPTY) Identity() nativecontract.NativeIdentity { return p.identity }
func (p *testPTY) Frames() <-chan nativecontract.TerminalFrame {
	return make(chan nativecontract.TerminalFrame)
}
func (p *testPTY) Write(_ context.Context, in nativecontract.PTYInput) (nativecontract.NativeEvidence, error) {
	p.writes++
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", RequestID: in.RequestID}, p.writeErr
}
func (p *testPTY) Resize(_ context.Context, in nativecontract.Resize) (nativecontract.NativeEvidence, error) {
	p.resizes++
	return nativecontract.NativeEvidence{Stage: "applied", RequestID: in.RequestID}, p.resizeErr
}
func (p *testPTY) Close(ctx context.Context) (nativecontract.ExitEvidence, error) {
	f := p.fixture
	if f.ptyEntered != nil {
		close(f.ptyEntered)
		select {
		case <-ctx.Done():
			return nativecontract.ExitEvidence{}, ctx.Err()
		case <-f.ptyRelease:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.unknownPTY && !p.closed {
		f.active--
		p.closed = true
	}
	return nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: !f.unknownPTY, GroupAbsent: !f.unknownPTY, ObservedAt: time.Now()}, nil
}
func (p *testPTY) Wait(context.Context) (nativecontract.ExitEvidence, error) {
	return nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: p.closed, GroupAbsent: p.closed, ObservedAt: time.Now()}, nil
}

type testStructured struct{ identity nativecontract.NativeIdentity }

func (p *testStructured) Identity() nativecontract.NativeIdentity { return p.identity }
func (*testStructured) Send(context.Context, nativecontract.Input) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, nil
}
func (*testStructured) Interrupt(context.Context, nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, nil
}
func (*testStructured) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, nil
}
func (*testStructured) Close(context.Context) (nativecontract.ExitEvidence, error) {
	return nativecontract.ExitEvidence{}, nil
}
func (*testStructured) Wait(context.Context) (nativecontract.ExitEvidence, error) {
	return nativecontract.ExitEvidence{}, nil
}
func (*testStructured) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{}
}
func (*testStructured) Events() <-chan nativecontract.ProviderEvent {
	return make(chan nativecontract.ProviderEvent)
}

func takeoverFixture(t *testing.T) (*Manager, *writerFixture, Caller, *testClock, *testQualification) {
	t.Helper()
	now := time.Now()
	clock := &testClock{now: now}
	b := nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: "fixture-daemon", DaemonGeneration: "fixture-daemon-generation", BindingRevision: 1}
	policy := nativecontract.Policy{GrantID: uuid.NewString(), GrantRevision: 1, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64), PermissionPolicy: "bounded_preapproved", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 10, MaximumTokens: 1000}
	effort := "high"
	s := nativecontract.LaunchSpec{Binding: b, Mode: "structured", Cwd: "/fixture/work", ResumeRef: "fixture-conversation", OwnedWriterID: uuid.NewString(), Lifetime: context.Background(), Policy: policy, Profile: nativecontract.LocalProfile{AccountID: uuid.NewString(), AccountHome: "/fixture/account", WorkspaceRoot: "/fixture/work", UserHome: "/fixture/user", ConversationRoot: "/fixture/account/history", ProfileBindingRevision: 1}, Options: nativecontract.LaunchConfig{ModelProfileID: uuid.NewString(), Model: "fixture-model", Effort: &effort, ServiceTier: "standard", ToolPolicyDigest: policy.ToolPolicyDigest, SettingsPolicyDigest: policy.SettingsPolicyDigest}}
	s.Tools = nativecontract.ToolCapability{Binding: b, Token: bytes.Repeat([]byte{1}, 32), ExpiresAt: now.Add(time.Hour)}
	c := Caller{Actor: nativecontract.Actor{TenantID: b.TenantID, PrincipalID: uuid.NewString(), PrincipalKind: "person", OwnerPersonID: uuid.NewString(), GrantID: policy.GrantID, GrantRevision: 1}, ClientID: uuid.NewString(), Capability: "fixture-control-proof", Local: true}
	a := &testAuthority{capability: c.Capability, access: Access{Controller: nativecontract.ControllerView{ActorID: c.Actor.PrincipalID, ClientID: c.ClientID, ControllerID: uuid.NewString(), Epoch: uuid.NewString()}, Deadline: now.Add(30 * time.Second)}}
	f := &writerFixture{spec: s, active: 1, starts: 1, maximum: 1, callbacks: 3, inputOpen: true, auth: a, identity: nativecontract.NativeIdentity{Provider: "fixture", NativeSessionRef: s.ResumeRef, RuntimeEpoch: b.RuntimeEpoch, PID: 100, StartedAt: now, ProcessIdentityDigest: strings.Repeat("a", 64)}}
	q := &testQualification{}
	m, err := New(Config{Current: s, State: "running", Revision: 1, Coordinator: f, Authority: a, Qualification: q, PTY: f, Structured: f, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return m, f, c, clock, q
}
func takeRequest(m *Manager, f *writerFixture) nativecontract.TakeIntent {
	return nativecontract.TakeIntent{RequestID: uuid.NewString(), ExpectedBinding: cloneBinding(m.spec.Binding), ControllerEpoch: f.auth.access.Controller.Epoch, ExpectedRevision: m.revision, Cols: 120, Rows: 36, ExpiresAt: f.auth.access.Deadline}
}
func resizeRequest(m *Manager, f *writerFixture, cols, rows int64) nativecontract.Resize {
	return nativecontract.Resize{RequestID: uuid.NewString(), ExpectedBinding: cloneBinding(m.spec.Binding), ControllerEpoch: f.auth.access.Controller.Epoch, Cols: cols, Rows: rows, ExpiresAt: f.auth.access.Deadline}
}
func freshControl(f *writerFixture, c Caller, clock *testClock) {
	f.auth.mu.Lock()
	defer f.auth.mu.Unlock()
	f.auth.access.Controller.Epoch = uuid.NewString()
	f.auth.access.Controller.ControllerID = uuid.NewString()
	f.auth.access.Controller.ClientID = c.ClientID
	f.auth.access.Deadline = clock.Now().Add(30 * time.Second)
}
func takeAndRegain(t *testing.T, m *Manager, f *writerFixture, c Caller, clock *testClock) {
	t.Helper()
	if _, err := m.Take(context.Background(), c, takeRequest(m, f)); err != nil {
		t.Fatal(err)
	}
	freshControl(f, c, clock)
	if _, err := m.RegainControl(context.Background(), c, resizeRequest(m, f, 120, 36)); err != nil {
		t.Fatal(err)
	}
}
func requireReason(t *testing.T, err error, want nativecontract.Reason) {
	t.Helper()
	var e *nativecontract.ValidationError
	if !errors.As(err, &e) || e.Reason != want {
		t.Fatalf("want exact reason %s, got %v", want, err)
	}
}
func waitBarrier(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("exit barrier not reached")
	}
}

// Named risk: a TUI is another writer. Both process exits are held by explicit
// barriers; no elapsed-time assertion can pass without reaching the close.
func TestTakeoverWaitsForBothOwnedExits(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	f.exitEntered, f.exitRelease = make(chan struct{}), make(chan struct{})
	in := takeRequest(m, f)
	done := make(chan error, 1)
	go func() { _, err := m.Take(context.Background(), c, in); done <- err }()
	waitBarrier(t, f.exitEntered)
	if active, starts, maximum := f.counts(); active != 1 || starts != 1 || maximum != 1 {
		t.Fatalf("PTY launched before structured exit: %d %d %d", active, starts, maximum)
	}
	close(f.exitRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	old := in.ExpectedBinding
	if old.OwnedSessionID != m.spec.Binding.OwnedSessionID || old.RuntimeEpoch == m.spec.Binding.RuntimeEpoch || old.HarnessSessionID == m.spec.Binding.HarnessSessionID {
		t.Fatal("logical conversation/runtime identity did not change correctly")
	}
	if m.holder != nil || f.callbacks != 0 || f.inputOpen {
		t.Fatal("old controller/callback/structured input survived take")
	}
	freshControl(f, c, clock)
	if _, err := m.RegainControl(context.Background(), c, resizeRequest(m, f, 132, 40)); err != nil {
		t.Fatal(err)
	}
	f.ptyEntered, f.ptyRelease = make(chan struct{}), make(chan struct{})
	ret := nativecontract.ReturnIntent{RequestID: uuid.NewString(), ExpectedBinding: cloneBinding(m.spec.Binding), ControllerEpoch: f.auth.access.Controller.Epoch, ExpectedRevision: m.revision, ExpiresAt: f.auth.access.Deadline}
	go func() { _, err := m.Return(context.Background(), c, ret); done <- err }()
	waitBarrier(t, f.ptyEntered)
	if active, starts, maximum := f.counts(); active != 1 || starts != 2 || maximum != 1 {
		t.Fatalf("structured successor launched before PTY exit: %d %d %d", active, starts, maximum)
	}
	close(f.ptyRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if active, starts, maximum := f.counts(); active != 1 || starts != 3 || maximum != 1 {
		t.Fatalf("writer overlap: %d %d %d", active, starts, maximum)
	}
	if m.state != "idle" || m.mode != "structured" || m.holder != nil || m.cols != 132 || m.rows != 40 {
		t.Fatal("hand-back did not retain size and retire terminal control")
	}
}

func TestTakeoverUnknownExitRetainsOccupancy(t *testing.T) {
	for _, side := range []string{"structured", "pty"} {
		t.Run(side, func(t *testing.T) {
			m, f, c, clock, _ := takeoverFixture(t)
			if side == "pty" {
				takeAndRegain(t, m, f, c, clock)
				f.unknownPTY = true
			} else {
				f.unknownExit = true
			}
			var err error
			if side == "pty" {
				_, err = m.Return(context.Background(), c, nativecontract.ReturnIntent{RequestID: uuid.NewString(), ExpectedBinding: m.spec.Binding, ControllerEpoch: f.auth.access.Controller.Epoch, ExpectedRevision: m.revision, ExpiresAt: f.auth.access.Deadline})
			} else {
				_, err = m.Take(context.Background(), c, takeRequest(m, f))
			}
			requireReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
			status, _ := m.Status(context.Background())
			if status.State != "recovering" || !status.OccupancyHeld || status.HasController {
				t.Fatal("uncertain writer was released")
			}
			_, starts, _ := f.counts()
			want := 1
			if side == "pty" {
				want = 2
			}
			if starts != want {
				t.Fatal("unknown exit admitted successor")
			}
		})
	}
}
func TestTakeoverRequiresIndependentTUIFence(t *testing.T) {
	m, f, c, _, q := takeoverFixture(t)
	q.err = refuse(nativecontract.ReasonQualificationMissing)
	_, err := m.Take(context.Background(), c, takeRequest(m, f))
	requireReason(t, err, nativecontract.ReasonQualificationMissing)
	if f.callbacks != 3 || !f.inputOpen {
		t.Fatal("unqualified TUI quiesced serving runtime")
	}
	_, starts, _ := f.counts()
	if starts != 1 {
		t.Fatal("unqualified TUI launched")
	}
}
func TestTakeoverRechecksAuthorityAfterExit(t *testing.T) {
	m, f, c, _, _ := takeoverFixture(t)
	f.tweak = func(*nativecontract.LaunchSpec) { f.auth.err = refuse(nativecontract.ReasonPermissionRevoked) }
	_, err := m.Take(context.Background(), c, takeRequest(m, f))
	requireReason(t, err, nativecontract.ReasonPermissionRevoked)
	active, starts, _ := f.counts()
	if active != 0 || starts != 1 || m.state != "recovering" {
		t.Fatal("revoked grant launched a writer")
	}
}
func TestTakeoverRejectsSuccessorIdentityOrProofReuse(t *testing.T) {
	for _, change := range []string{"epoch", "harness", "profile", "conversation", "tools", "settings", "proof", "initial-input"} {
		t.Run(change, func(t *testing.T) {
			m, f, c, _, _ := takeoverFixture(t)
			f.tweak = func(s *nativecontract.LaunchSpec) {
				switch change {
				case "epoch":
					s.Binding.RuntimeEpoch = f.spec.Binding.RuntimeEpoch
				case "harness":
					s.Binding.HarnessSessionID = f.spec.Binding.HarnessSessionID
				case "profile":
					s.Profile.AccountID = uuid.NewString()
				case "conversation":
					s.ResumeRef = "other"
				case "tools":
					s.Policy.ToolPolicyDigest = strings.Repeat("c", 64)
				case "settings":
					s.Policy.SettingsPolicyDigest = strings.Repeat("c", 64)
				case "proof":
					s.Tools.Token = bytes.Repeat([]byte{1}, 32)
				case "initial-input":
					s.InitialInput = &nativecontract.Input{Text: "implicit retarget"}
				}
			}
			_, err := m.Take(context.Background(), c, takeRequest(m, f))
			requireReason(t, err, nativecontract.ReasonIdentityMismatch)
			_, starts, _ := f.counts()
			if starts != 1 {
				t.Fatal("invalid successor forked")
			}
		})
	}
}
func TestTakeoverFailedRebindKeepsPossibleFork(t *testing.T) {
	m, f, c, _, _ := takeoverFixture(t)
	f.commitErr = refuse(nativecontract.ReasonNativeFailure)
	_, err := m.Take(context.Background(), c, takeRequest(m, f))
	requireReason(t, err, nativecontract.ReasonNativeFailure)
	status, _ := m.Status(context.Background())
	if status.State != "recovering" || !status.OccupancyHeld || m.pty == nil {
		t.Fatal("failed rebind forgot possible writer")
	}
	_, err = m.Take(context.Background(), c, takeRequest(m, f))
	requireReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	active, starts, maximum := f.counts()
	if active != 1 || starts != 2 || maximum != 1 {
		t.Fatal("failed rebind admitted another writer")
	}
}
func TestTakeoverLaunchIdentityMustMatchConversationAndFreshProof(t *testing.T) {
	for _, change := range []string{"conversation", "provider", "process-proof"} {
		t.Run(change, func(t *testing.T) {
			m, f, c, _, _ := takeoverFixture(t)
			f.identityTweak = func(i *nativecontract.NativeIdentity) {
				switch change {
				case "conversation":
					i.NativeSessionRef = "foreign-conversation"
				case "provider":
					i.Provider = "foreign-provider"
				case "process-proof":
					i.ProcessIdentityDigest = f.identity.ProcessIdentityDigest
				}
			}
			_, err := m.Take(context.Background(), c, takeRequest(m, f))
			requireReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
			if m.state != "recovering" || m.pty == nil {
				t.Fatal("mismatched possible writer was forgotten")
			}
			active, starts, maximum := f.counts()
			if active != 1 || starts != 2 || maximum != 1 {
				t.Fatal("mismatched identity admitted another writer")
			}
		})
	}
}

func TestTakeoverClientLossAndExpiredLeaseDoNotRestart(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	takeAndRegain(t, m, f, c, clock)
	before := m.status()
	if err := m.ClientLost(context.Background(), c.ClientID, f.auth.access.Controller.Epoch, m.spec.Binding); err != nil {
		t.Fatal(err)
	}
	_, err := m.Resize(context.Background(), c, resizeRequest(m, f, 132, 40))
	requireReason(t, err, nativecontract.ReasonControllerExpired)
	if m.cols != before.Cols || m.rows != before.Rows {
		t.Fatal("client loss changed size")
	}
	freshControl(f, c, clock)
	if _, err = m.RegainControl(context.Background(), c, resizeRequest(m, f, 120, 36)); err != nil {
		t.Fatal(err)
	}
	clock.advance(31 * time.Second)
	_, err = m.Resize(context.Background(), c, resizeRequest(m, f, 132, 40))
	requireReason(t, err, nativecontract.ReasonControllerExpired)
	active, starts, maximum := f.counts()
	if active != 1 || starts != 2 || maximum != 1 {
		t.Fatal("disconnect/expiry restarted writer")
	}
}

func TestPTYResizeBounds(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	takeAndRegain(t, m, f, c, clock)
	for _, size := range [][2]int64{{39, 12}, {40, 11}, {321, 100}, {320, 101}, {0, 0}, {-1, 36}} {
		_, err := m.Resize(context.Background(), c, resizeRequest(m, f, size[0], size[1]))
		requireReason(t, err, nativecontract.ReasonInvalidInput)
	}
	for _, size := range [][2]int64{{40, 12}, {320, 100}} {
		if _, err := m.Resize(context.Background(), c, resizeRequest(m, f, size[0], size[1])); err != nil {
			t.Fatal(err)
		}
	}
}
func TestPTYResizeTwoHolderTabsAndViewers(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	takeAndRegain(t, m, f, c, clock)
	for _, kind := range []string{"other-tab", "browser", "agent", "viewer"} {
		t.Run(kind, func(t *testing.T) {
			other := c
			switch kind {
			case "other-tab":
				other.ClientID = uuid.NewString()
			case "browser":
				other.Local = false
			case "agent":
				other.Actor.PrincipalKind = "agent"
			case "viewer":
				other.Capability = ""
			}
			_, err := m.RegainControl(context.Background(), other, resizeRequest(m, f, 132, 40))
			want := nativecontract.ReasonControllerConflict
			if kind == "browser" || kind == "agent" {
				want = nativecontract.ReasonWrongPrincipal
			}
			if kind == "viewer" {
				want = nativecontract.ReasonGrantMissing
			}
			requireReason(t, err, want)
		})
	}
	if f.pty.resizes != 1 {
		t.Fatal("non-holder sized PTY")
	}
}
func TestPTYResizeOnlyOnChangeAndReconnect(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	takeAndRegain(t, m, f, c, clock)
	in := resizeRequest(m, f, 120, 36)
	if _, err := m.Resize(context.Background(), c, in); err != nil {
		t.Fatal(err)
	}
	if f.pty.resizes != 1 {
		t.Fatal("unchanged grid resized")
	}
	if _, err := m.RegainControl(context.Background(), c, in); err != nil {
		t.Fatal(err)
	}
	if f.pty.resizes != 2 {
		t.Fatal("reconnect did not reapply")
	}
	freshControl(f, c, clock)
	if _, err := m.RegainControl(context.Background(), c, resizeRequest(m, f, 120, 36)); err != nil {
		t.Fatal(err)
	}
	if f.pty.resizes != 3 {
		t.Fatal("regained lease did not reapply")
	}
}
func TestPTYResizeFailureDoesNotReportNewSize(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	takeAndRegain(t, m, f, c, clock)
	f.pty.resizeErr = refuse(nativecontract.ReasonNativeFailure)
	_, err := m.Resize(context.Background(), c, resizeRequest(m, f, 132, 40))
	requireReason(t, err, nativecontract.ReasonNativeFailure)
	if m.cols != 120 || m.rows != 36 || m.holder != nil {
		t.Fatal("failed resize reported success")
	}
}
func TestPTYResizePhoneKeyboardAndPausedInput(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	takeAndRegain(t, m, f, c, clock)
	for _, rows := range []int64{20, 36} {
		if _, err := m.Resize(context.Background(), c, resizeRequest(m, f, 120, rows)); err != nil {
			t.Fatal(err)
		}
		if m.rows != int(rows) {
			t.Fatal("phone keyboard size was not applied")
		}
	}
	m.state = "returning"
	_, err := m.Resize(context.Background(), c, resizeRequest(m, f, 132, 40))
	requireReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	if f.pty.resizes != 3 {
		t.Fatal("resize occurred while input paused")
	}
}
func TestTakeoverLeaseExpiryDuringQualificationPreventsFork(t *testing.T) {
	m, f, c, clock, q := takeoverFixture(t)
	q.before = func(string) {
		if q.calls == 2 {
			clock.advance(31 * time.Second)
		}
	}
	_, err := m.Take(context.Background(), c, takeRequest(m, f))
	requireReason(t, err, nativecontract.ReasonControllerExpired)
	_, starts, _ := f.counts()
	if starts != 1 {
		t.Fatal("qualification work extended expired control")
	}
}
func TestPTYInputRejectsStaleControlAndUncertainRetry(t *testing.T) {
	m, f, c, clock, _ := takeoverFixture(t)
	old := takeRequest(m, f)
	takeAndRegain(t, m, f, c, clock)
	in := nativecontract.PTYInput{RequestID: uuid.NewString(), ExpectedBinding: old.ExpectedBinding, ControllerEpoch: old.ControllerEpoch, Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString([]byte("x")), ExpiresAt: f.auth.access.Deadline}
	_, err := m.Input(context.Background(), c, in)
	requireReason(t, err, nativecontract.ReasonWrongGeneration)
	in.ExpectedBinding = m.spec.Binding
	_, err = m.Input(context.Background(), c, in)
	requireReason(t, err, nativecontract.ReasonControllerConflict)
	in.ControllerEpoch = f.auth.access.Controller.Epoch
	f.pty.writeErr = refuse(nativecontract.ReasonInputOutcomeUnconfirmed)
	_, err = m.Input(context.Background(), c, in)
	requireReason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	_, err = m.Input(context.Background(), c, in)
	requireReason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	if _, err = m.RegainControl(context.Background(), c, resizeRequest(m, f, 120, 36)); err != nil {
		t.Fatal(err)
	}
	_, err = m.Input(context.Background(), c, in)
	requireReason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	if f.pty.writes != 1 {
		t.Fatal("uncertain bytes were replayed")
	}
}

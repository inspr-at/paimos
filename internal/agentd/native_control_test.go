// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativepty"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
)

type nativePersonAuthorityFixture struct {
	actor nativecontract.Actor
	calls int
}

type nativePTYAuthorityFixture struct {
	mu         sync.Mutex
	caller     nativepty.Caller
	access     nativepty.Access
	binding    nativecontract.Binding
	viewer     string
	operations map[string]string
	deny       error
	before     func()
}

func (a *nativePTYAuthorityFixture) Prepare(_ context.Context, actor nativecontract.Actor, request nativestream.LocalRequest, digest string) (nativepty.Caller, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if actor.PrincipalID != a.caller.Actor.PrincipalID || request.ViewerID != a.viewer || request.ControlCapability != a.caller.Capability || request.ExpectedBinding == nil || !request.ExpectedBinding.Equal(a.binding) || len(digest) != 64 {
		return nativepty.Caller{}, "", nativeRefusal(nativecontract.ReasonGrantMissing)
	}
	id := a.operations[request.Envelope.RequestID]
	if id == "" {
		id = uuid.NewString()
		a.operations[request.Envelope.RequestID] = id
	}
	return a.caller, id, nil
}
func (a *nativePTYAuthorityFixture) WithWrite(ctx context.Context, caller nativepty.Caller, binding nativecontract.Binding, kind string, fn func(nativepty.Access) error) error {
	if a.before != nil {
		a.before()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deny != nil {
		return a.deny
	}
	if !binding.Equal(a.binding) || caller.Capability != a.caller.Capability {
		return nativeRefusal(nativecontract.ReasonGrantMissing)
	}
	return fn(a.access)
}

type nativePTYQualificationFixture struct{ err error }

func (q nativePTYQualificationFixture) Require(context.Context, nativecontract.Actor, nativecontract.LaunchSpec, string) error {
	return q.err
}

type nativePTYRevokedProfile struct{}

func (nativePTYRevokedProfile) Resolve(context.Context, nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	return nativecontract.LocalProfile{}, nativeRefusal(nativecontract.ReasonIdentityMismatch)
}

type nativePTYRuntimeFixture struct {
	mu                         sync.Mutex
	identity                   nativecontract.NativeIdentity
	frames                     chan nativecontract.TerminalFrame
	done                       chan struct{}
	readyEntered, readyRelease chan struct{}
	closeEntered, closeRelease chan struct{}
	once                       sync.Once
	writes, resizes, closes    int
	writeErr, errorExit        error
	readyErr                   error
	x                          nativecontract.ExitEvidence
	now                        time.Time
}

func (p *nativePTYRuntimeFixture) Identity() nativecontract.NativeIdentity     { return p.identity }
func (p *nativePTYRuntimeFixture) Frames() <-chan nativecontract.TerminalFrame { return p.frames }
func (p *nativePTYRuntimeFixture) Ready(ctx context.Context) (nativecontract.NativeIdentity, error) {
	if p.readyEntered != nil {
		close(p.readyEntered)
		select {
		case <-ctx.Done():
			return nativecontract.NativeIdentity{}, ctx.Err()
		case <-p.readyRelease:
		}
	}
	return p.identity, p.readyErr
}
func (p *nativePTYRuntimeFixture) Write(_ context.Context, in nativecontract.PTYInput) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes++
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", RequestID: in.RequestID, ObservedAt: p.now}, p.writeErr
}
func (p *nativePTYRuntimeFixture) Resize(_ context.Context, in nativecontract.Resize) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes++
	return nativecontract.NativeEvidence{Stage: "applied", RequestID: in.RequestID, ObservedAt: p.now}, nil
}
func (p *nativePTYRuntimeFixture) finish() {
	p.once.Do(func() {
		p.mu.Lock()
		p.x = nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: p.errorExit == nil, ObservedAt: p.now}
		p.mu.Unlock()
		close(p.frames)
		close(p.done)
	})
}
func (p *nativePTYRuntimeFixture) Close(ctx context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	p.closes++
	p.mu.Unlock()
	if p.closeEntered != nil {
		close(p.closeEntered)
		select {
		case <-ctx.Done():
			return nativecontract.ExitEvidence{}, ctx.Err()
		case <-p.closeRelease:
		}
	}
	p.finish()
	return p.Wait(ctx)
}
func (p *nativePTYRuntimeFixture) Wait(ctx context.Context) (nativecontract.ExitEvidence, error) {
	select {
	case <-ctx.Done():
		return nativecontract.ExitEvidence{}, ctx.Err()
	case <-p.done:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.x, p.errorExit
}
func (p *nativePTYRuntimeFixture) ObserveExit(ctx context.Context) (nativecontract.ExitEvidence, error) {
	select {
	case <-p.done:
		return p.Wait(ctx)
	default:
		return nativecontract.ExitEvidence{}, ErrProcessesUnconfirmed
	}
}
func (p *nativePTYRuntimeFixture) counts() (int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes, p.resizes, p.closes
}

type nativePTYLauncherFixture struct {
	pty   *nativePTYRuntimeFixture
	opens int
}

func (l *nativePTYLauncherFixture) OpenPTY(ctx context.Context, s nativecontract.PTYLaunchSpec) (nativecontract.PTYRuntime, error) {
	if s.Revalidate != nil {
		if err := s.Revalidate(ctx); err != nil {
			return nil, err
		}
	}
	l.opens++
	l.pty.identity = nativecontract.NativeIdentity{Provider: Claude, NativeSessionRef: s.ResumeRef, RuntimeEpoch: s.Binding.RuntimeEpoch, PID: 5500 + l.opens, StartedAt: l.pty.now, ProcessIdentityDigest: strings.Repeat("d", 64)}
	if l.pty.identity.NativeSessionRef == "" {
		l.pty.identity.NativeSessionRef = "observed-fixture-conversation"
	}
	go func() {
		select {
		case <-s.Lifetime.Done():
			l.pty.finish()
		case <-l.pty.done:
		}
	}()
	return l.pty, nil
}

func nativePTYHost(t *testing.T) (*nativeHostFixture, *nativePTYLauncherFixture, *nativePTYAuthorityFixture) {
	t.Helper()
	f := newNativeHostFixture(t, 1)
	f.server.startMode = "terminal"
	l := &nativePTYLauncherFixture{pty: &nativePTYRuntimeFixture{frames: make(chan nativecontract.TerminalFrame, 64), done: make(chan struct{}), now: f.server.now}}
	a := &nativePTYAuthorityFixture{viewer: uuid.NewString(), operations: map[string]string{}}
	a.caller = nativepty.Caller{Local: true, Actor: nativecontract.Actor{TenantID: f.server.tenant, PrincipalID: f.server.owner, PrincipalKind: "person"}, ClientID: uuid.NewString(), Capability: "fixture-control"}
	a.access = nativepty.Access{Controller: nativecontract.ControllerView{ControllerID: uuid.NewString(), ActorID: f.server.owner, ClientID: a.caller.ClientID, Epoch: uuid.NewString(), ExpiresAt: f.server.now.Add(time.Minute)}, Deadline: f.server.now.Add(time.Minute), Revision: 3}
	f.h.config.Terminal, f.h.config.Qualification, f.h.config.PTYAuthority = l, nativePTYQualificationFixture{}, a
	t.Cleanup(func() {
		l.pty.finish()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = f.h.Close(ctx)
	})
	return f, l, a
}
func nativePTYContext(a *nativePTYAuthorityFixture) context.Context {
	return context.WithValue(context.Background(), nativeLocalRequestKey{}, nativeLocalRequest{actor: a.caller.Actor, request: nativestream.LocalRequest{ViewerID: a.viewer, ControlCapability: a.caller.Capability}})
}
func startNativePTY(t *testing.T, f *nativeHostFixture, a *nativePTYAuthorityFixture) nativecontract.Binding {
	t.Helper()
	out, err := f.h.Start(t.Context(), f.local(0))
	if err != nil || out.Session == nil || out.Session.State != "taken" {
		t.Fatal("initial PTY", out, err)
	}
	a.binding = *out.Session.CurrentBinding
	return a.binding
}
func nativePTYResize(a *nativePTYAuthorityFixture) nativecontract.Resize {
	return nativecontract.Resize{RequestID: uuid.NewString(), ExpectedBinding: a.binding, ControllerEpoch: a.access.Controller.Epoch, Cols: 120, Rows: 36, ExpiresAt: a.access.Deadline}
}
func nativePTYInput(a *nativePTYAuthorityFixture) nativecontract.PTYInput {
	return nativecontract.PTYInput{RequestID: uuid.NewString(), ExpectedBinding: a.binding, ControllerEpoch: a.access.Controller.Epoch, Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString([]byte("fixture-input\n")), ExpiresAt: a.access.Deadline}
}
func requireNativeReason(t *testing.T, err error, want nativecontract.Reason) {
	t.Helper()
	var r *nativecontract.ValidationError
	if !errors.As(err, &r) || r.Reason != want {
		t.Fatalf("reason %s: %v", want, err)
	}
}

// Risk: fork success is misreported as readiness, or an initial TUI invents a
// resume prerequisite. The authorized start stays starting behind a barrier.
func TestNativeHostInitialPTYWaitsForProviderReadiness(t *testing.T) {
	f, l, a := nativePTYHost(t)
	l.pty.readyEntered, l.pty.readyRelease = make(chan struct{}), make(chan struct{})
	releaseReady := sync.OnceFunc(func() { close(l.pty.readyRelease) })
	defer releaseReady()
	local := f.local(0)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.h.Start(ctx, local); done <- err }()
	select {
	case <-l.pty.readyEntered:
	case <-ctx.Done():
		t.Fatal("readiness barrier")
	}
	f.server.mu.Lock()
	state := f.server.sessions[local.Intent.RequestID].view.State
	f.server.mu.Unlock()
	if state != "starting" || f.groups(t) != 1 || l.opens != 1 {
		t.Fatal("fork was treated as readiness", state)
	}
	releaseReady()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.server.mu.Lock()
	a.binding = f.server.sessions[local.Intent.RequestID].binding
	f.server.mu.Unlock()
	if f.launcher.opens != 0 || local.NativeResumeRef != nil {
		t.Fatal("initial TUI used the structured launcher or invented resume")
	}
}

func TestNativeHostPTYReadinessFailureRetainsHandleUntilExitReport(t *testing.T) {
	f, l, _ := nativePTYHost(t)
	l.pty.readyErr = nativeRefusal(nativecontract.ReasonQualificationMissing)
	_, err := f.h.Start(t.Context(), f.local(0))
	requireNativeReason(t, err, nativecontract.ReasonQualificationMissing)
	if l.opens != 1 || f.groups(t) != 1 {
		t.Fatal("readiness failure lost possible owner")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := l.pty.Wait(ctx); err != nil {
		t.Fatal("owner lifetime cleanup", err)
	}
	if err := f.h.Poll(ctx); err != nil {
		t.Fatal("exit reconciliation", err)
	}
	if f.groups(t) != 0 {
		t.Fatal("confirmed and reported exit retained local occupancy")
	}
}

// Risk: stale, viewer or agent selectors reach the terminal, unchanged resize
// storms issue ioctls, or a sequence retry repeats an input write.
func TestNativeHostPTYCurrentHolderInputAndResize(t *testing.T) {
	f, l, a := nativePTYHost(t)
	startNativePTY(t, f, a)
	ctx := nativePTYContext(a)
	r := nativePTYResize(a)
	if _, err := f.h.Resize(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.RequestID = uuid.NewString()
	if _, err := f.h.Resize(ctx, r); err != nil {
		t.Fatal(err)
	}
	in := nativePTYInput(a)
	out, err := f.h.PTYInput(ctx, in)
	if err != nil || out.Receipt.State != "write_acknowledged" {
		t.Fatal(out, err)
	}
	in.RequestID = uuid.NewString()
	_, err = f.h.PTYInput(ctx, in)
	requireNativeReason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	if writes, resizes, _ := l.pty.counts(); writes != 1 || resizes != 1 {
		t.Fatal("wrong PTY effects", writes, resizes)
	}
}

// Risk: normal typing fills the shared journal/evidence map and prevents the
// person's Stop. More than a journal's capacity must keep writing and stop once.
func TestNativeHostPTYTypingRetiresReportsAndPreservesStop(t *testing.T) {
	f, l, a := nativePTYHost(t)
	a.access.Deadline = f.server.now.Add(10 * time.Minute)
	a.access.Controller.ExpiresAt = a.access.Deadline
	b := startNativePTY(t, f, a)
	ctx := nativePTYContext(a)
	for i := 1; i <= nativereceipts.MaxReceipts+8; i++ {
		r := nativePTYResize(a)
		r.Cols += int64(i % 2)
		if _, err := f.h.Resize(ctx, r); err != nil {
			t.Fatalf("resize %d: %v", i, err)
		}
		in := nativePTYInput(a)
		in.Sequence = strconv.Itoa(i)
		if out, err := f.h.PTYInput(ctx, in); err != nil || out.Receipt.State != "write_acknowledged" {
			t.Fatalf("input %d: %+v %v", i, out, err)
		}
	}
	f.h.mu.Lock()
	var e *nativeOwned
	for _, candidate := range f.h.entries {
		e = candidate
	}
	f.h.mu.Unlock()
	page, err := e.journal.Page(b, "", 100)
	e.mu.Lock()
	observations := len(e.observations)
	e.mu.Unlock()
	if err != nil || page.Truncated || len(page.Items) > 32 || observations > 32 {
		t.Fatal("settled PTY metadata grew without bound", len(page.Items), observations, err)
	}
	stop := nativecontract.Stop{RequestID: uuid.NewString(), ExpectedBinding: b, Kind: "graceful", ExpectedProcessIdentityDigest: l.pty.identity.ProcessIdentityDigest, ExpiresAt: a.access.Deadline}
	f.server.queue(b, "stop", stop.RequestID, stop)
	if out, err := f.h.Stop(t.Context(), stop); err != nil || out.Receipt.State != "exit_confirmed" {
		t.Fatal("typing prevented owner stop", out, err)
	}
	if writes, resizes, closes := l.pty.counts(); writes != nativereceipts.MaxReceipts+8 || resizes != nativereceipts.MaxReceipts+8 || closes != 1 || f.groups(t) != 0 {
		t.Fatal("wrong PTY effects or retained occupancy", writes, resizes, closes)
	}
}

// Risk: a holder check that refuses before native I/O is mislabeled uncertain
// and permanently faults a healthy process. Reacquiring control must recover.
func TestNativeHostPTYPreIORefusalDoesNotFaultSession(t *testing.T) {
	for _, kind := range []string{"before first resize", "expired holder"} {
		t.Run(kind, func(t *testing.T) {
			f, l, a := nativePTYHost(t)
			var clock atomic.Int64
			clock.Store(f.server.now.UnixNano())
			f.s.capacityNow = func() time.Time { return time.Unix(0, clock.Load()) }
			startNativePTY(t, f, a)
			ctx := nativePTYContext(a)
			if kind == "expired holder" {
				if _, err := f.h.Resize(ctx, nativePTYResize(a)); err != nil {
					t.Fatal(err)
				}
				clock.Store(f.server.now.Add(90 * time.Second).UnixNano())
				a.access.Deadline = f.server.now.Add(3 * time.Minute)
				a.access.Controller.ExpiresAt = a.access.Deadline
			}
			in := nativePTYInput(a)
			out, err := f.h.PTYInput(ctx, in)
			requireNativeReason(t, err, nativecontract.ReasonControllerExpired)
			if out.Receipt.State != "rejected" || out.Receipt.Reason == nil || *out.Receipt.Reason != nativecontract.ReasonControllerExpired {
				t.Fatal("pre-I/O refusal reported a possible write", out)
			}
			if writes, _, _ := l.pty.counts(); writes != 0 {
				t.Fatal("refused input reached the PTY")
			}
			if _, err := f.h.Resize(ctx, nativePTYResize(a)); err != nil {
				t.Fatal("pre-I/O refusal faulted resize", err)
			}
			in.RequestID = uuid.NewString()
			if out, err := f.h.PTYInput(ctx, in); err != nil || out.Receipt.State != "write_acknowledged" {
				t.Fatal("pre-I/O refusal consumed the sequence or faulted input", out, err)
			}
		})
	}
}

func TestNativeHostPTYRejectsUntrustedOrStaleWrites(t *testing.T) {
	for _, kind := range []string{"missing proof", "agent key", "viewer", "stale binding", "stale epoch", "revoked grant", "revoked profile", "replaced daemon", "size bounds"} {
		t.Run(kind, func(t *testing.T) {
			f, l, a := nativePTYHost(t)
			startNativePTY(t, f, a)
			ctx := nativePTYContext(a)
			in := nativePTYResize(a)
			want := nativecontract.ReasonGrantMissing
			switch kind {
			case "missing proof":
				ctx = context.Background()
				want = nativecontract.ReasonWrongPrincipal
			case "agent key":
				r := ctx.Value(nativeLocalRequestKey{}).(nativeLocalRequest)
				r.actor.PrincipalKind = "agent"
				ctx = context.WithValue(ctx, nativeLocalRequestKey{}, r)
				want = nativecontract.ReasonWrongPrincipal
			case "viewer":
				r := ctx.Value(nativeLocalRequestKey{}).(nativeLocalRequest)
				r.request.ControlCapability = ""
				ctx = context.WithValue(ctx, nativeLocalRequestKey{}, r)
			case "stale binding":
				in.ExpectedBinding.BindingRevision++
				want = ""
			case "stale epoch":
				in.ControllerEpoch = uuid.NewString()
				want = nativecontract.ReasonControllerConflict
			case "revoked grant":
				a.deny = nativeRefusal(nativecontract.ReasonPermissionRevoked)
				want = nativecontract.ReasonPermissionRevoked
			case "revoked profile":
				f.h.profiles = nativePTYRevokedProfile{}
				want = nativecontract.ReasonIdentityMismatch
			case "replaced daemon":
				f.s.generation = uuid.NewString()
				want = nativecontract.ReasonWrongGeneration
			case "size bounds":
				in.Cols = 321
				want = nativecontract.ReasonInvalidInput
			}
			_, err := f.h.Resize(ctx, in)
			if want == "" {
				if !errors.Is(err, ErrNotOwned) {
					t.Fatal(err)
				}
			} else {
				requireNativeReason(t, err, want)
			}
			if writes, resizes, _ := l.pty.counts(); writes != 0 || resizes != 0 {
				t.Fatal("denied operation touched PTY")
			}
		})
	}
}

// Risk: a partial/uncertain input is injected again after reconnect.
func TestNativeHostPTYUncertainInputRequiresRecovery(t *testing.T) {
	f, l, a := nativePTYHost(t)
	startNativePTY(t, f, a)
	ctx := nativePTYContext(a)
	if _, err := f.h.Resize(ctx, nativePTYResize(a)); err != nil {
		t.Fatal(err)
	}
	l.pty.writeErr = errors.New("fixture partial write")
	in := nativePTYInput(a)
	out, err := f.h.PTYInput(ctx, in)
	if !errors.Is(err, l.pty.writeErr) || out.Receipt.State != "uncertain" {
		t.Fatal(out, err)
	}
	in.RequestID, in.Sequence = uuid.NewString(), "2"
	_, err = f.h.PTYInput(ctx, in)
	requireNativeReason(t, err, nativecontract.ReasonInputOutcomeUnconfirmed)
	if writes, _, _ := l.pty.counts(); writes != 1 || f.groups(t) != 1 {
		t.Fatal("uncertain input was replayed or occupancy freed")
	}
}

// Risk: a grant revoked after server preparation but before the final native
// fence still permits a write. The barrier establishes that exact ordering.
func TestNativeHostPTYRevalidatesAfterPreparationBeforeIO(t *testing.T) {
	f, l, a := nativePTYHost(t)
	startNativePTY(t, f, a)
	ctx := nativePTYContext(a)
	if _, err := f.h.Resize(ctx, nativePTYResize(a)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseFence := sync.OnceFunc(func() { close(release) })
	defer releaseFence()
	a.before = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, err := f.h.PTYInput(ctx, nativePTYInput(a)); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("final write fence barrier")
	}
	a.mu.Lock()
	a.deny = nativeRefusal(nativecontract.ReasonPermissionRevoked)
	a.mu.Unlock()
	releaseFence()
	requireNativeReason(t, <-done, nativecontract.ReasonPermissionRevoked)
	if writes, _, _ := l.pty.counts(); writes != 0 {
		t.Fatal("revoked grant reached stdin")
	}
}

func enableNativePTYRelay(t *testing.T, f *nativeHostFixture) {
	t.Helper()
	p, err := nativestream.NewProjector(nativestream.ExportPolicy{Text: func(_ context.Context, _ nativecontract.Binding, s string) (string, error) { return s, nil }, Terminal: func(context.Context, nativecontract.Binding, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	f.h.config.Live, err = stream.NewRelay(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.h.caps.TerminalView = true
	f.h.caps.EventsSupported = true
	f.server.eventsSupported = true
}
func subscribeNativePTY(t *testing.T, f *nativeHostFixture, b nativecontract.Binding, after ...string) nativecontract.Subscription {
	t.Helper()
	f.server.mu.Lock()
	view := f.server.byOwned(b.OwnedSessionID).view
	f.server.mu.Unlock()
	view.Capabilities.TerminalView = true
	fence := func(_ context.Context, fn func(nativecontract.ValidationContext) error) error {
		return fn(nativecontract.ValidationContext{Binding: &b, Generation: view.Generation, PrincipalKind: "person", Capabilities: &view.Capabilities, View: "terminal", Mode: "terminal", State: "taken"})
	}
	cursor := ""
	if len(after) > 0 {
		cursor = after[0]
	}
	sub, err := f.h.config.Live.SubscribeView(t.Context(), b, stream.Viewer{Query: nativecontract.StreamQuery{Generation: view.Generation, View: "terminal", After: cursor}, Fence: fence})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}
func readNativePTYEvent(t *testing.T, sub nativecontract.Subscription) nativecontract.StreamEvent {
	t.Helper()
	select {
	case event, ok := <-sub.Events():
		if !ok {
			t.Fatal("terminal stream ended before witness")
		}
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("terminal stream witness barrier")
		return nativecontract.StreamEvent{}
	}
}

// Risk: gaps are hidden or a slow viewer blocks the owner and grows memory.
func TestNativeHostPTYSourceGapsAndSlowViewerStayBounded(t *testing.T) {
	for _, kind := range []string{"source gap", "slow viewer"} {
		t.Run(kind, func(t *testing.T) {
			f, l, a := nativePTYHost(t)
			enableNativePTYRelay(t, f)
			b := startNativePTY(t, f, a)
			slow := subscribeNativePTY(t, f, b)
			witness := subscribeNativePTY(t, f, b)
			readNativePTYEvent(t, witness)
			if kind == "source gap" {
				l.pty.frames <- nativecontract.TerminalFrame{TerminalEpoch: b.RuntimeEpoch, Sequence: "3", DataBase64: base64.StdEncoding.EncodeToString([]byte("gap witness")), Cols: 120, Rows: 36}
				event := readNativePTYEvent(t, witness)
				var gap nativecontract.Gap
				if event.Kind != "gap" || json.Unmarshal(event.Payload, &gap) != nil || gap.Reason != "frame_omitted" || gap.DroppedFrames != "2" {
					t.Fatal("missing explicit source gap", event)
				}
				return
			}
			for i := 1; i <= nativecontract.LiveReplayFrames+2; i++ {
				l.pty.frames <- nativecontract.TerminalFrame{TerminalEpoch: b.RuntimeEpoch, Sequence: strconv.Itoa(i), DataBase64: base64.StdEncoding.EncodeToString([]byte("bounded output")), Cols: 120, Rows: 36}
				if event := readNativePTYEvent(t, witness); event.Kind != "terminal" {
					t.Fatal("fast witness lost output", event.Kind)
				}
			}
			frames, bytes, _, err := f.h.config.Live.Stats(b)
			if err != nil || frames > nativecontract.LiveReplayFrames || bytes > nativecontract.LiveReplayBytes {
				t.Fatal("unbounded relay", frames, bytes, err)
			}
			if event := readNativePTYEvent(t, slow); event.Kind != "snapshot" {
				t.Fatal("slow viewer snapshot", event.Kind)
			}
			event := readNativePTYEvent(t, slow)
			var gap nativecontract.Gap
			if event.Kind != "gap" || json.Unmarshal(event.Payload, &gap) != nil || gap.Reason != "buffer_evicted" || gap.DroppedFrames == "0" {
				t.Fatal("slow viewer loss was hidden", event)
			}
		})
	}
}

// Risk: transient per-frame authorization/export failure kills the person's
// TUI. HTTP barriers prove output continues draining during the outage, then
// resumes with an explicit loss gap; a definitive revocation must still stop.
func TestNativeHostPTYExportOutageKeepsRuntimeAndReportsGap(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f, l, a := nativePTYHost(t)
			enableNativePTYRelay(t, f)
			var outage atomic.Bool
			attempts := make(chan struct{}, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if outage.Load() && r.Method == http.MethodPost && r.URL.Path == "/api/agentd/native-sessions/receipts" {
					nativeHostRefuse(w, status, nativecontract.ReasonPermissionRevoked)
					select {
					case attempts <- struct{}{}:
					default:
					}
					return
				}
				f.server.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			f.h.remote = NewRemote(server.URL, "fixture-runtime")
			b := startNativePTY(t, f, a)
			sub := subscribeNativePTY(t, f, b)
			readNativePTYEvent(t, sub)
			f.h.mu.Lock()
			var e *nativeOwned
			for _, candidate := range f.h.entries {
				e = candidate
			}
			f.h.mu.Unlock()
			frame := func(sequence int) {
				t.Helper()
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatal("export outage closed the owned PTY frame channel", recovered)
					}
				}()
				l.pty.frames <- nativecontract.TerminalFrame{TerminalEpoch: b.RuntimeEpoch, Sequence: strconv.Itoa(sequence), DataBase64: base64.StdEncoding.EncodeToString([]byte("outage-output")), Cols: 120, Rows: 36}
			}
			awaitFailure := func() {
				t.Helper()
				select {
				case <-attempts:
				case <-time.After(10 * time.Second):
					t.Fatal("frame publish did not reach outage barrier")
				}
			}
			outage.Store(true)
			frame(1)
			awaitFailure()
			if status == http.StatusForbidden {
				select {
				case <-l.pty.done:
				case <-time.After(10 * time.Second):
					t.Fatal("definitive revocation did not cancel the owner")
				}
				if e.faulted() == nil {
					t.Fatal("definitive refusal was not retained")
				}
				return
			}
			frame(2)
			awaitFailure() // processing the second frame proves the first failed
			outage.Store(false)
			frame(3)
			event := readNativePTYEvent(t, sub)
			var gap nativecontract.Gap
			if event.Kind != "gap" || json.Unmarshal(event.Payload, &gap) != nil || gap.Reason != "frame_omitted" || gap.DroppedFrames != "2" {
				t.Fatal("outage loss was hidden", event)
			}
			select {
			case _, ok := <-sub.Events():
				if ok {
					t.Fatal("gap did not require a fresh subscription")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("gap did not close the stream")
			}
			after, err := nativecontract.EventID(event.Generation, 2)
			if err != nil {
				t.Fatal(err)
			}
			reconnected := subscribeNativePTY(t, f, b, after)
			if event := readNativePTYEvent(t, reconnected); event.Kind != "snapshot" {
				t.Fatal("reconnect did not re-establish the authorized snapshot", event)
			}
			if event := readNativePTYEvent(t, reconnected); event.Kind != "terminal" || event.Seq != "3" {
				t.Fatal("terminal export did not recover", event)
			}
			select {
			case <-l.pty.done:
				t.Fatal("transient export error canceled the person's PTY")
			default:
			}
			if e.faulted() != nil || e.terminalEvidence().Frames != 3 || e.terminalEvidence().OutputBytes != 3*uint64(len("outage-output")) || f.groups(t) != 1 {
				t.Fatal("outage faulted the owner or stopped counting/draining", e.faulted(), e.terminalEvidence())
			}
			if _, err := f.h.Resize(nativePTYContext(a), nativePTYResize(a)); err != nil {
				t.Fatal("outage faulted control", err)
			}
			if _, err := f.h.PTYInput(nativePTYContext(a), nativePTYInput(a)); err != nil {
				t.Fatal("outage faulted input", err)
			}
		})
	}
}

// Risk: output stops draining without watch/export, natural EOF leaks the
// owner, or lifecycle persistence stores raw terminal bytes as history.
func TestNativeHostPTYOutputEOFAndLifecycleMetadata(t *testing.T) {
	f, l, a := nativePTYHost(t)
	enableNativePTYRelay(t, f)
	b := startNativePTY(t, f, a)
	sub := subscribeNativePTY(t, f, b)
	if event := readNativePTYEvent(t, sub); event.Kind != "snapshot" {
		t.Fatal("initial snapshot", event.Kind)
	}
	l.pty.frames <- nativecontract.TerminalFrame{TerminalEpoch: b.RuntimeEpoch, Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString([]byte("private-terminal")), Cols: 120, Rows: 36}
	if event := readNativePTYEvent(t, sub); event.Kind != "terminal" {
		t.Fatal("terminal output", event.Kind)
	}
	f.h.mu.Lock()
	var e *nativeOwned
	for _, candidate := range f.h.entries {
		e = candidate
	}
	f.h.mu.Unlock()
	l.pty.finish()
	select {
	case <-e.released:
	case <-time.After(10 * time.Second):
		t.Fatal("exit release barrier")
	}
	if f.groups(t) != 0 {
		t.Fatal("confirmed EOF kept local occupancy")
	}
	f.server.mu.Lock()
	evidence := f.server.terminalEvidence[len(f.server.terminalEvidence)-1]
	f.server.mu.Unlock()
	if evidence.OutputBytes != uint64(len("private-terminal")) || evidence.Frames != 1 || evidence.OutputSHA256 != nativeDigest("private-terminal") {
		t.Fatal("wrong lifecycle evidence", evidence)
	}
	raw, _ := json.Marshal(evidence)
	if strings.Contains(string(raw), "private-terminal") || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte("private-terminal"))) {
		t.Fatal("lifecycle persisted content")
	}
	if err := f.h.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Close(t.Context()); err != nil {
		t.Fatal("second cleanup", err)
	}
}

// Risk: either switch opens a second writer before the predecessor root/group
// exit. Each predecessor close is held behind an explicit release barrier.
func TestNativeHostPTYTransitionsWaitForBothOwnedExits(t *testing.T) {
	f, l, a := nativePTYHost(t)
	f.server.startMode = "structured"
	f.launcher.now = f.server.now.Add(-2 * time.Second)
	l.pty.now = f.server.now.Add(-time.Second)
	b, p := f.start(t, 0)
	p.closeEntered, p.closeRelease = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	take := nativecontract.TakeIntent{RequestID: uuid.NewString(), ExpectedBinding: b, ControllerEpoch: a.access.Controller.Epoch, ExpectedRevision: 3, Cols: 120, Rows: 36, ExpiresAt: a.access.Deadline}
	f.server.queue(b, "take", take.RequestID, take)
	done := make(chan error, 1)
	go func() { _, err := f.h.Take(ctx, take); done <- err }()
	select {
	case <-p.closeEntered:
	case <-ctx.Done():
		t.Fatal("structured exit barrier", ctx.Err())
	}
	if l.opens != 0 || f.groups(t) != 1 {
		t.Fatal("PTY opened or occupancy released before structured exit")
	}
	close(p.closeRelease)
	if err := <-done; err != nil {
		t.Fatal("take", err)
	}
	f.server.mu.Lock()
	fresh := f.server.byOwned(b.OwnedSessionID).binding
	f.server.mu.Unlock()
	if fresh.RuntimeEpoch == b.RuntimeEpoch || l.opens != 1 || !p.exited {
		t.Fatal("take did not rotate exactly once")
	}
	l.pty.closeEntered, l.pty.closeRelease = make(chan struct{}), make(chan struct{})
	f.launcher.now = f.server.now
	back := nativecontract.ReturnIntent{RequestID: uuid.NewString(), ExpectedBinding: fresh, ControllerEpoch: uuid.NewString(), ExpectedRevision: 4, ExpiresAt: a.access.Deadline}
	f.server.queue(fresh, "return", back.RequestID, back)
	go func() { _, err := f.h.Return(ctx, back); done <- err }()
	select {
	case <-l.pty.closeEntered:
	case <-ctx.Done():
		t.Fatal("terminal exit barrier", ctx.Err())
	}
	if f.launcher.opens != 1 || f.groups(t) != 1 {
		t.Fatal("structured successor opened or occupancy released before PTY exit")
	}
	close(l.pty.closeRelease)
	if err := <-done; err != nil {
		t.Fatal("return", err)
	}
	f.server.mu.Lock()
	last := f.server.byOwned(b.OwnedSessionID).binding
	f.server.mu.Unlock()
	if f.launcher.opens != 2 || l.opens != 1 || last.RuntimeEpoch == fresh.RuntimeEpoch {
		t.Fatal("return did not create one structured successor")
	}
	f.launcher.mu.Lock()
	successor := f.launcher.providers[last.RuntimeEpoch]
	f.launcher.mu.Unlock()
	if _, err := f.h.Stop(ctx, f.stop(last, successor)); err != nil {
		t.Fatal("stop successor", err)
	}
}

func TestNativeHostPTYUnconfirmedExitKeepsOccupancy(t *testing.T) {
	f, l, a := nativePTYHost(t)
	b := startNativePTY(t, f, a)
	l.pty.errorExit = ErrProcessesUnconfirmed
	back := nativecontract.ReturnIntent{RequestID: uuid.NewString(), ExpectedBinding: b, ControllerEpoch: a.access.Controller.Epoch, ExpectedRevision: 3, ExpiresAt: a.access.Deadline}
	f.server.queue(b, "return", back.RequestID, back)
	_, err := f.h.Return(t.Context(), back)
	if !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("unconfirmed exit", err)
	}
	if f.launcher.opens != 0 || f.groups(t) != 1 {
		t.Fatal("uncertain writer allowed a successor or released occupancy")
	}
	if err := f.h.Close(t.Context()); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("uncertain exit reported clean shutdown", err)
	}
}

// Risk: a returned handle plus a readiness error retains the predecessor's
// command delivery binding, stranding the exact new owner when stopped.
func TestNativeHostPTYFailedSuccessorStillAcceptsExactOwnerStop(t *testing.T) {
	f, l, a := nativePTYHost(t)
	f.server.startMode = "structured"
	f.launcher.now = f.server.now.Add(-2 * time.Second)
	l.pty.now = f.server.now.Add(-time.Second)
	l.pty.readyErr = nativeRefusal(nativecontract.ReasonQualificationMissing)
	b, _ := f.start(t, 0)
	take := nativecontract.TakeIntent{RequestID: uuid.NewString(), ExpectedBinding: b, ControllerEpoch: a.access.Controller.Epoch, ExpectedRevision: 3, Cols: 120, Rows: 36, ExpiresAt: a.access.Deadline}
	f.server.queue(b, "take", take.RequestID, take)
	_, err := f.h.Take(t.Context(), take)
	requireNativeReason(t, err, nativecontract.ReasonQualificationMissing)
	f.server.mu.Lock()
	fresh := f.server.byOwned(b.OwnedSessionID).binding
	f.server.mu.Unlock()
	if fresh.RuntimeEpoch == b.RuntimeEpoch || f.groups(t) != 1 {
		t.Fatal("failed successor lost its exact hold")
	}
	stop := nativecontract.Stop{RequestID: uuid.NewString(), ExpectedBinding: fresh, Kind: "graceful", ExpectedProcessIdentityDigest: l.pty.Identity().ProcessIdentityDigest, ExpiresAt: a.access.Deadline}
	f.server.queue(fresh, "stop", stop.RequestID, stop)
	if _, err := f.h.Stop(t.Context(), stop); err != nil {
		t.Fatal("stop retained successor", err)
	}
	if f.groups(t) != 0 {
		t.Fatal("stopped failed successor retained its local hold")
	}
	if _, _, closes := l.pty.counts(); closes != 1 {
		t.Fatal("owner stop did not clean up exactly once", closes)
	}
}

func (f *nativePersonAuthorityFixture) Authenticate(*http.Request) (nativecontract.Actor, error) {
	f.calls++
	return f.actor, nil
}
func (f *nativePersonAuthorityFixture) WithAccess(context.Context, nativecontract.Actor, nativestream.LocalRequest, func(nativecontract.ValidationContext) error) error {
	return nativeRefusal(nativecontract.ReasonGrantMissing)
}

// Risk: a protected socket bearer or paired-agent identity is promoted to a
// person. Kernel peer provenance and the independent human verifier are BOTH
// required; read/control authority still needs its separate current fence.
func TestNativeLocalSocketRequiresIndependentPersonProof(t *testing.T) {
	person := nativecontract.Actor{TenantID: uuid.NewString(), PrincipalID: uuid.NewString(), PrincipalKind: "person"}
	fake := &nativePersonAuthorityFixture{actor: person}
	allowed := false
	a := &nativeSocketAuthority{authority: fake, authorize: func(*http.Request) bool { return allowed }}
	req := httptest.NewRequest("POST", "http://agentd/v1/native-sessions/"+uuid.NewString()+"/attach", nil)
	if _, err := a.Authenticate(req); err == nil || fake.calls != 0 {
		t.Fatal("socket denial reached person authority")
	}
	allowed = true
	if _, err := a.Authenticate(req); err == nil || fake.calls != 0 {
		t.Fatal("missing kernel peer reached person authority")
	}
	peer, err := observeAttachProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(context.WithValue(req.Context(), attachPeerKey{}, peer))
	fake.actor.PrincipalKind = "agent"
	_, err = a.Authenticate(req)
	var refusal *nativecontract.ValidationError
	if !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonWrongPrincipal {
		t.Fatal("paired agent became a person", err)
	}
	fake.actor = person
	actor, err := a.Authenticate(req)
	if err != nil || actor.PrincipalID != person.PrincipalID {
		t.Fatal("verified person was lost", err)
	}
	if _, ok := req.Context().Value(nativeLocalRequestKey{}).(nativeLocalRequest); !ok {
		t.Fatal("viewer provenance was not retained")
	}
	called := false
	err = a.WithAccess(req.Context(), actor, nativestream.LocalRequest{}, func(nativecontract.ValidationContext) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("person verification substituted for a current server grant")
	}
}

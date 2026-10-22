// SPDX-License-Identifier: AGPL-3.0-only
package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	guuid "github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/interaction"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type controlTestAuthority struct{ store *Store }

func (a controlTestAuthority) AuthorizeTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, b nativecontract.Binding, _ string) error {
	s, err := a.store.production.consent.SessionTx(ctx, tx, b)
	if err != nil {
		return err
	}
	return stream.TakeAuthorityTx(ctx, tx, p, s.OwnerPersonID, b.ProjectID)
}
func (controlTestAuthority) AuthorizeRequestTx(context.Context, pgx.Tx, tenant.Principal, nativecontract.Approval) (bool, error) {
	return false, errors.New("no effect approval in this fixture")
}

type qualifiedControlFixture struct{}

func (qualifiedControlFixture) Require(context.Context, nativecontract.Actor, nativecontract.LaunchSpec, string) error {
	return nil
}

type controlPTYFixture struct {
	mu                             sync.Mutex
	now                            time.Time
	identity                       nativecontract.NativeIdentity
	frames                         chan nativecontract.TerminalFrame
	closed                         bool
	confirmed                      bool
	opens, closes, writes, resizes int
}

func (p *controlPTYFixture) OpenPTY(_ context.Context, s nativecontract.PTYLaunchSpec) (nativecontract.PTYRuntime, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opens++
	p.identity = nativecontract.NativeIdentity{Provider: "claude", NativeSessionRef: s.ResumeRef, RuntimeEpoch: s.Binding.RuntimeEpoch, PID: 23456, StartedAt: p.now.Add(-8500 * time.Millisecond), ProcessIdentityDigest: strings.Repeat("e", 64)}
	return p, nil
}
func (p *controlPTYFixture) Identity() nativecontract.NativeIdentity {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.identity
}
func (p *controlPTYFixture) Frames() <-chan nativecontract.TerminalFrame { return p.frames }
func (p *controlPTYFixture) Ready(context.Context) (nativecontract.NativeIdentity, error) {
	return p.Identity(), nil
}
func (p *controlPTYFixture) Write(context.Context, nativecontract.PTYInput) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	p.writes++
	p.mu.Unlock()
	return nativecontract.NativeEvidence{}, errors.New("viewer must never write")
}
func (p *controlPTYFixture) Resize(context.Context, nativecontract.Resize) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	p.resizes++
	p.mu.Unlock()
	return nativecontract.NativeEvidence{}, errors.New("viewer must never resize")
}
func (p *controlPTYFixture) Close(context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	p.closed = true
	p.closes++
	p.mu.Unlock()
	return p.Wait(context.Background())
}
func (p *controlPTYFixture) Wait(context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return nativecontract.ExitEvidence{RootExited: p.closed, GroupAbsent: p.closed && p.confirmed, ProcessIdentityDigest: p.identity.ProcessIdentityDigest, ObservedAt: p.now}, nil
}

// Risk: take/hand-back reuse a live writer, borrow the paired key's person
// authority, lose the account hold, or allow an old decision after an epoch
// changes. Real SQL consent/claim/receipt fences surround one fake provider.
func TestOwnedNativeControlConfirmsExitAndRejectsStaleEpoch(t *testing.T) {
	pty := &controlPTYFixture{frames: make(chan nativecontract.TerminalFrame, 4), confirmed: true}
	c := composeProduction(t, compositionOptions{disableRetention: true, configure: func(f *startFixture, a *consent.Authority, config *agentd.NativeSessionConfig) {
		a.Capabilities.Take = true
		a.Capabilities.TakeSupported = true
		a.Capabilities.TerminalView = true
		a.Capabilities.EventsSupported = true
		if config != nil {
			pty.now = f.now
			config.Terminal = pty
			config.Qualification = qualifiedControlFixture{}
			projector, err := nativestream.NewProjector(nativestream.ExportPolicy{Text: func(_ context.Context, _ nativecontract.Binding, s string) (string, error) { return s, nil }, Terminal: func(context.Context, nativecontract.Binding, []byte) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			config.Live, err = stream.NewRelay(projector, nil)
			if err != nil {
				t.Fatal(err)
			}
		}
	}})
	f := c.f
	c.provider.age = 10 * time.Second
	f.begin(t)
	started, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
	if err != nil || started.Session == nil {
		t.Fatal("start", err)
	}
	b := *started.Session.CurrentBinding
	input := nativecontract.Input{RequestID: guuid.NewString(), InputID: guuid.NewString(), ExpectedBinding: b, Mode: "now", Text: "one structured instruction", ExpiresAt: f.now.Add(time.Minute)}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "input", Input: &input}); err != nil {
		t.Fatal(err)
	}
	if out, err := c.config.Host.Input(t.Context(), input); err != nil || out.Receipt.State != "write_acknowledged" {
		t.Fatal("input", out, err)
	}
	turn := guuid.NewString()
	c.provider.turn("turn_started", turn, 2)
	select {
	case <-c.turnStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("turn start barrier")
	}
	c.provider.turn("turn_completed", turn, 3)
	select {
	case <-c.turnDone:
	case <-time.After(10 * time.Second):
		t.Fatal("turn completion barrier")
	}
	if err = c.config.Poll(t.Context()); err != nil {
		t.Fatal("settle completed input", err)
	}
	controls, err := interaction.New(f.d.App, interaction.Options{Authority: controlTestAuthority{f.s}, Clock: func(context.Context, pgx.Tx) (time.Time, error) { return f.now, nil }})
	if err != nil {
		t.Fatal(err)
	}
	attach := func(binding nativecontract.Binding) nativecontract.AttachResult {
		t.Helper()
		out, err := controls.Attach(f.personCtx(t), f.actor, nativecontract.AttachIntent{RequestID: guuid.NewString(), ExpectedBinding: binding, ClientID: guuid.NewString(), Mode: "control", View: "structured"})
		if err != nil {
			t.Fatal("attach controller", err)
		}
		return out
	}
	held := attach(b)
	take := nativecontract.TakeIntent{RequestID: guuid.NewString(), ExpectedBinding: b, ControllerEpoch: held.Controller.Epoch, ExpectedRevision: held.Snapshot.Revision, Rows: 36, Cols: 120, ExpiresAt: f.now.Add(20 * time.Second)}
	for _, principal := range []tenant.Principal{f.agent, func() tenant.Principal { p := f.person; p.AuthKeyID = f.agent.KeyID; return p }()} {
		actor := nativecontract.Actor{TenantID: principal.TenantID, PrincipalID: principal.ID, PrincipalKind: string(principal.Kind), KeyCeiling: principal.Scopes}
		_, err := f.s.Mutate(tenant.WithPrincipal(t.Context(), principal), actor, nativecontract.Command{Kind: "take", Take: &take})
		assertNativeReason(t, err, nativecontract.ReasonWrongPrincipal)
	}
	accepted, err := f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "take", Take: &take})
	if err != nil {
		t.Fatal("queue take", err)
	}
	c.provider.confirmed = true
	taken, err := c.config.Host.Take(t.Context(), take)
	if err != nil || taken.Session != nil || taken.Receipt.State != "applied" || taken.Receipt.Binding == nil || !taken.Receipt.Binding.Equal(b) {
		t.Fatal("take", taken, err)
	}
	takenView, err := f.s.View(f.personCtx(t), f.actor, b.ProjectID, b.OwnedSessionID)
	if err != nil || takenView.State != "taken" {
		t.Fatal("fresh take projection", err)
	}
	taken.Session = &takenView
	takenBinding := *taken.Session.CurrentBinding
	if takenBinding.RuntimeEpoch == b.RuntimeEpoch || takenBinding.HarnessSessionID == b.HarnessSessionID || takenBinding.BindingRevision <= b.BindingRevision || pty.opens != 1 {
		t.Fatal("take did not make one fresh writer")
	}
	// A trusted fake watch grant is independent of control consent. The live
	// viewer consumes actual PTY frames and has no input/resize callback.
	view := *taken.Session
	view.Capabilities.TerminalView = true
	fence := func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
		current, err := f.s.Read(f.personCtx(t), f.actor, takenBinding)
		if err != nil {
			return err
		}
		if current.State != "taken" {
			return nativeRefuse(nativecontract.ReasonWrongGeneration)
		}
		return deliver(nativecontract.ValidationContext{Binding: &takenBinding, OwnedSessionID: takenBinding.OwnedSessionID, Generation: view.Generation, PrincipalKind: "person", Capabilities: &view.Capabilities, View: "terminal", Mode: "terminal", State: "taken"})
	}
	sub, err := c.config.Live.SubscribeView(t.Context(), takenBinding, stream.Viewer{Query: nativecontract.StreamQuery{View: "terminal", Generation: view.Generation}, Fence: fence})
	if err != nil {
		t.Fatal("read-only live terminal", err)
	}
	defer sub.Close()
	select {
	case event := <-sub.Events():
		if event.Kind != "snapshot" {
			t.Fatal(event.Kind)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot barrier")
	}
	pty.frames <- nativecontract.TerminalFrame{TerminalEpoch: takenBinding.RuntimeEpoch, Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString([]byte("live owned terminal")), Rows: 36, Cols: 120}
	select {
	case event := <-sub.Events():
		if event.Kind != "terminal" {
			t.Fatal(event.Kind)
		}
		var frame nativecontract.TerminalFrame
		if json.Unmarshal(event.Payload, &frame) != nil || frame.DataBase64 != base64.StdEncoding.EncodeToString([]byte("live owned terminal")) {
			t.Fatal("terminal content changed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("terminal frame barrier")
	}
	if pty.writes != 0 || pty.resizes != 0 || pty.closes != 0 {
		t.Fatal("viewer performed native I/O")
	}
	if _, err = c.config.Host.Take(t.Context(), take); !errors.Is(err, agentd.ErrNotOwned) {
		t.Fatal("old take did not refuse the retired binding", err)
	}
	hand := attach(takenBinding)
	back := nativecontract.ReturnIntent{RequestID: guuid.NewString(), ExpectedBinding: takenBinding, ControllerEpoch: hand.Controller.Epoch, ExpectedRevision: hand.Snapshot.Revision, ExpiresAt: f.now.Add(20 * time.Second)}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "return", Return: &back}); err != nil {
		t.Fatal("queue hand-back", err)
	}
	returned, err := c.config.Host.Return(t.Context(), back)
	if err != nil || returned.Session != nil || returned.Receipt.State != "applied" || returned.Receipt.Binding == nil || !returned.Receipt.Binding.Equal(takenBinding) {
		t.Fatal("hand-back", returned, err)
	}
	returnedView, err := f.s.View(f.personCtx(t), f.actor, b.ProjectID, b.OwnedSessionID)
	if err != nil || returnedView.State != "idle" {
		t.Fatal("fresh hand-back projection", err)
	}
	returned.Session = &returnedView
	fresh := *returned.Session.CurrentBinding
	if fresh.RuntimeEpoch == takenBinding.RuntimeEpoch || fresh.HarnessSessionID == takenBinding.HarnessSessionID || pty.closes != 1 {
		t.Fatal("hand-back skipped exit/fresh epoch")
	}
	_, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "return", Return: &back})
	assertNativeReason(t, err, nativecontract.ReasonWrongGeneration)
	var audit, roots int
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='native_session.mode_changed'`, f.person.TenantID).Scan(&audit); err != nil || audit != 2 {
		t.Fatal("take and hand-back must each emit one audit line", audit, err)
	}
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM native_session_roots WHERE owned_session_id=$1 AND runtime_epoch=$2 AND released_at IS NULL`, fresh.OwnedSessionID, fresh.RuntimeEpoch).Scan(&roots); err != nil || roots != 1 {
		t.Fatal("root hold lost", roots, err)
	}
	if accepted.Receipt.OperationID == returned.Receipt.OperationID {
		t.Fatal("different transitions reused operation")
	}
	// D4 permits aggregate lifecycle evidence, never persisted screen content.
	// The final PTY summary is recorded in the authorized successor admission
	// transaction against the exact exited predecessor epoch.
	var metadata []byte
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT after->'predecessor_terminal' FROM events WHERE tenant_id=$1 AND after->'predecessor_terminal'->>'runtime_epoch'=$2`, f.person.TenantID, takenBinding.RuntimeEpoch).Scan(&metadata); err != nil {
		t.Fatal("terminal lifecycle evidence", err)
	}
	var evidence daemon.TerminalEvidence
	hash := sha256.Sum256([]byte("live owned terminal"))
	if err := json.Unmarshal(metadata, &evidence); err != nil || evidence.OutputBytes != uint64(len("live owned terminal")) || evidence.Frames != 1 || evidence.OutputSHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("terminal counts/hash", string(metadata), err)
	}
	if strings.Contains(string(metadata), "live owned terminal") || strings.Contains(string(metadata), base64.StdEncoding.EncodeToString([]byte("live owned terminal"))) {
		t.Fatal("terminal content persisted")
	}
	// An explicit provider-exit observation drives P5 manual recovery. The
	// retained handle, existing ledger and journal are checked; no saved PID
	// is adopted and no structured input/decision is reconstructed.
	c.provider.mu.Lock()
	c.provider.closed, c.provider.confirmed = true, true
	c.provider.mu.Unlock()
	resume := nativecontract.ResumeIntent{RequestID: guuid.NewString(), ExpectedBinding: fresh, ExpectedRevision: returned.Session.Revision, Reason: "person_continue", PolicyID: f.intent.PolicyID}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "resume", Resume: &resume}); err != nil {
		t.Fatal("authorize explicit recovery", err)
	}
	recovered, err := c.config.Host.Resume(t.Context(), resume)
	if err != nil || recovered.Session != nil || recovered.Receipt.State != "applied" || recovered.Receipt.Binding == nil || !recovered.Receipt.Binding.Equal(fresh) {
		t.Fatal("owned recovery", recovered, err)
	}
	recoveredView, err := f.s.View(f.personCtx(t), f.actor, b.ProjectID, b.OwnedSessionID)
	if err != nil || recoveredView.State != "idle" {
		t.Fatal("fresh recovery projection", err)
	}
	recovered.Session = &recoveredView
	if recovered.Session.CurrentBinding.RuntimeEpoch == fresh.RuntimeEpoch || recovered.Session.CurrentBinding.OwnedSessionID != fresh.OwnedSessionID {
		t.Fatal("recovery did not preserve conversation and advance epoch")
	}
	opens := c.provider.opens
	if _, err = c.config.Host.Resume(t.Context(), resume); err != nil || c.provider.opens != opens {
		t.Fatal("completed recovery was not an exact no-fork replay", err)
	}
	stale := resume
	stale.RequestID = guuid.NewString()
	_, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "resume", Resume: &stale})
	assertNativeReason(t, err, nativecontract.ReasonWrongGeneration)
	var recoveryAudit int
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='native_session.recovered'`, f.person.TenantID).Scan(&recoveryAudit); err != nil || recoveryAudit != 1 {
		t.Fatal("recovery audit", recoveryAudit, err)
	}
}

func assertNativeReason(t *testing.T, err error, want nativecontract.Reason) {
	t.Helper()
	var reason *nativecontract.ValidationError
	if !errors.As(err, &reason) || reason.Reason != want {
		t.Fatalf("got %T %v; want %s", err, err, want)
	}
}
func nativeRefuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

// Risk: an uncertain native close is retried, or its root/account reservation
// is released and a second writer opens. No elapsed-time assertion is used.
func TestOwnedNativeControlRetainsUnconfirmedWriter(t *testing.T) {
	pty := &controlPTYFixture{frames: make(chan nativecontract.TerminalFrame, 1)}
	c := composeProduction(t, compositionOptions{disableRetention: true, configure: func(f *startFixture, a *consent.Authority, config *agentd.NativeSessionConfig) {
		a.Capabilities.Take = true
		a.Capabilities.TakeSupported = true
		if config != nil {
			pty.now = f.now
			config.Terminal = pty
			config.Qualification = qualifiedControlFixture{}
		}
	}})
	f := c.f
	f.begin(t)
	started, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
	if err != nil {
		t.Fatal(err)
	}
	b := *started.Session.CurrentBinding
	controls, err := interaction.New(f.d.App, interaction.Options{Authority: controlTestAuthority{f.s}, Clock: func(context.Context, pgx.Tx) (time.Time, error) { return f.now, nil }})
	if err != nil {
		t.Fatal(err)
	}
	held, err := controls.Attach(f.personCtx(t), f.actor, nativecontract.AttachIntent{RequestID: guuid.NewString(), ExpectedBinding: b, ClientID: guuid.NewString(), Mode: "control", View: "structured"})
	if err != nil {
		t.Fatal(err)
	}
	in := nativecontract.TakeIntent{RequestID: guuid.NewString(), ExpectedBinding: b, ControllerEpoch: held.Controller.Epoch, ExpectedRevision: held.Snapshot.Revision, Rows: 36, Cols: 120, ExpiresAt: f.now.Add(20 * time.Second)}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "take", Take: &in}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.config.Host.Take(t.Context(), in); !errors.Is(err, agentd.ErrProcessesUnconfirmed) {
		t.Fatalf("unconfirmed group exit: %v", err)
	}
	_, err = c.config.Host.Take(t.Context(), in)
	assertNativeReason(t, err, nativecontract.ReasonOwnershipUnconfirmed)
	if c.provider.closes != 1 || pty.opens != 0 {
		t.Fatal("an uncertain switch repeated a close or opened a successor", c.provider.closes, pty.opens)
	}
	current, err := f.s.Read(f.personCtx(t), f.actor, b)
	if err != nil || !current.CurrentBinding.Equal(b) {
		t.Fatal("unconfirmed exit advanced binding", current, err)
	}
	var roots int
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM native_session_roots WHERE owned_session_id=$1 AND runtime_epoch=$2 AND released_at IS NULL`, b.OwnedSessionID, b.RuntimeEpoch).Scan(&roots); err != nil || roots != 1 {
		t.Fatal("uncertain writer lost occupancy", roots, err)
	}
	// The failed transition must not prevent a separately authorized owner
	// stop from reaching the retained handle through normal daemon polling.
	c.provider.mu.Lock()
	c.provider.confirmed = true
	c.provider.mu.Unlock()
	stop := nativecontract.Stop{RequestID: guuid.NewString(), ExpectedBinding: b, Kind: "graceful", ExpectedProcessIdentityDigest: c.provider.Identity().ProcessIdentityDigest, ExpiresAt: f.now.Add(20 * time.Second)}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "stop", Stop: &stop}); err != nil {
		t.Fatal("authorize stop after failed take", err)
	}
	if err = c.config.Poll(t.Context()); err != nil {
		t.Fatal("poll owner stop after failed take", err)
	}
	current, err = f.s.Read(f.personCtx(t), f.actor, b)
	if err != nil || current.State != "stopped" {
		t.Fatal("owner stop did not settle the retained writer", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeintegration/delivery"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
)

// Only content-free transition metadata enters the existing possible-fork
// journal. Handles, native resume references and worker proof remain in RAM.
type nativeTransitionRecord struct {
	Operation, Kind, Phase string
	Source                 nativecontract.Binding
	Successor              *nativecontract.Binding `json:",omitempty"`
}

type nativeTransition struct {
	record   nativeTransitionRecord
	previous daemon.Proof
	next     daemon.Proof
	claim    nativecontract.Claim
	grant    nativecontract.ClaimResult
	command  nativecontract.ClaimResult
	identity nativecontract.NativeIdentity
}

func (e *nativeOwned) startEvents() {
	ctx, cancel := context.WithCancel(e.host.s.lifetime)
	e.mu.Lock()
	e.eventCancel = cancel
	done := make(chan struct{})
	e.eventDone = done
	e.mu.Unlock()
	go func() { defer close(done); e.events(ctx) }()
}

func (e *nativeOwned) stopEvents(ctx context.Context) error {
	e.mu.Lock()
	cancel, done := e.eventCancel, e.eventDone
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return e.flushEvents(ctx)
}

func (e *nativeOwned) successorClaim(ctx context.Context, out *nativecontract.ClaimResult) error {
	t := e.transition
	old, err := daemon.Encode(t.previous)
	if err != nil {
		return err
	}
	next, err := daemon.Encode(t.next)
	if err != nil {
		return err
	}
	return e.host.remote.Client.DoWithHeaders(ctx, http.MethodPost, "/api/agentd/native-sessions/claim", t.claim, out, map[string]string{daemon.ProofHeader: old, "X-Aeon-Native-Successor-Proof": next})
}

func (e *nativeOwned) revalidateSuccessor(ctx context.Context) error {
	t := e.transition
	if t == nil || t.grant.ExpectedBinding == nil || !e.host.s.capacityNow().Before(e.claimDeadline) {
		return nativeRefusal(nativecontract.ReasonGrantExpired)
	}
	var current nativecontract.ClaimResult
	if err := e.successorClaim(ctx, &current); err != nil {
		return err
	}
	if !current.LaunchAuthorized || current.ClaimKind != "resume" || current.ExpectedBinding == nil || !current.ExpectedBinding.Equal(*t.grant.ExpectedBinding) || !reflect.DeepEqual(current.LaunchConfig, t.grant.LaunchConfig) || !sameNativePolicy(current.Policy, t.grant.Policy) || current.AuthorizationTTLMS <= 0 {
		return nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	return nil
}

func sameNativePolicy(a, b nativecontract.Policy) bool {
	if !a.ExpiresAt.Equal(b.ExpiresAt) {
		return false
	}
	a.ExpiresAt, b.ExpiresAt = time.Time{}, time.Time{}
	return a == b
}

// switchRuntime runs under the same per-owner command lock as input and stop.
// The dispatch lock protects local account reservation and fork as one unit.
// All server calls finish before provider I/O; no database lock spans a close
// or launch. Every ambiguous phase remains journalled and keeps occupancy.
func (e *nativeOwned) switchRuntime(ctx context.Context, g nativecontract.ClaimResult) (out nativecontract.OperationResult, err error) {
	h, s := e.host, e.host.s
	if h.config.Terminal == nil || h.config.Qualification == nil {
		return out, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	if e.transition != nil {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err = s.dispatchMu.LockContext(ctx); err != nil {
		return out, err
	}
	defer s.dispatchMu.Unlock()
	if !s.dispatchAllowed(e.local.Intent.AccountID) || !s.accountAvailable(e.local.Intent.AccountID) {
		return out, ErrDraining
	}
	e.mu.Lock()
	old := e.transport
	busy := len(e.operations) > 0 || len(e.drains) > 0
	e.active = g
	e.mu.Unlock()
	if old == nil || busy {
		return out, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	i := old.Identity()
	mode := "terminal"
	if g.Command.Kind == "return" {
		mode = "structured"
	}
	if i.NativeSessionRef == "" || i.RuntimeEpoch != g.ExpectedBinding.RuntimeEpoch {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	actor := nativecontract.Actor{TenantID: g.CommandActor.TenantID, PrincipalID: g.CommandActor.PrincipalID, PrincipalKind: "person", OwnerPersonID: g.CommandActor.OwnerPersonID, GrantID: g.CommandActor.GrantID, GrantRevision: g.CommandActor.GrantRevision}
	spec := nativecontract.LaunchSpec{Binding: *g.ExpectedBinding, Profile: e.profile, Policy: g.Policy, Cwd: e.local.Cwd, ResumeRef: i.NativeSessionRef, Mode: mode, OwnedWriterID: g.ExpectedBinding.RuntimeEpoch, Options: g.LaunchConfig, Lifetime: e.lifetime}
	if err = h.config.Qualification.Require(ctx, actor, spec, mode); err != nil {
		return out, err
	}
	if err = e.require(ctx); err != nil {
		return out, err
	}
	if err = e.stopEvents(ctx); err != nil {
		return out, err
	}
	t := &nativeTransition{record: nativeTransitionRecord{Operation: g.OperationID, Kind: g.Command.Kind, Phase: "closing", Source: *g.ExpectedBinding}, command: g, identity: i}
	e.transition = t
	if err = e.save("transition"); err != nil {
		return out, err
	}
	x, closeErr := old.Close(ctx)
	if closeErr != nil {
		return out, closeErr
	}
	waited, waitErr := old.Wait(ctx)
	if waitErr != nil {
		return out, waitErr
	}
	for _, exit := range []nativecontract.ExitEvidence{x, waited} {
		if !exit.RootExited || !exit.GroupAbsent || exit.ProcessIdentityDigest != i.ProcessIdentityDigest || exit.ObservedAt.IsZero() {
			return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
		}
	}
	e.mu.Lock()
	t.previous = e.proof
	e.mu.Unlock()
	t.previous.SetRuntime(i, nativecontract.NativeEvidence{}, &x)
	t.next = t.previous
	t.next.WorkerLease, err = randomID()
	if err != nil {
		return out, err
	}
	t.next.SessionRef, err = randomID()
	if err != nil {
		return out, err
	}
	t.next.ObservedAt = s.capacityNow()
	t.next.ExpiresAt = t.next.ObservedAt.Add(20 * time.Second)
	t.next.SetRuntime(nativecontract.NativeIdentity{}, nativecontract.NativeEvidence{}, nil)
	t.claim = nativecontract.Claim{RequestID: uuid.NewString(), OperationID: g.OperationID, DaemonID: s.daemonID, DaemonGeneration: s.generation, LedgerGeneration: s.ledgerBinding.Generation, CapabilityDigest: h.caps.QualificationDigest, ExpectedRevision: g.ExpectedBinding.BindingRevision, ClaimKind: "resume"}
	started := s.capacityNow()
	if err = e.successorClaim(ctx, &t.grant); err != nil {
		return out, err
	}
	fresh := t.grant.ExpectedBinding
	if fresh == nil || !t.grant.LaunchAuthorized || t.grant.ClaimKind != "resume" || !sameNativeConversation(*g.ExpectedBinding, *fresh) || fresh.RuntimeEpoch == g.ExpectedBinding.RuntimeEpoch || fresh.HarnessSessionID == g.ExpectedBinding.HarnessSessionID || fresh.BindingRevision <= g.ExpectedBinding.BindingRevision || !reflect.DeepEqual(t.grant.Intent, e.local.Intent) || !sameNativePolicy(g.Policy, t.grant.Policy) || !reflect.DeepEqual(g.LaunchConfig, t.grant.LaunchConfig) {
		return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	e.claimDeadline = started.Add(time.Duration(t.grant.AuthorizationTTLMS) * time.Millisecond)
	t.record.Successor = fresh
	t.record.Phase = "predecessor_exit_confirmed"
	if err = e.save("transition"); err != nil {
		return out, err
	}
	// This releases only the independently confirmed local group. The server
	// root, quota holds and original runtime deadline remain unchanged.
	if err = s.ledger.Release(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, e.group); err != nil {
		return out, err
	}
	group, err := agentsetup.LedgerID()
	if err != nil {
		return out, err
	}
	candidates, err := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group, s.ledgerCandidates([]string{e.local.Intent.AccountID}, s.ledgerBinding.Generation))
	if err != nil {
		return out, err
	}
	if len(candidates) != 1 || candidates[0].AccountID != e.local.Intent.AccountID {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	e.group = group
	profile, err := h.profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: fresh.TenantID, OwnerPersonID: e.owner.OwnerPersonID, AccountID: e.local.Intent.AccountID, AccountKey: e.local.AccountKey, WorkspaceID: e.local.Intent.WorkspaceID, Harness: Claude, Policy: t.grant.Policy, BindingRevision: e.profile.ProfileBindingRevision})
	if err != nil {
		return out, err
	}
	oldProfile := e.profile
	if !reflect.DeepEqual(profile, oldProfile) {
		return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	h.mu.Lock()
	e.mu.Lock()
	e.grant = t.grant
	e.proof = t.next
	e.transport = nil
	e.profile = profile
	e.ownership = nil
	e.observations = map[string]nativecontract.NativeEvidence{}
	e.exit = nil
	e.mu.Unlock()
	h.mu.Unlock()
	spec.Binding = *fresh
	spec.Profile = profile
	spec.OwnedWriterID = fresh.RuntimeEpoch
	if err = h.config.Qualification.Require(ctx, actor, spec, mode); err != nil {
		return out, err
	}
	if err = e.revalidateSuccessor(ctx); err != nil {
		return out, err
	}
	if err = s.ledger.Claimed(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group, s.ledgerLogin(e.local.Intent.AccountID, s.ledgerBinding.Generation)); err != nil {
		return out, err
	}
	t.record.Phase = "possible_fork"
	if err = e.save("transition"); err != nil {
		return out, err
	}
	if err = s.ledger.Launching(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group); err != nil {
		return out, err
	}
	var transport nativecontract.NativeTransport
	if mode == "terminal" {
		var in nativecontract.TakeIntent
		if err = decodeNativeControl(g.Command.Params, &in); err != nil {
			return out, err
		}
		pty, openErr := h.config.Terminal.OpenPTY(ctx, nativecontract.PTYLaunchSpec{LaunchSpec: spec, Rows: int(in.Rows), Cols: int(in.Cols)})
		if pty != nil {
			transport = &nativeTerminalTransport{PTYRuntime: pty, events: make(chan nativecontract.ProviderEvent)}
		}
		err = openErr
	} else {
		transport, err = h.config.Launcher.Open(ctx, spec)
	}
	// Retain every returned handle, including one returned with an error.
	if transport != nil {
		e.mu.Lock()
		e.transport = &nativeObservedTransport{NativeTransport: transport, owner: e}
		e.mu.Unlock()
	}
	if err != nil {
		return out, err
	}
	if transport == nil {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	identity := transport.Identity()
	if identity.RuntimeEpoch != fresh.RuntimeEpoch || identity.Provider != i.Provider || identity.NativeSessionRef != i.NativeSessionRef || identity.PID <= 1 || !identity.StartedAt.After(i.StartedAt) || !nativereceipts.ValidDigest(identity.ProcessIdentityDigest) || identity.ProcessIdentityDigest == i.ProcessIdentityDigest {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err = s.ledger.Running(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group, identity.PID, identity.StartedAt); err != nil {
		return out, err
	}
	d, err := delivery.New(delivery.DeliveryOptions{Binding: *fresh, Journal: e.journal, Transport: e.transport, Authority: e, Reports: e, Finals: e, Now: s.capacityNow, Retention: e.retention, Release: e.release, Refused: func(err error) bool { _ = e.fault(err); return e.faulted() != nil }})
	if err != nil {
		return out, err
	}
	e.delivery = d
	var commandID struct {
		RequestID string `json:"request_id"`
	}
	if err = decodeNativeControl(g.Command.Params, &commandID); err != nil {
		return out, err
	}
	r := nativecontract.Receipt{RequestID: commandID.RequestID, OperationID: g.OperationID, Binding: fresh, Operation: g.Command.Kind, State: "applied", SourceSequence: "0", UpdatedAt: s.capacityNow().UTC()}
	if err = e.call(ctx, "POST", "/api/agentd/native-sessions/receipts", r, &out); err != nil {
		return out, err
	}
	if out.Session == nil || out.Session.CurrentBinding == nil || !out.Session.CurrentBinding.Equal(*fresh) {
		return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	t.record.Phase = "running"
	if err = e.save("transition"); err != nil {
		return out, err
	}
	e.mu.Lock()
	e.active = nativecontract.ClaimResult{}
	e.mu.Unlock()
	e.transition = nil
	if mode == "structured" {
		e.startEvents()
	} else if terminal, ok := transport.(*nativeTerminalTransport); ok {
		if err = e.startTerminalFrames(*out.Session, terminal.PTYRuntime); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (e *nativeOwned) startTerminalFrames(view nativecontract.SessionView, pty nativecontract.PTYRuntime) error {
	live := e.host.config.Live
	if live == nil {
		return nil
	}
	// The daemon receipt is projected for its agent key. Local screen export
	// is separately installed and qualified; it still needs the viewer's
	// current person/watch consent at every delivery via LocalAuthority.
	view.Capabilities.TerminalView = e.host.caps.TerminalView
	if !view.Capabilities.TerminalView {
		return nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	if err := live.Register(view); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(e.host.s.lifetime)
	done := make(chan struct{})
	e.mu.Lock()
	e.eventCancel = cancel
	e.eventDone = done
	e.mu.Unlock()
	go func() {
		defer close(done)
		var sequence uint64
		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-pty.Frames():
				if !ok {
					return
				}
				sequence++
				raw, err := json.Marshal(frame)
				if err != nil || len(raw) > nativecontract.ExportedFrameBytes {
					_ = e.fault(nativeRefusal(nativecontract.ReasonBufferFull))
					return
				}
				position := strconv.FormatUint(sequence, 10)
				event := nativecontract.StreamEvent{Binding: *view.CurrentBinding, Generation: view.Generation, Sequence: position, Seq: position, Source: "native", ObservedAt: e.host.s.capacityNow(), Kind: "terminal", Payload: raw, DroppedFrames: "0"}
				err = live.Publish(ctx, nativecontract.Publish{RequestID: uuid.NewString(), ExpectedBinding: *view.CurrentBinding, Events: []nativecontract.StreamEvent{event}}, func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
					if err := e.require(ctx); err != nil {
						return err
					}
					return deliver(nativecontract.ValidationContext{Binding: view.CurrentBinding, OwnedSessionID: view.OwnedSessionID, Generation: view.Generation, Private: true, PrincipalKind: "agent", Capabilities: &view.Capabilities, Mode: "terminal", State: "taken", View: "terminal"})
				})
				if err != nil {
					_ = e.fault(err)
					return
				}
			}
		}
	}()
	return nil
}

// nativeLocalRequest is installed only by the protected socket wrapper. It
// preserves viewer provenance for the relay's per-delivery authorization.
type nativeLocalRequest struct {
	request nativestream.LocalRequest
	actor   nativecontract.Actor
}
type nativeLocalRequestKey struct{}

func (h *NativeHost) localSubscription(ctx context.Context, b nativecontract.Binding, q nativecontract.StreamQuery) (nativecontract.Subscription, error) {
	r, ok := ctx.Value(nativeLocalRequestKey{}).(nativeLocalRequest)
	if !ok || h.config.LocalAuthority == nil || h.config.Live == nil {
		return nil, nativeRefusal(nativecontract.ReasonGrantMissing)
	}
	fence := func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
		return h.config.LocalAuthority.WithAccess(ctx, r.actor, r.request, deliver)
	}
	return h.config.Live.SubscribeView(ctx, b, stream.Viewer{Query: q, Fence: fence})
}

func sameNativeConversation(a, b nativecontract.Binding) bool {
	a.RuntimeEpoch, b.RuntimeEpoch = "", ""
	a.HarnessSessionID, b.HarnessSessionID = "", ""
	a.BindingRevision, b.BindingRevision = 0, 0
	return a.Equal(b)
}

type nativeTerminalTransport struct {
	nativecontract.PTYRuntime
	events chan nativecontract.ProviderEvent
}

func (t *nativeTerminalTransport) Send(context.Context, nativecontract.Input) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, ErrUnsupported
}
func (t *nativeTerminalTransport) Interrupt(context.Context, nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, ErrUnsupported
}
func (t *nativeTerminalTransport) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, ErrUnsupported
}
func (t *nativeTerminalTransport) Events() <-chan nativecontract.ProviderEvent { return t.events }
func (t *nativeTerminalTransport) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported"}
}

var _ nativecontract.NativeTransport = (*nativeTerminalTransport)(nil)

func decodeNativeControl(raw []byte, out any) error {
	if len(raw) > nativecontract.RequestBodyBytes {
		return nativeRefusal(nativecontract.ReasonBufferFull)
	}
	if json.Unmarshal(raw, out) != nil {
		return nativeRefusal(nativecontract.ReasonInvalidInput)
	}
	return nil
}

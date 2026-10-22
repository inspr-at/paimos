// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativepty"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// NativePTYAuthority is the qualified local person command port. Prepare must
// durably accept the exact payload on the server and resolve its viewer to the
// actual client. WithWrite revalidates that operation, person, controller,
// binding and grant while holding the shared access-change fence through I/O.
// It must not hold a database transaction through the callback.
type NativePTYAuthority interface {
	nativepty.Authority
	Prepare(context.Context, nativecontract.Actor, nativestream.LocalRequest, string) (nativepty.Caller, string, error)
}

type nativeHostClock struct{ now func() time.Time }

func (c nativeHostClock) Now() time.Time                         { return c.now() }
func (c nativeHostClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// nativePTYWrite adds the existing metadata journal to the nativepty fence.
// Its intent is request-scoped under nativeOwned.opMu, shared with transitions,
// stop and structured writes. No PTY bytes are written to this journal.
type nativePTYWrite struct {
	owner  *nativeOwned
	intent nativereceipts.Intent
	begun  bool
	epoch  string
}

func (w *nativePTYWrite) WithWrite(ctx context.Context, caller nativepty.Caller, binding nativecontract.Binding, kind string, fn func(nativepty.Access) error) error {
	e := w.owner
	a := e.host.config.PTYAuthority
	if a == nil {
		return nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	return a.WithWrite(ctx, caller, binding, kind, func(access nativepty.Access) error {
		now := e.host.s.capacityNow()
		if !binding.Equal(*e.grant.ExpectedBinding) || binding.DaemonID != e.host.s.daemonID || binding.DaemonGeneration != e.host.s.generation {
			return nativeRefusal(nativecontract.ReasonWrongGeneration)
		}
		if access.Controller.ActorID != caller.Actor.PrincipalID || access.Controller.ClientID != caller.ClientID || access.Controller.Epoch != w.epoch || access.Controller.ControllerID == "" {
			return nativeRefusal(nativecontract.ReasonControllerConflict)
		}
		if !now.Before(access.Deadline) || !now.Before(w.intent.ExpiresAt) || !now.Before(e.grant.Policy.ExpiresAt) {
			return nativeRefusal(nativecontract.ReasonControllerExpired)
		}
		profile, err := e.host.profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: binding.TenantID, OwnerPersonID: e.owner.OwnerPersonID, AccountID: e.local.Intent.AccountID, AccountKey: e.local.AccountKey, WorkspaceID: e.local.Intent.WorkspaceID, Harness: e.local.Intent.Harness, Policy: e.grant.Policy, BindingRevision: e.profile.ProfileBindingRevision})
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(profile, e.profile) {
			return nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
		if _, err := e.journal.BeginWrite(ctx, binding, w.intent.Receipt.OperationID); err != nil {
			return err
		}
		w.begun = true
		// The access-change fence is still held after fsync. Recheck time and
		// the daemon/runtime immediately before the controller touches the PTY.
		if err := ctx.Err(); err != nil {
			return err
		}
		if binding.DaemonID != e.host.s.daemonID || binding.DaemonGeneration != e.host.s.generation || e.transport.Identity().ProcessIdentityDigest != w.intent.ProcessIdentityDigest {
			return nativeRefusal(nativecontract.ReasonWrongGeneration)
		}
		now = e.host.s.capacityNow()
		if !now.Before(access.Deadline) || !now.Before(w.intent.ExpiresAt) || !now.Before(e.grant.Policy.ExpiresAt) {
			return nativeRefusal(nativecontract.ReasonControllerExpired)
		}
		return fn(access)
	})
}

func (h *NativeHost) ptyOperation(ctx context.Context, b nativecontract.Binding, request, kind string, value any, expires time.Time) (out nativecontract.OperationResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	schema := map[string]string{"pty_input": "NativePTYInput", "resize": "NativeResize"}[kind]
	v, err := nativecontract.NewValidator()
	if err != nil {
		return out, err
	}
	raw, err := nativestream.Encode(v, schema, value)
	if err != nil {
		return out, err
	}
	local, ok := ctx.Value(nativeLocalRequestKey{}).(nativeLocalRequest)
	if !ok || local.actor.PrincipalKind != "person" || len(local.actor.KeyCeiling) != 0 || local.actor.TenantID != b.TenantID {
		return out, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	if h.config.PTYAuthority == nil {
		return out, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	if b.DaemonID != h.s.daemonID || b.DaemonGeneration != h.s.generation {
		return out, nativeRefusal(nativecontract.ReasonWrongGeneration)
	}
	h.mu.Lock()
	var e *nativeOwned
	for _, candidate := range h.entries {
		if candidate.grant.ExpectedBinding != nil && candidate.grant.ExpectedBinding.Equal(b) {
			e = candidate
			break
		}
	}
	h.mu.Unlock()
	if e == nil {
		return out, ErrNotOwned
	}
	if err = e.opMu.LockContext(ctx); err != nil {
		return out, err
	}
	defer e.opMu.Unlock()
	if e.transition != nil || e.terminal == nil || e.terminal.control == nil || e.exited || !b.Equal(*e.grant.ExpectedBinding) {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err = e.faulted(); err != nil {
		return out, err
	}
	local.request.Method, local.request.OwnedSessionID, local.request.ExpectedBinding = kind, b.OwnedSessionID, &b
	local.request.Envelope = &nativecontract.LocalEnvelope{Protocol: nativecontract.LocalProtocol, RequestID: request, Method: kind, ExpectedBinding: &b, Params: raw}
	digest, err := nativereceipts.Digest(value)
	if err != nil {
		return out, err
	}
	caller, operation, err := h.config.PTYAuthority.Prepare(ctx, local.actor, local.request, digest)
	if err != nil {
		return out, err
	}
	if !caller.Local || caller.Actor.PrincipalKind != "person" || caller.Actor.PrincipalID != local.actor.PrincipalID || caller.Actor.TenantID != b.TenantID || len(caller.Actor.KeyCeiling) != 0 || !uuidPattern.MatchString(operation) {
		return out, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	intent := nativereceipts.Intent{Receipt: nativecontract.Receipt{RequestID: request, OperationID: operation, Binding: &b, Operation: kind, State: "accepted", SourceSequence: "0", UpdatedAt: h.s.capacityNow().UTC()}, ActorID: caller.Actor.PrincipalID, PayloadSHA256: digest, ExpiresAt: expires, ProcessIdentityDigest: e.transport.Identity().ProcessIdentityDigest}
	r, err := e.journal.Accept(ctx, intent)
	if err != nil {
		return out, err
	}
	if r.State != "accepted" {
		return out, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	e.ptyWrite.intent, e.ptyWrite.begun = intent, false
	if kind == "pty_input" {
		e.ptyWrite.epoch = value.(nativecontract.PTYInput).ControllerEpoch
	} else {
		e.ptyWrite.epoch = value.(nativecontract.Resize).ControllerEpoch
	}
	var evidence nativecontract.NativeEvidence
	if kind == "pty_input" {
		evidence, err = e.terminal.control.Input(ctx, caller, value.(nativecontract.PTYInput))
	} else {
		evidence, err = e.terminal.control.Resize(ctx, caller, value.(nativecontract.Resize))
	}
	if err == nil {
		evidence.SourceSequence = 1
		e.mu.Lock()
		e.observations[evidence.Stage+"/"+request] = evidence
		e.mu.Unlock()
		r, err = e.journal.Observe(ctx, b, operation, evidence)
	}
	if err != nil && e.ptyWrite.begun {
		reason := nativecontract.ReasonInputOutcomeUnconfirmed
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		r, journalErr := e.journal.SetIntentState(cleanup, b, operation, "uncertain", &reason)
		out.Receipt = r
		e.mu.Lock()
		e.refused = nativeRefusal(reason) // Explicit recovery, never reinjection.
		e.mu.Unlock()
		return out, errors.Join(err, journalErr)
	}
	if err != nil {
		return out, err
	}
	out.Receipt = r
	e.eventMu.Lock()
	_, err = e.journal.FlushOperationReports(ctx, b, operation, 16, e)
	e.eventMu.Unlock()
	return out, err
}

func (e *nativeOwned) openTerminal(ctx context.Context, spec nativecontract.LaunchSpec, cols, rows int) (nativecontract.NativeTransport, error) {
	actor := nativecontract.Actor{TenantID: e.owner.TenantID, PrincipalID: e.owner.PrincipalID, PrincipalKind: "person", OwnerPersonID: e.owner.OwnerPersonID, GrantID: e.owner.GrantID, GrantRevision: e.owner.GrantRevision}
	if e.host.config.Terminal == nil || e.host.config.Qualification == nil {
		return nil, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	check := func(ctx context.Context) error {
		if err := e.host.config.Qualification.Require(ctx, actor, spec, "terminal"); err != nil {
			return err
		}
		return e.require(ctx)
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	pty, err := e.host.config.Terminal.OpenPTY(ctx, nativecontract.PTYLaunchSpec{LaunchSpec: spec, Cols: cols, Rows: rows, Revalidate: check})
	if pty == nil {
		return nil, err
	}
	t := &nativeTerminalTransport{PTYRuntime: pty, events: make(chan nativecontract.ProviderEvent), digest: sha256.New(), spec: spec}
	e.mu.Lock()
	e.terminal = t
	e.mu.Unlock()
	if err != nil {
		return t, err
	}
	identity, err := nativecontract.ReadyPTY(ctx, pty)
	if err != nil {
		return t, err
	}
	if identity != pty.Identity() || identity.Provider != e.local.Intent.Harness || identity.RuntimeEpoch != spec.Binding.RuntimeEpoch || identity.NativeSessionRef == "" || spec.ResumeRef != "" && identity.NativeSessionRef != spec.ResumeRef {
		return t, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	spec.ResumeRef = identity.NativeSessionRef
	e.ptyWrite = &nativePTYWrite{owner: e}
	t.control, err = nativepty.NewOwnedControl(spec, pty, e.ptyWrite, nativeHostClock{e.host.s.capacityNow})
	return t, err
}

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
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
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
	for id, drain := range e.drains {
		if !s.capacityNow().Before(drain.grant.ExpiresAt) {
			delete(e.drains, id)
		}
	}
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
	spec, err = e.admitSuccessor(ctx, g, i, x, mode)
	if err != nil {
		return out, err
	}
	return e.openSuccessor(ctx, spec, actor)
}

func (e *nativeOwned) admitSuccessor(ctx context.Context, g nativecontract.ClaimResult, i nativecontract.NativeIdentity, x nativecontract.ExitEvidence, mode string) (spec nativecontract.LaunchSpec, err error) {
	h, s := e.host, e.host.s
	t := e.transition
	e.mu.Lock()
	t.previous = e.proof
	e.mu.Unlock()
	t.previous.SetRuntime(i, nativecontract.NativeEvidence{}, &x)
	t.previous.Terminal = e.terminalEvidence()
	t.next = t.previous
	t.next.Terminal = nil
	t.next.WorkerLease, err = randomID()
	if err != nil {
		return spec, err
	}
	t.next.SessionRef, err = randomID()
	if err != nil {
		return spec, err
	}
	t.next.ObservedAt = s.capacityNow()
	t.next.ExpiresAt = t.next.ObservedAt.Add(20 * time.Second)
	t.next.SetRuntime(nativecontract.NativeIdentity{}, nativecontract.NativeEvidence{}, nil)
	t.claim = nativecontract.Claim{RequestID: uuid.NewString(), OperationID: g.OperationID, DaemonID: s.daemonID, DaemonGeneration: s.generation, LedgerGeneration: s.ledgerBinding.Generation, CapabilityDigest: h.caps.QualificationDigest, ExpectedRevision: g.ExpectedBinding.BindingRevision, ClaimKind: "resume"}
	started := s.capacityNow()
	if err = e.successorClaim(ctx, &t.grant); err != nil {
		return spec, err
	}
	fresh := t.grant.ExpectedBinding
	if fresh == nil || !t.grant.LaunchAuthorized || t.grant.ClaimKind != "resume" || !sameNativeConversation(*g.ExpectedBinding, *fresh) || fresh.RuntimeEpoch == g.ExpectedBinding.RuntimeEpoch || fresh.HarnessSessionID == g.ExpectedBinding.HarnessSessionID || fresh.BindingRevision <= g.ExpectedBinding.BindingRevision || !reflect.DeepEqual(t.grant.Intent, e.local.Intent) || !sameNativePolicy(g.Policy, t.grant.Policy) || !reflect.DeepEqual(g.LaunchConfig, t.grant.LaunchConfig) {
		return spec, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	e.claimDeadline = started.Add(time.Duration(t.grant.AuthorizationTTLMS) * time.Millisecond)
	t.record.Successor = fresh
	t.record.Phase = "predecessor_exit_confirmed"
	if err = e.save("transition"); err != nil {
		return spec, err
	}
	profile, err := h.profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: fresh.TenantID, OwnerPersonID: e.owner.OwnerPersonID, AccountID: e.local.Intent.AccountID, AccountKey: e.local.AccountKey, WorkspaceID: e.local.Intent.WorkspaceID, Harness: e.local.Intent.Harness, Policy: t.grant.Policy, BindingRevision: e.profile.ProfileBindingRevision})
	if err != nil {
		return spec, err
	}
	oldProfile := e.profile
	if !reflect.DeepEqual(profile, oldProfile) {
		return spec, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	spec = nativecontract.LaunchSpec{Binding: *fresh, Profile: profile, Policy: t.grant.Policy, Cwd: e.local.Cwd, ResumeRef: i.NativeSessionRef, Mode: mode, OwnedWriterID: fresh.RuntimeEpoch, Options: t.grant.LaunchConfig, Lifetime: e.lifetime}
	return spec, nil
}

func (e *nativeOwned) openSuccessor(ctx context.Context, spec nativecontract.LaunchSpec, actor nativecontract.Actor) (out nativecontract.OperationResult, err error) {
	h, s := e.host, e.host.s
	t := e.transition
	if t == nil || t.record.Phase != "predecessor_exit_confirmed" {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	g, i, mode, fresh := t.command, t.identity, spec.Mode, t.grant.ExpectedBinding
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
	profile := spec.Profile
	h.mu.Lock()
	e.mu.Lock()
	e.grant = t.grant
	e.proof = t.next
	e.transport = nil
	e.terminal = nil
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
		transport, err = e.openTerminal(ctx, spec, int(in.Cols), int(in.Rows))
	} else {
		transport, err = h.config.Launcher.Open(ctx, spec)
	}
	// Retain every returned handle, including one returned with an error.
	if transport != nil {
		err = errors.Join(err, e.retainTransport(transport))
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
	ownershipSource := any(transport)
	if terminal, ok := transport.(*nativeTerminalTransport); ok {
		ownershipSource = terminal.PTYRuntime
	}
	if owner, ok := ownershipSource.(interface {
		Ownership() (ownedprocess.Identity, error)
	}); ok {
		owned, err := owner.Ownership()
		if err != nil {
			return out, err
		}
		e.ownership = &owned
	}
	var commandID struct {
		RequestID string `json:"request_id"`
	}
	if err = decodeNativeControl(g.Command.Params, &commandID); err != nil {
		return out, err
	}
	r := nativecontract.Receipt{RequestID: commandID.RequestID, OperationID: t.grant.OperationID, Binding: fresh, Operation: g.Command.Kind, State: "applied", SourceSequence: "0", UpdatedAt: s.capacityNow().UTC()}
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
	// The daemon receipt is projected for its agent key. Local screen export
	// is separately installed and qualified; it still needs the viewer's
	// current person/watch consent at every delivery via LocalAuthority.
	view.Capabilities.TerminalView = e.host.caps.TerminalView
	export := live != nil && view.Capabilities.TerminalView && view.Capabilities.EventsSupported && e.host.caps.EventsSupported
	if export {
		if err := live.Register(view); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(e.host.s.lifetime)
	done := make(chan struct{})
	e.mu.Lock()
	e.eventCancel = cancel
	e.eventDone = done
	terminal := e.terminal
	e.mu.Unlock()
	go func() {
		defer close(done)
		if export {
			defer live.Remove(*view.CurrentBinding)
		}
		var sequence, source uint64
		publish := func(kind string, payload any, dropped string) error {
			if !export {
				return nil
			}
			sequence++
			raw, err := json.Marshal(payload)
			if err != nil || len(raw) > nativecontract.ExportedFrameBytes {
				return nativeRefusal(nativecontract.ReasonBufferFull)
			}
			position := strconv.FormatUint(sequence, 10)
			event := nativecontract.StreamEvent{Binding: *view.CurrentBinding, Generation: view.Generation, Sequence: position, Seq: position, Source: "native", ObservedAt: e.host.s.capacityNow(), Kind: kind, Payload: raw, DroppedFrames: dropped}
			return live.Publish(ctx, nativecontract.Publish{RequestID: uuid.NewString(), ExpectedBinding: *view.CurrentBinding, Events: []nativecontract.StreamEvent{event}}, func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
				if err := e.require(ctx); err != nil {
					return err
				}
				return deliver(nativecontract.ValidationContext{Binding: view.CurrentBinding, OwnedSessionID: view.OwnedSessionID, Generation: view.Generation, Private: true, PrincipalKind: "agent", Capabilities: &view.Capabilities, Mode: "terminal", State: "taken", View: "terminal"})
			})
		}
		fail := func(err error) {
			e.mu.Lock()
			e.lastError, e.refused = err, err
			e.mu.Unlock()
			// Only the owner cancels its lifetime. Occupancy is released solely
			// after owned exit AND the existing server lifecycle write succeeds.
			e.cancel()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-pty.Frames():
				if !ok {
					// Never wait for opMu before closing eventDone: a transition
					// owns that lock while draining this reader.
					go func() {
						cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), nativecontract.LocalOperationSeconds*time.Second)
						defer finish()
						if err := e.finishTerminalExit(cleanup, terminal); err != nil {
							_ = e.fault(err)
						}
					}()
					return
				}
				if len(frame.DataBase64) > base64.StdEncoding.EncodedLen(nativecontract.PTYInputDecodedBytes) {
					fail(nativeRefusal(nativecontract.ReasonBufferFull))
					return
				}
				data, err := base64.StdEncoding.Strict().DecodeString(frame.DataBase64)
				next, parseErr := strconv.ParseUint(frame.Sequence, 10, 64)
				if err != nil || parseErr != nil || next <= source || frame.TerminalEpoch != view.CurrentBinding.RuntimeEpoch || !nativepty.ValidSize(frame.Cols, frame.Rows) {
					clear(data)
					fail(nativeRefusal(nativecontract.ReasonIdentityMismatch))
					return
				}
				terminal.mu.Lock()
				terminal.bytes += uint64(len(data))
				terminal.frames++
				_, _ = terminal.digest.Write(data)
				terminal.mu.Unlock()
				clear(data)
				if next != source+1 {
					dropped := next - source - 1
					if err := publish("gap", nativecontract.Gap{Reason: "frame_omitted", DroppedFrames: strconv.FormatUint(dropped, 10), ReloadSnapshot: true}, strconv.FormatUint(dropped, 10)); err != nil {
						fail(err)
						return
					}
					if export && dropped > 1 {
						sequence += dropped - 1
					}
				}
				source = next
				if err := publish("terminal", frame, "0"); err != nil {
					fail(err)
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
	events        chan nativecontract.ProviderEvent
	mu            sync.Mutex
	digest        hash.Hash
	bytes, frames uint64
	spec          nativecontract.LaunchSpec
	control       *nativepty.OwnedControl
}

func (e *nativeOwned) terminalEvidence() *daemon.TerminalEvidence {
	e.mu.Lock()
	t := e.terminal
	e.mu.Unlock()
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return &daemon.TerminalEvidence{RuntimeEpoch: t.spec.Binding.RuntimeEpoch, OutputBytes: t.bytes, Frames: t.frames, OutputSHA256: hex.EncodeToString(t.digest.Sum(nil))}
}

func (t *nativeTerminalTransport) ObserveExit(ctx context.Context) (nativecontract.ExitEvidence, error) {
	observer, ok := t.PTYRuntime.(interface {
		ObserveExit(context.Context) (nativecontract.ExitEvidence, error)
	})
	if !ok {
		return nativecontract.ExitEvidence{}, ErrProcessesUnconfirmed
	}
	return observer.ObserveExit(ctx)
}

func (e *nativeOwned) finishTerminalExit(ctx context.Context, terminal *nativeTerminalTransport) error {
	x, err := terminal.Wait(ctx)
	if err != nil {
		return err
	}
	if !x.RootExited || !x.GroupAbsent || x.ObservedAt.IsZero() || x.ProcessIdentityDigest != terminal.Identity().ProcessIdentityDigest {
		return nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err := e.opMu.LockContext(ctx); err != nil {
		return err
	}
	defer e.opMu.Unlock()
	return e.finishTerminalExitLocked(ctx, terminal, x)
}
func (e *nativeOwned) finishTerminalExitLocked(ctx context.Context, terminal *nativeTerminalTransport, x nativecontract.ExitEvidence) error {
	if terminal == nil || !x.RootExited || !x.GroupAbsent || x.ObservedAt.IsZero() || x.ProcessIdentityDigest != terminal.Identity().ProcessIdentityDigest {
		return nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if e.transition != nil || e.terminal != terminal || e.exited {
		return nil
	}
	e.mu.Lock()
	e.exit = &x
	e.mu.Unlock()
	b := *e.grant.ExpectedBinding
	r := nativecontract.Receipt{RequestID: e.local.Intent.RequestID, OperationID: e.startOperation, Binding: &b, Operation: "start", State: "exit_confirmed", SourceSequence: "1", UpdatedAt: e.host.s.capacityNow().UTC()}
	var out nativecontract.OperationResult
	if err := e.call(ctx, "POST", "/api/agentd/native-sessions/receipts", r, &out); err != nil {
		return err
	}
	if out.Session == nil || out.Session.CurrentBinding == nil || !out.Session.CurrentBinding.Equal(b) || out.Session.State != "stopped" {
		return nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return e.release(ctx, b, x)
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

// The old request's acknowledgement stays bound to the old generation. The
// client explicitly refreshes/re-attaches to the successor, as required by the
// local contract; no stale stream or controller follows a switch implicitly.
func localTransitionReceipt(out nativecontract.OperationResult, b nativecontract.Binding) nativecontract.OperationResult {
	out.Receipt.Binding = &b
	out.Session = nil
	return out
}

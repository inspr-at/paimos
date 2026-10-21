// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

type NativeStartEnvelope struct {
	Protocol        string                    `json:"protocol"`
	RequestID       string                    `json:"request_id"`
	Method          string                    `json:"method"`
	ExpectedBinding json.RawMessage           `json:"expected_binding"`
	Params          NativeSessionStartRequest `json:"params"`
}

// The existing protected socket proves local process provenance only. The
// supplied person authority must independently verify the actual person and
// current server intent/view/controller; the paired agent is never that person.
type nativeSocketAuthority struct {
	authority nativestream.LocalAuthority
	authorize func(*http.Request) bool
}

func (a *nativeSocketAuthority) Authenticate(r *http.Request) (nativecontract.Actor, error) {
	if !a.authorize(r) {
		return nativecontract.Actor{}, nativeRefusal(nativecontract.ReasonGrantMissing)
	}
	peer, ok := r.Context().Value(attachPeerKey{}).(attachObservation)
	if !ok || peer.UID != os.Getuid() {
		return nativecontract.Actor{}, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	current, err := observeAttachProcess(peer.PID)
	if err != nil || !sameAttachProcessIdentity(peer, current) {
		return nativecontract.Actor{}, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	actor, err := a.authority.Authenticate(r)
	if err != nil {
		return actor, err
	}
	if actor.PrincipalKind != "person" || !uuidPattern.MatchString(actor.PrincipalID) || !uuidPattern.MatchString(actor.TenantID) || len(actor.KeyCeiling) > 0 {
		return nativecontract.Actor{}, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	query := r.URL.Query()
	request := nativestream.LocalRequest{Method: "stream", OwnedSessionID: r.PathValue("ownedSessionId"), ViewerID: r.Header.Get(nativecontract.ViewerHeader), Query: query, View: query.Get("view")}
	if request.View == "" {
		request.View = "structured"
	}
	*r = *r.WithContext(context.WithValue(r.Context(), nativeLocalRequestKey{}, nativeLocalRequest{request: request, actor: actor}))
	return actor, nil
}
func (a *nativeSocketAuthority) WithAccess(ctx context.Context, actor nativecontract.Actor, r nativestream.LocalRequest, deliver func(nativecontract.ValidationContext) error) error {
	return a.authority.WithAccess(ctx, actor, r, deliver)
}

func serveNativeStart(s *Supervisor, authorize func(*http.Request) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nativestream.Headers(w)
		if !authorize(r) {
			nativestream.WriteError(w, &nativestream.Failure{Status: http.StatusUnauthorized, Reason: nativecontract.ReasonGrantMissing})
			return
		}
		if r.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonUnsupported))
			return
		}
		if r.URL.RawQuery != "" {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonInvalidInput))
			return
		}
		raw, err := httpapi.ReadRequestBody(w, r, nativecontract.RequestBodyBytes, httpapi.DefaultBodyReadTimeout)
		if err != nil {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonBufferFull))
			return
		}
		validator, err := nativecontract.NewValidator()
		if err != nil {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonQualificationMissing))
			return
		}
		envelope, err := validator.DecodeLocal(raw, nativecontract.LocalProtocol, "start", nativecontract.ValidationContext{PrincipalKind: "person"})
		if err != nil {
			nativestream.WriteError(w, err)
			return
		}
		var local nativecontract.LocalStart
		if err = validator.DecodeJSON("NativeLocalStart", envelope.Params, &local); err != nil {
			nativestream.WriteError(w, err)
			return
		}
		if s.native == nil || s.native.config == nil || s.native.config.StartLocal == nil {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonQualificationMissing))
			return
		}
		peer, ok := r.Context().Value(attachPeerKey{}).(attachObservation)
		if !ok || peer.UID != os.Getuid() {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonWrongPrincipal))
			return
		}
		current, peerErr := observeAttachProcess(peer.PID)
		if peerErr != nil || !sameAttachProcessIdentity(peer, current) {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonWrongPrincipal))
			return
		}
		if local.Intent.Role != "coordinator" || local.Intent.Profile != "native" || local.Intent.Harness != Claude {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonUnsupported))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), nativecontract.LocalOperationSeconds*time.Second)
		defer cancel()
		result, err := s.native.config.StartLocal(ctx, local)
		if err != nil {
			nativestream.WriteError(w, err)
			return
		}
		if result.Receipt.RequestID != local.Intent.RequestID || result.Receipt.Operation != "start" {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonIdentityMismatch))
			return
		}
		if _, err := nativestream.Encode(validator, "NativeOperationResult", result); err != nil {
			nativestream.WriteError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if result.Receipt.State == "accepted" || result.Receipt.State == "queued" {
			w.WriteHeader(http.StatusAccepted)
		}
		_ = json.NewEncoder(w).Encode(result)
	}
}

// nativeC0Transport exports only explicit provider observations. The immutable
// process identity never changes when a new native turn begins.
type nativeC0Transport struct {
	mu       sync.Mutex
	owner    *nativeOwned
	spec     nativecontract.LaunchSpec
	process  *nativeClaudeLead
	writer   nativeC0Writer // the same owned process; its write boundary only
	identity nativecontract.NativeIdentity
	caps     nativecontract.Capabilities
	events   chan nativecontract.ProviderEvent
	sequence uint64
	// Live or uncertain work only: a completed input or applied interrupt
	// leaves, an unconfirmed one stays and keeps blocking the next input.
	pending map[string]*nativeC0Input
	exit    *nativecontract.ExitEvidence
	fault   error
}

// nativeC0Writer is the owned provider's input/control boundary.
type nativeC0Writer interface {
	Input(ctx context.Context, id, text string) error
	nativeControl(ctx context.Context, id, op string) error
}
type nativeC0Input struct {
	input, request, turn, text, kind                       string
	ack, started, completed, emittedStart, emittedComplete bool
	ackEvidence                                            nativecontract.NativeEvidence
}

func newNativeC0Transport(owner *nativeOwned, spec nativecontract.LaunchSpec, caps nativecontract.Capabilities) *nativeC0Transport {
	return &nativeC0Transport{owner: owner, spec: spec, caps: caps, events: make(chan nativecontract.ProviderEvent, 256), pending: map[string]*nativeC0Input{}}
}
func (t *nativeC0Transport) Capabilities() nativecontract.Capabilities { return t.caps }
func (t *nativeC0Transport) Identity() nativecontract.NativeIdentity {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.identity
}
func (t *nativeC0Transport) Events() <-chan nativecontract.ProviderEvent { return t.events }
func (t *nativeC0Transport) beforeInput(ctx context.Context, id, digest string) error {
	t.owner.mu.Lock()
	active := t.owner.active
	t.owner.mu.Unlock()
	if active.OperationID == "" {
		return nativeRefusal(nativecontract.ReasonGrantMissing)
	}
	if active.Command == nil {
		return nativeRefusal(nativecontract.ReasonGrantMissing)
	}
	switch active.Command.Kind {
	case "input":
		var in nativecontract.Input
		if json.Unmarshal(active.Command.Params, &in) != nil || id != in.InputID || digest != nativeDigest(in.Text) {
			return nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
	case "interrupt":
		var in nativecontract.Interrupt
		if json.Unmarshal(active.Command.Params, &in) != nil || id != in.RequestID || digest != nativeDigest("interrupt") {
			return nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
	case "stop":
		if id != "native-stop" || digest != nativeDigest("stop") {
			return nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
	case "take", "return":
		// A current person-authorized switch may close this exact predecessor;
		// it cannot send provider input or reuse the old command on a successor.
		if id != "native-stop" || digest != nativeDigest("stop") {
			return nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
		return t.owner.require(ctx)
	default:
		return ErrUnsupported
	}
	r, err := t.owner.journal.Get(t.spec.Binding, active.OperationID)
	if err != nil || r.State != "write_started" {
		return nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	return t.owner.require(ctx)
}
func (t *nativeC0Transport) Send(ctx context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	if !in.ExpectedBinding.Equal(t.spec.Binding) {
		return nativecontract.NativeEvidence{}, nativeRefusal(nativecontract.ReasonWrongGeneration)
	}
	t.mu.Lock()
	if t.fault != nil {
		err := t.fault
		t.mu.Unlock()
		return nativecontract.NativeEvidence{}, err
	}
	w := t.writer
	if w == nil {
		t.mu.Unlock()
		return nativecontract.NativeEvidence{}, ErrNotOwned
	}
	if len(t.pending) >= 128 || t.pending[in.InputID] != nil {
		t.mu.Unlock()
		return nativecontract.NativeEvidence{}, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	for _, p := range t.pending {
		if p.kind == "input" && !p.completed {
			t.mu.Unlock()
			return nativecontract.NativeEvidence{}, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
		}
	}
	pending := &nativeC0Input{input: in.InputID, request: in.RequestID, kind: "input"}
	t.pending[in.InputID] = pending
	t.mu.Unlock()
	if err := w.Input(ctx, in.InputID, in.Text); err != nil {
		return nativecontract.NativeEvidence{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !pending.ack || t.fault != nil {
		return nativecontract.NativeEvidence{}, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	return pending.ackEvidence, nil
}
func (t *nativeC0Transport) Interrupt(ctx context.Context, in nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	if !in.ExpectedBinding.Equal(t.spec.Binding) {
		return nativecontract.NativeEvidence{}, nativeRefusal(nativecontract.ReasonWrongGeneration)
	}
	t.mu.Lock()
	w := t.writer
	found := false
	for _, p := range t.pending {
		if p.kind == "input" && p.turn == in.ExpectedTurnRef && p.started && !p.completed {
			found = true
		}
	}
	if w == nil || !found || t.pending[in.RequestID] != nil || len(t.pending) >= 128 {
		t.mu.Unlock()
		return nativecontract.NativeEvidence{}, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	pending := &nativeC0Input{request: in.RequestID, turn: in.ExpectedTurnRef, kind: "interrupt"}
	t.pending[in.RequestID] = pending
	t.mu.Unlock()
	if err := w.nativeControl(ctx, in.RequestID, "interrupt"); err != nil {
		return nativecontract.NativeEvidence{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !pending.ack {
		return nativecontract.NativeEvidence{}, nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	delete(t.pending, in.RequestID) // Applied: no longer live work.
	return pending.ackEvidence, nil
}
func (*nativeC0Transport) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, ErrUnsupported
}
func (t *nativeC0Transport) Close(ctx context.Context) (nativecontract.ExitEvidence, error) {
	t.mu.Lock()
	p := t.process
	t.mu.Unlock()
	if p == nil {
		return nativecontract.ExitEvidence{}, ErrNotOwned
	}
	if err := p.Stop(ctx); err != nil {
		return nativecontract.ExitEvidence{}, err
	}
	return t.Wait(ctx)
}
func (t *nativeC0Transport) Wait(ctx context.Context) (nativecontract.ExitEvidence, error) {
	t.mu.Lock()
	p := t.process
	t.mu.Unlock()
	if p == nil {
		return nativecontract.ExitEvidence{}, ErrNotOwned
	}
	select {
	case <-ctx.Done():
		return nativecontract.ExitEvidence{}, ctx.Err()
	case <-p.waitDone:
	}
	if !p.ProcessExited() || p.cleanupResult() != nil {
		return nativecontract.ExitEvidence{}, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	i := t.Identity()
	x := nativecontract.ExitEvidence{ProcessIdentityDigest: i.ProcessIdentityDigest, RootExited: true, GroupAbsent: true, ObservedAt: t.owner.host.s.capacityNow(), Reason: "owned_group_exit"}
	t.mu.Lock()
	t.exit = &x
	t.mu.Unlock()
	return x, nil
}
func (t *nativeC0Transport) observe(raw json.RawMessage) {
	if len(raw) > nativecontract.NativeFrameBeforeDecodeBytes {
		return
	}
	var f struct {
		Kind, Text  string
		Correlation string `json:"correlation_id"`
		Session     string `json:"harness_session_id"`
		Turn        string `json:"native_turn_ref"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if f.Kind == "session_started" {
		if t.identity.NativeSessionRef == "" {
			t.identity.NativeSessionRef = f.Session
		}
		return
	}
	if f.Correlation == "" || f.Correlation == "initial" {
		return
	}
	p := t.pending[f.Correlation]
	if p == nil {
		return
	}
	switch f.Kind {
	case "control_applied":
		if p.ack {
			return
		}
		p.ack = true
		stage := "write_acknowledged"
		if p.kind == "interrupt" {
			stage = "applied"
		}
		p.ackEvidence = t.newEvidence(p, stage)
	case "turn_started":
		if f.Turn != "" && len(f.Turn) <= 128 {
			if p.turn != "" && p.turn != f.Turn {
				t.fault = nativeRefusal(nativecontract.ReasonInputOutcomeUnconfirmed)
				return
			}
			p.turn = f.Turn
			p.started = true
		}
	case "turn_completed":
		if f.Turn == p.turn && p.turn != "" {
			p.completed = true
		}
	case "chat_final":
		if len(f.Text) <= nativereceipts.MaxTextBytes && f.Turn == p.turn {
			p.text = f.Text
		}
	}
	if p.kind != "input" || !p.ack {
		return
	}
	if p.started && !p.emittedStart {
		p.emittedStart = true
		t.emitReceipt(p, "turn_started")
	}
	if p.completed && !p.emittedComplete && p.emittedStart {
		p.emittedComplete = true
		t.emitReceipt(p, "turn_completed")
		if p.text != "" {
			final := nativecontract.FinalPublish{RequestID: uuid.NewString(), FinalID: uuid.NewString(), ExpectedBinding: t.spec.Binding, InputID: p.input, TurnRef: p.turn, Text: p.text}
			t.emit(nativecontract.ProviderEvent{Identity: t.identity, Final: &final})
		}
		p.text = ""
		// Completion and its final are emitted: the input is no longer live.
		// The bridge drops the correlation too; a late frame finds no entry.
		delete(t.pending, p.input)
	}
}
func (t *nativeC0Transport) newEvidence(p *nativeC0Input, stage string) nativecontract.NativeEvidence {
	t.sequence++
	return nativecontract.NativeEvidence{Stage: stage, InputID: p.input, RequestID: p.request, TurnRef: p.turn, SourceSequence: t.sequence, ObservedAt: t.owner.host.s.capacityNow()}
}
func (t *nativeC0Transport) emitReceipt(p *nativeC0Input, stage string) {
	e := t.newEvidence(p, stage)
	id, turn := p.input, p.turn
	r := nativecontract.Receipt{RequestID: p.request, InputID: &id, TurnRef: &turn, State: stage, SourceSequence: strconv.FormatUint(e.SourceSequence, 10)}
	t.emit(nativecontract.ProviderEvent{SourceSequence: e.SourceSequence, Identity: t.identity, Receipt: &r})
}
func (t *nativeC0Transport) emit(e nativecontract.ProviderEvent) {
	select {
	case t.events <- e:
	default:
		t.fault = nativeRefusal(nativecontract.ReasonBufferFull)
	}
}
func verifyNativePin(pin nativecontract.BinaryPin) error {
	f, err := os.Open(pin.Path)
	if err != nil {
		return ErrNativeUnqualified
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return ErrNativeUnqualified
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, 512<<20+1)); err != nil {
		return ErrNativeUnqualified
	}
	if hex.EncodeToString(h.Sum(nil)) != pin.SHA256 {
		return ErrNativeUnqualified
	}
	return nil
}

// The owner records evidence only from its installed provider. A public body
// cannot fill this cache, and retrying a report never repeats provider I/O.
type nativeObservedTransport struct {
	nativecontract.NativeTransport
	owner *nativeOwned
}

func (t *nativeObservedTransport) Send(ctx context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	e, err := t.NativeTransport.Send(ctx, in)
	if err == nil {
		t.owner.mu.Lock()
		t.owner.observations[e.Stage+"/"+e.RequestID] = e
		t.owner.mu.Unlock()
	}
	return e, err
}
func (t *nativeObservedTransport) Interrupt(ctx context.Context, in nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	e, err := t.NativeTransport.Interrupt(ctx, in)
	if err == nil {
		t.owner.mu.Lock()
		t.owner.observations[e.Stage+"/"+e.RequestID] = e
		t.owner.mu.Unlock()
	}
	return e, err
}
func (t *nativeObservedTransport) Close(ctx context.Context) (nativecontract.ExitEvidence, error) {
	x, err := t.NativeTransport.Close(ctx)
	if err == nil {
		t.owner.mu.Lock()
		t.owner.exit = &x
		t.owner.mu.Unlock()
	}
	return x, err
}
func (t *nativeObservedTransport) Wait(ctx context.Context) (nativecontract.ExitEvidence, error) {
	x, err := t.NativeTransport.Wait(ctx)
	if err == nil {
		t.owner.mu.Lock()
		t.owner.exit = &x
		t.owner.mu.Unlock()
	}
	return x, err
}
func (e *nativeOwned) proofFor(r *nativecontract.Receipt) (nativecontract.NativeIdentity, nativecontract.NativeEvidence, *nativecontract.ExitEvidence) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ev nativecontract.NativeEvidence
	if r != nil {
		ev = e.observations[r.State+"/"+r.RequestID]
	}
	return e.transport.Identity(), ev, e.exit
}

func (t *nativeC0Transport) ObserveExit(ctx context.Context) (nativecontract.ExitEvidence, error) {
	t.mu.Lock()
	p := t.process
	t.mu.Unlock()
	if p == nil || !p.ProcessExited() {
		return nativecontract.ExitEvidence{}, ErrProcessesUnconfirmed
	}
	return t.Wait(ctx)
}
func (t *nativeObservedTransport) ObserveExit(ctx context.Context) (nativecontract.ExitEvidence, error) {
	observer, ok := t.NativeTransport.(interface {
		ObserveExit(context.Context) (nativecontract.ExitEvidence, error)
	})
	if !ok {
		return nativecontract.ExitEvidence{}, ErrProcessesUnconfirmed
	}
	x, err := observer.ObserveExit(ctx)
	if err == nil {
		t.owner.mu.Lock()
		t.owner.exit = &x
		t.owner.mu.Unlock()
	}
	return x, err
}

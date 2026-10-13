// SPDX-License-Identifier: AGPL-3.0-only

package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"time"

	n "github.com/inspr-at/paimos/internal/nativecontract"
)

// FinalGrants supplies only the exact server-issued publication grant. A
// provider final without it remains a live final reference, never a publication.
type FinalGrants interface {
	FinalDrain(context.Context, n.Binding) (n.FinalDrainGrant, error)
}

// ApprovalAuthority uses the original command actor/current effect/step-up
// authorization. Session-level permission alone must not approve a tool.
type ApprovalAuthority interface {
	Approve(context.Context, n.ApprovalDecision, n.Approval) error
}

type pendingApproval struct {
	approval n.Approval
	rpcID    json.RawMessage
}
type transport struct {
	mu           sync.Mutex // serializes account/grant rechecks with native writes and notifications
	wire         Wire
	grants       GrantSource
	clock        n.Clock
	spec         n.LaunchSpec
	grant        WorkerGrant
	identity     n.NativeIdentity
	events       chan n.ProviderEvent
	readerDone   chan struct{}
	seq          uint64
	turn         string
	input        n.Input
	active       bool
	started      bool
	turns        int64
	tokens       int64
	final        *nativeItem
	finalGrantID string
	finals       int64
	approvals    map[string]pendingApproval
	invalid      error
	closing      bool
}

var _ n.NativeTransport = (*transport)(nil)

func (s *transport) initialize(ctx context.Context) error {
	if _, err := s.call(ctx, "initialize", struct {
		ClientInfo map[string]string `json:"clientInfo"`
	}{
		map[string]string{"name": "aeon-agentd", "title": "AEON agentd", "version": "1"}}); err != nil {
		return err
	}
	if err := s.wire.Notify(ctx, "initialized", encode(struct{}{})); err != nil {
		return ErrUncertain
	}
	if err := s.authorize(ctx); err != nil {
		return err
	}
	p := threadParams{Cwd: s.spec.Cwd, Model: s.spec.Options.Model, ApprovalPolicy: s.grant.ApprovalPolicy,
		Sandbox: s.grant.Sandbox, Config: map[string]string{"service_tier": "default"}}
	method := "thread/start"
	if s.spec.ResumeRef != "" {
		method = "thread/resume"
		p.ThreadID = s.spec.ResumeRef
	} else {
		ephemeral := s.spec.Policy.LocalHistory == "disabled"
		p.Ephemeral = &ephemeral
	}
	raw, err := s.call(ctx, method, p)
	if err != nil {
		return err
	}
	var result threadResult
	if decode(raw, &result) != nil || !validRef(result.Thread.ID) ||
		(s.spec.ResumeRef != "" && result.Thread.ID != s.spec.ResumeRef) || result.Model != s.spec.Options.Model ||
		result.ModelProvider != "openai" || result.Cwd != s.spec.Cwd || result.ApprovalPolicy != s.grant.ApprovalPolicy ||
		!reflect.DeepEqual(result.Sandbox, s.sandbox()) {
		return ErrPolicy
	}
	// Ignore the resume response's old turns/items: they are not evidence from
	// this writer, input or generation and must not be imported as new finals.
	s.identity.NativeThreadRef = result.Thread.ID
	s.identity.NativeSessionRef = result.Thread.ID
	s.identity.ResumeCursor = result.Thread.ID
	return nil
}

func (s *transport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := s.wire.Call(op, method, encode(params))
	if err != nil {
		return nil, ErrUncertain
	}
	var obj map[string]json.RawMessage
	if decode(raw, &obj) != nil || obj == nil {
		return nil, ErrProtocol
	}
	return raw, nil
}

func (s *transport) authorize(ctx context.Context) error {
	if s.invalid != nil {
		return s.invalid
	}
	if s.closing {
		return ErrUncertain
	}
	proof := s.wire.Identity()
	if proof.Provider != s.identity.Provider || proof.RuntimeEpoch != s.identity.RuntimeEpoch ||
		proof.PID != s.identity.PID || proof.ProcessIdentityDigest != s.identity.ProcessIdentityDigest ||
		!proof.StartedAt.Equal(s.identity.StartedAt) {
		s.invalid = ErrBinding
		return ErrBinding
	}
	if !s.spec.Policy.ExpiresAt.After(s.clock.Now()) ||
		!s.identity.StartedAt.Add(time.Duration(s.spec.Policy.MaximumRuntimeSeconds)*time.Second).After(s.clock.Now()) || s.spec.Lifetime.Err() != nil {
		return ErrExpired
	}
	var result struct {
		Account *struct {
			Type  string `json:"type"`
			Email string `json:"email"`
		} `json:"account"`
		RequiresOpenAIAuth *bool `json:"requiresOpenaiAuth"`
	}
	raw, err := s.call(ctx, "account/read", struct {
		RefreshToken bool `json:"refreshToken"`
	}{false})
	if err != nil || decode(raw, &result) != nil || result.Account == nil || result.Account.Type != "chatgpt" ||
		result.RequiresOpenAIAuth == nil || !*result.RequiresOpenAIAuth ||
		!strings.EqualFold(strings.TrimSpace(result.Account.Email), strings.TrimSpace(s.grant.EnrolledEmail)) {
		s.invalid = ErrIdentity
		return ErrIdentity
	}
	g, err := s.grants.Authorize(ctx, cloneSpec(s.spec))
	if err != nil {
		s.invalid = ErrQualification
		return ErrQualification
	}
	if !reflect.DeepEqual(g, s.grant) {
		s.invalid = ErrPolicy
		return ErrPolicy
	}
	if !s.spec.Policy.ExpiresAt.After(s.clock.Now()) {
		return ErrExpired
	}
	return nil
}

func (s *transport) sandbox() sandboxPolicy {
	p := sandboxPolicy{Type: s.grant.Sandbox}
	if p.Type == "workspaceWrite" {
		p.WritableRoots = []string{s.spec.Profile.WorkspaceRoot}
		denyNetwork := false
		p.NetworkAccess = &denyNetwork
	}
	return p
}

func (s *transport) Send(ctx context.Context, input n.Input) (n.NativeEvidence, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sameBinding(input.ExpectedBinding, s.spec.Binding) {
		return n.NativeEvidence{}, ErrBinding
	}
	if !validText(input.Text) || input.RequestID == "" || input.InputID == "" || (input.Mode != "now" && input.Mode != "after_turn") {
		return n.NativeEvidence{}, ErrProtocol
	}
	if validateDTO("NativeInput", input) != nil {
		return n.NativeEvidence{}, ErrProtocol
	}
	if !input.ExpiresAt.After(s.clock.Now()) {
		return n.NativeEvidence{}, ErrExpired
	}
	if s.active && input.Mode == "after_turn" {
		return n.NativeEvidence{}, ErrBusy
	}
	if s.turns >= s.spec.Policy.MaximumTurns || s.tokens >= s.spec.Policy.MaximumTokens {
		return n.NativeEvidence{}, ErrExpired
	}
	if err := s.authorize(ctx); err != nil {
		return n.NativeEvidence{}, err
	}
	if !input.ExpiresAt.After(s.clock.Now()) {
		return n.NativeEvidence{}, ErrExpired
	}
	if s.active {
		// Exact expected-turn steering. No model/policy/effort overrides or
		// client receipt IDs are supported by this method's native schema.
		raw, err := s.call(ctx, "turn/steer", steerParams{s.identity.NativeThreadRef, s.turn, []textInput{{"text", input.Text}}})
		var response struct {
			TurnID string `json:"turnId"`
		}
		if err != nil || decode(raw, &response) != nil || response.TurnID != s.turn {
			s.invalid = ErrUncertain
			return n.NativeEvidence{}, ErrUncertain
		}
		// The native response acknowledges injection, not which input a future
		// answer consumed. Keep the original turn-start input for final evidence.
		return s.evidence("write_acknowledged", input, s.turn), nil
	}
	p := turnParams{ThreadID: s.identity.NativeThreadRef, Input: []textInput{{"text", input.Text}},
		Model: s.spec.Options.Model, Effort: s.spec.Options.Effort, ApprovalPolicy: s.grant.ApprovalPolicy, SandboxPolicy: s.sandbox()}
	raw, err := s.call(ctx, "turn/start", p)
	var result struct {
		Turn nativeTurn `json:"turn"`
	}
	if err != nil || decode(raw, &result) != nil || !validRef(result.Turn.ID) || result.Turn.Status != "inProgress" {
		s.invalid = ErrUncertain
		return n.NativeEvidence{}, ErrUncertain
	}
	s.turn, s.input, s.active, s.started = result.Turn.ID, input, true, false
	s.identity.NativeTurnRef = s.turn
	s.final = nil
	s.turns++
	return s.evidence("write_acknowledged", input, s.turn), nil
}

func (s *transport) Interrupt(ctx context.Context, input n.Interrupt) (n.NativeEvidence, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if validateDTO("NativeInterrupt", input) != nil {
		return n.NativeEvidence{}, ErrProtocol
	}
	if !sameBinding(input.ExpectedBinding, s.spec.Binding) {
		return n.NativeEvidence{}, ErrBinding
	}
	if !s.active || input.ExpectedTurnRef != s.turnRef() {
		return n.NativeEvidence{}, ErrTurn
	}
	if !input.ExpiresAt.After(s.clock.Now()) {
		return n.NativeEvidence{}, ErrExpired
	}
	if err := s.authorize(ctx); err != nil {
		return n.NativeEvidence{}, err
	}
	if !input.ExpiresAt.After(s.clock.Now()) {
		return n.NativeEvidence{}, ErrExpired
	}
	raw, err := s.call(ctx, "turn/interrupt", interruptParams{s.identity.NativeThreadRef, s.turn})
	var response map[string]json.RawMessage
	if err != nil || decode(raw, &response) != nil || len(response) != 0 {
		s.invalid = ErrUncertain
		return n.NativeEvidence{}, ErrUncertain
	}
	// Ack is not a completed turn or observed process exit.
	return n.NativeEvidence{Stage: "applied", RequestID: input.RequestID, TurnRef: s.turnRef(), SourceSequence: s.seq, ObservedAt: s.clock.Now()}, nil
}

func (s *transport) Decide(ctx context.Context, decision n.ApprovalDecision) (n.NativeEvidence, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if validateDTO("NativeApprovalDecision", decision) != nil {
		return n.NativeEvidence{}, ErrProtocol
	}
	if !sameBinding(decision.ExpectedBinding, s.spec.Binding) {
		return n.NativeEvidence{}, ErrBinding
	}
	p, ok := s.approvals[decision.ApprovalID]
	if !ok || !s.active || p.approval.TurnRef != s.turnRef() || !p.approval.ExpiresAt.After(s.clock.Now()) {
		return n.NativeEvidence{}, ErrExpired
	}
	if decision.EffectDigest != p.approval.EffectDigest || decision.Answer != nil ||
		(decision.Decision != "allow_once" && decision.Decision != "deny") {
		return n.NativeEvidence{}, ErrPolicy
	}
	a, ok := s.grants.(ApprovalAuthority)
	if !ok {
		return n.NativeEvidence{}, ErrQualification
	}
	if a.Approve(ctx, decision, p.approval) != nil {
		return n.NativeEvidence{}, ErrPolicy
	}
	if err := s.authorize(ctx); err != nil {
		return n.NativeEvidence{}, err
	}
	if !p.approval.ExpiresAt.After(s.clock.Now()) {
		return n.NativeEvidence{}, ErrExpired
	}
	nativeDecision := "decline"
	if decision.Decision == "allow_once" {
		nativeDecision = "accept"
	}
	delete(s.approvals, decision.ApprovalID) // uncertain writes are never retried
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if s.wire.Reply(op, p.rpcID, encode(map[string]string{"decision": nativeDecision})) != nil {
		s.invalid = ErrUncertain
		return n.NativeEvidence{}, ErrUncertain
	}
	return n.NativeEvidence{Stage: "write_acknowledged", RequestID: decision.RequestID, EffectDigest: decision.EffectDigest,
		TurnRef: s.turnRef(), SourceSequence: s.seq, ObservedAt: s.clock.Now()}, nil
}

func (s *transport) evidence(stage string, input n.Input, turn string) n.NativeEvidence {
	return n.NativeEvidence{Stage: stage, InputID: input.InputID, RequestID: input.RequestID,
		TurnRef: opaque(s.spec.Binding, "turn", turn), SourceSequence: s.seq, ObservedAt: s.clock.Now()}
}
func (s *transport) turnRef() string                { return opaque(s.spec.Binding, "turn", s.turn) }
func (s *transport) Identity() n.NativeIdentity     { s.mu.Lock(); defer s.mu.Unlock(); return s.identity }
func (s *transport) Events() <-chan n.ProviderEvent { return s.events }
func (s *transport) Capabilities() n.Capabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalid != nil || s.closing {
		return n.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: s.grant.QualificationDigest}
	}
	return n.Capabilities{Input: true, Steer: "native", Interrupt: true, Stop: true, Resume: s.spec.Policy.LocalHistory == "vendor_native",
		Expert: true, Approvals: true, CorrelatedReceipts: true, Quota: "stream", QualificationDigest: s.grant.QualificationDigest,
		EventsSupported: true, InterruptSupported: true, ChatSupported: true}
}

func (s *transport) validExit(e n.ExitEvidence) bool {
	return e.RootExited && e.GroupAbsent && e.ProcessIdentityDigest == s.identity.ProcessIdentityDigest && !e.ObservedAt.IsZero()
}
func (s *transport) Close(ctx context.Context) (n.ExitEvidence, error) {
	s.mu.Lock()
	s.closing = true
	clear(s.approvals)
	s.final = nil
	s.mu.Unlock()
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	exit, err := s.wire.Close(op)
	if err != nil || !s.validExit(exit) {
		return n.ExitEvidence{}, ErrUncertain
	}
	return exit, nil
}
func (s *transport) Wait(ctx context.Context) (n.ExitEvidence, error) {
	exit, err := s.wire.Wait(ctx)
	if err != nil || !s.validExit(exit) {
		return n.ExitEvidence{}, ErrUncertain
	}
	return exit, nil
}

func (s *transport) readFrames() {
	defer close(s.readerDone)
	defer close(s.events)
	for {
		select {
		case raw, ok := <-s.wire.Frames():
			if !ok {
				s.mu.Lock()
				if s.invalid == nil {
					s.invalid = ErrUncertain
					_ = s.structured("turn_state", "", "", "ownership_lost")
				}
				s.mu.Unlock()
				return
			}
			s.mu.Lock()
			if s.invalid == nil && !s.closing {
				ctx, cancel := context.WithTimeout(s.spec.Lifetime, 20*time.Second)
				if err := s.observe(ctx, raw); err != nil {
					s.invalid = err
					s.final = nil
					clear(s.approvals)
					_ = s.structured("turn_state", "", "", "ownership_lost")
					cancel()
					s.mu.Unlock()
					return
				}
				cancel()
			}
			s.mu.Unlock()
		case <-s.spec.Lifetime.Done():
			s.mu.Lock()
			s.invalid = ErrUncertain
			_ = s.structured("turn_state", "", "", "ownership_lost")
			s.mu.Unlock()
			return
		}
	}
}

func (s *transport) emit(event n.ProviderEvent) error {
	var schema, kind string
	var payload any
	switch {
	case event.Structured != nil:
		schema, kind, payload = "NativeStructuredFrame", "structured", event.Structured
	case event.Expert != nil:
		schema, kind, payload = "NativeExpertFrame", "expert", event.Expert
	case event.Approval != nil:
		schema, kind, payload = "NativeApproval", "approval", event.Approval
	case event.Quota != nil:
		schema, kind, payload = "NativeQuota", "quota", event.Quota
	case event.Receipt != nil:
		schema, kind, payload = "NativeReceipt", "receipt", event.Receipt
	case event.Final != nil:
		schema, payload = "NativeFinalPublish", event.Final
	default:
		return ErrProtocol
	}
	if validateDTO(schema, payload) != nil {
		return ErrProtocol
	}
	if kind != "" {
		frame := n.StreamEvent{Binding: s.spec.Binding, Sequence: "18446744073709551615", ObservedAt: s.clock.Now(), Kind: kind,
			Payload: encode(payload), DroppedFrames: "0", Generation: 9007199254740991}
		if len(encode(frame)) > n.ExportedFrameBytes {
			return ErrBuffer
		}
	}
	s.seq++
	event.SourceSequence = s.seq
	event.Identity = s.identity
	select {
	case s.events <- event:
		return nil
	default:
		s.final = nil
		return ErrBuffer
	}
}

func validateDTO(schema string, value any) error {
	v, err := n.NewValidator()
	if err != nil {
		return ErrProtocol
	}
	return v.ValidateJSON(schema, encode(value))
}
func (s *transport) structured(kind, text, ref, state string) error {
	f := n.StructuredFrame{Kind: kind}
	if text != "" {
		f.Text = &text
	}
	if ref != "" {
		f.Reference = &ref
	}
	if state != "" {
		f.State = &state
	}
	return s.emit(n.ProviderEvent{Structured: &f})
}

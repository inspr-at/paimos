// SPDX-License-Identifier: AGPL-3.0-only

package acp

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	nc "github.com/inspr-at/paimos/internal/nativecontract"
)

// Launcher is only a profile/codec extension of the existing process owner.
type Launcher struct {
	Qualification Qualification
	OpenWire      OpenWire
	Now           func() time.Time
}

var _ nc.Launcher = (*Launcher)(nil)
var _ nc.NativeTransport = (*Transport)(nil)

type turn struct {
	ref        string
	done       chan struct{}
	cancelling bool
	stopReason string
}

type Transport struct {
	q         Qualification
	spec      nc.LaunchSpec
	wire      Wire
	now       func() time.Time
	base      nc.NativeIdentity
	mu        sync.Mutex
	controlMu sync.Mutex // Turn completion/start cannot cross an interrupt write.
	session   string
	active    *turn
	failure   error
	closing   bool
	ended     bool
	sequence  uint64
	turns     int64
	next      atomic.Int64
	gate      chan struct{}
	waiters   chan struct{}
	stop      chan struct{}
	stopOnce  sync.Once
	events    chan nc.ProviderEvent
}

func (l *Launcher) Open(ctx context.Context, spec nc.LaunchSpec) (nc.NativeTransport, error) {
	now := l.Now
	if now == nil {
		now = time.Now
	}
	q := l.Qualification
	if !supported(q.Harness) {
		return nil, Refuse(nc.ReasonUnsupported)
	}
	if err := ValidateSpec(q, spec, now(), Protocol); err != nil {
		return nil, err
	}
	args, err := arguments(q, spec)
	if err != nil {
		return nil, err
	}
	if l.OpenWire == nil {
		return nil, Refuse(nc.ReasonQualificationMissing)
	}
	spec = CloneSpec(spec)
	wire, err := l.OpenWire(ctx, CloneSpec(spec), args)
	if wire == nil {
		return nil, Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	p := &Transport{q: q, spec: spec, wire: wire, now: now, base: wire.Identity(), gate: make(chan struct{}, 1), waiters: make(chan struct{}, 32), stop: make(chan struct{}), events: make(chan nc.ProviderEvent, 64)}
	if err != nil {
		p.failure = Refuse(nc.ReasonOwnershipUnconfirmed)
		return p, p.failure
	}
	// Return a non-nil transport on a post-fork failure: the owner must retain
	// occupancy until Close/Wait proves exit. Never lose a possible child handle.
	if err = ValidateIdentity(p.base, q.Harness, spec); err != nil {
		p.failure = err
		return p, err
	}
	wire.Observe(p.observe)
	if err = p.initialize(ctx); err != nil {
		p.mu.Lock()
		p.failure = err
		p.mu.Unlock()
		return p, err
	}
	return p, nil
}

func (p *Transport) rpc(ctx context.Context, method string, params any) (json.RawMessage, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := CheckWire(ctx, p.wire, p.spec, p.base, p.now()); err != nil {
		return nil, "", err
	}
	id := p.next.Add(1)
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, "", Refuse(nc.ReasonInvalidInput)
	}
	reply, err := p.wire.Request(ctx, CloneSpec(p.spec), raw, nil)
	if err != nil {
		return nil, "", Refuse(nc.ReasonInputOutcomeUnconfirmed)
	}
	result, err := response(reply, id)
	return result, strconv.FormatInt(id, 10), err
}

func response(raw json.RawMessage, id int64) (json.RawMessage, error) {
	var r struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := Decode(raw, &r); err != nil {
		return nil, err
	}
	if r.JSONRPC != "2.0" || r.ID != id || len(r.Result) == 0 || string(r.Result) == "null" || len(r.Error) > 0 && string(r.Error) != "null" {
		return nil, Refuse(nc.ReasonInputOutcomeUnconfirmed)
	}
	return r.Result, nil
}

func (p *Transport) initialize(ctx context.Context) error {
	raw, _, err := p.rpc(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "aeon-agentd", "version": "1"}, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}})
	var init struct {
		Version int `json:"protocolVersion"`
	}
	if err != nil {
		return err
	}
	if Decode(raw, &init) != nil || init.Version != 1 {
		return Refuse(nc.ReasonPinInvalid)
	}
	raw, _, err = p.rpc(ctx, "session/new", map[string]any{"cwd": p.spec.Cwd, "mcpServers": []any{}})
	var session struct {
		ID     string `json:"sessionId"`
		Models struct {
			Current string `json:"currentModelId"`
		} `json:"models"`
	}
	if err != nil {
		return err
	}
	if Decode(raw, &session) != nil || !ValidID(session.ID, 128) {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	p.mu.Lock()
	p.session = session.ID
	p.mu.Unlock()
	if p.q.Harness == "opencode" {
		if err := p.config(ctx, "model", p.spec.Options.Model); err != nil {
			return err
		}
		if p.spec.Options.Effort != nil {
			return p.config(ctx, "effort", *p.spec.Options.Effort)
		}
	} else if session.Models.Current != p.spec.Options.Model {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	return nil
}

func (p *Transport) config(ctx context.Context, name, value string) error {
	raw, _, err := p.rpc(ctx, "session/set_config_option", map[string]string{"sessionId": p.session, "configId": name, "value": value})
	if err != nil {
		return err
	}
	var result struct {
		Options []struct {
			ID    string `json:"id"`
			Value string `json:"currentValue"`
		} `json:"configOptions"`
	}
	if Decode(raw, &result) != nil || len(result.Options) > 64 {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	matched, model := false, false
	for _, option := range result.Options {
		matched = matched || option.ID == name && option.Value == value
		model = model || option.ID == "model" && option.Value == p.spec.Options.Model
	}
	if !matched || !model {
		return Refuse(nc.ReasonIdentityMismatch)
	}
	return nil
}

func (p *Transport) Send(ctx context.Context, input nc.Input) (nc.NativeEvidence, error) {
	if err := ValidateInput(p.spec.Binding, input, p.now()); err != nil {
		return nc.NativeEvidence{}, err
	}
	select {
	case p.waiters <- struct{}{}:
		defer func() { <-p.waiters }()
	default:
		return nc.NativeEvidence{}, Refuse(nc.ReasonBufferFull)
	}
	if input.Mode == "now" {
		select {
		case p.gate <- struct{}{}:
		default:
			return nc.NativeEvidence{}, Refuse(nc.ReasonUnsupported)
		}
	} else {
		select {
		case p.gate <- struct{}{}:
		case <-ctx.Done():
			return nc.NativeEvidence{}, ctx.Err()
		case <-p.stop:
			return nc.NativeEvidence{}, Refuse(nc.ReasonOwnershipUnconfirmed)
		case <-p.spec.Lifetime.Done():
			return nc.NativeEvidence{}, Refuse(nc.ReasonOwnershipUnconfirmed)
		}
	}
	defer func() { <-p.gate }()
	// Queueing is no delivery receipt. Revalidate expiry, identity and policy
	// after the clean boundary, immediately before the owner performs the write.
	if err := ValidateInput(p.spec.Binding, input, p.now()); err != nil {
		return nc.NativeEvidence{}, err
	}
	if err := CheckWire(ctx, p.wire, p.spec, p.base, p.now()); err != nil {
		return nc.NativeEvidence{}, err
	}
	p.controlMu.Lock()
	p.mu.Lock()
	if p.failure != nil || p.closing {
		err := p.failure
		p.mu.Unlock()
		if err == nil {
			err = Refuse(nc.ReasonOwnershipUnconfirmed)
		}
		p.controlMu.Unlock()
		return nc.NativeEvidence{}, err
	}
	if p.turns >= p.spec.Policy.MaximumTurns {
		p.mu.Unlock()
		p.controlMu.Unlock()
		return nc.NativeEvidence{}, Refuse(nc.ReasonUnsupported)
	}
	id := p.next.Add(1)
	t := &turn{ref: strconv.FormatInt(id, 10), done: make(chan struct{})}
	p.active = t
	p.turns++
	p.mu.Unlock()
	// Keep the turn-start fence until the request has actually been written;
	// otherwise an interrupt could cancel idle just before the prompt starts.
	var written sync.Once
	releaseWrite := func() { written.Do(p.controlMu.Unlock) }
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/prompt", "params": map[string]any{"sessionId": p.session, "prompt": []map[string]string{{"type": "text", "text": input.Text}}}})
	deadline := p.base.StartedAt.Add(time.Duration(p.spec.Policy.MaximumRuntimeSeconds) * time.Second)
	if p.spec.Policy.ExpiresAt.Before(deadline) {
		deadline = p.spec.Policy.ExpiresAt
	}
	if input.ExpiresAt.Before(deadline) {
		deadline = input.ExpiresAt
	}
	op, cancel := context.WithTimeout(ctx, deadline.Sub(p.now()))
	defer cancel()
	reply, err := p.wire.Request(op, CloneSpec(p.spec), raw, releaseWrite)
	releaseWrite()
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if err == nil {
		var body json.RawMessage
		body, err = response(reply, id)
		if err == nil {
			err = Decode(body, &result)
		}
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	var evidence nc.NativeEvidence
	switch {
	case err != nil:
		p.failure = Refuse(nc.ReasonInputOutcomeUnconfirmed)
		err = p.failure
	case p.failure != nil:
		err = p.failure
	case p.closing:
		err = Refuse(nc.ReasonOwnershipUnconfirmed)
	case result.StopReason != "end_turn" && !(t.cancelling && result.StopReason == "cancelled"):
		p.failure = Refuse(nc.ReasonInputOutcomeUnconfirmed)
		err = p.failure
	default:
		t.stopReason = result.StopReason
		p.emitLocked(&nc.StructuredFrame{Kind: "turn_state", State: str("idle")})
		err = p.failure
		if err == nil {
			// Native prompt completion proves this request was consumed. Chunk
			// text is NEVER assembled into a FinalPublish, even on end_turn.
			evidence = nc.NativeEvidence{Stage: "turn_completed", InputID: input.InputID, RequestID: input.RequestID, TurnRef: t.ref, SourceSequence: p.sequence, ObservedAt: p.now()}
		}
	}
	// Retire the turn before releasing either fence, so an interrupt cannot
	// match a completed turn in the cleanup window or mistake end_turn for
	// cancellation. Waiters observe the native stop reason under the same mu.
	p.active = nil
	close(t.done)
	return evidence, err
}

func (p *Transport) Interrupt(ctx context.Context, input nc.Interrupt) (nc.NativeEvidence, error) {
	if !p.Capabilities().Interrupt {
		return nc.NativeEvidence{}, Refuse(nc.ReasonUnsupported)
	}
	if err := ValidateDTO("NativeInterrupt", input); err != nil {
		return nc.NativeEvidence{}, err
	}
	if !p.spec.Binding.Equal(input.ExpectedBinding) {
		return nc.NativeEvidence{}, Refuse(nc.ReasonWrongGeneration)
	}
	if !p.now().Before(input.ExpiresAt) {
		return nc.NativeEvidence{}, Refuse(nc.ReasonGrantExpired)
	}
	if err := CheckWire(ctx, p.wire, p.spec, p.base, p.now()); err != nil {
		return nc.NativeEvidence{}, err
	}
	p.controlMu.Lock()
	if err := CheckWire(ctx, p.wire, p.spec, p.base, p.now()); err != nil {
		p.controlMu.Unlock()
		return nc.NativeEvidence{}, err
	}
	if !p.now().Before(input.ExpiresAt) {
		p.controlMu.Unlock()
		return nc.NativeEvidence{}, Refuse(nc.ReasonGrantExpired)
	}
	p.mu.Lock()
	t := p.active
	if p.closing || p.failure != nil || t == nil || t.ref != input.ExpectedTurnRef {
		p.mu.Unlock()
		p.controlMu.Unlock()
		return nc.NativeEvidence{}, Refuse(nc.ReasonIdentityMismatch)
	}
	t.cancelling = true
	p.mu.Unlock()
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": p.session}})
	if err := p.wire.Notify(op, CloneSpec(p.spec), raw); err != nil {
		p.mu.Lock()
		p.failure = Refuse(nc.ReasonInputOutcomeUnconfirmed)
		p.mu.Unlock()
		p.controlMu.Unlock()
		return nc.NativeEvidence{}, Refuse(nc.ReasonInputOutcomeUnconfirmed)
	}
	p.controlMu.Unlock()
	select {
	case <-t.done:
	case <-op.Done():
		return nc.NativeEvidence{}, Refuse(nc.ReasonInputOutcomeUnconfirmed)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return nc.NativeEvidence{}, p.failure
	}
	if p.closing {
		return nc.NativeEvidence{}, Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	if t.stopReason != "cancelled" {
		return nc.NativeEvidence{}, Refuse(nc.ReasonInputOutcomeUnconfirmed)
	}
	return nc.NativeEvidence{Stage: "applied", RequestID: input.RequestID, TurnRef: t.ref, SourceSequence: p.sequence, ObservedAt: p.now()}, nil
}

func (p *Transport) Decide(context.Context, nc.ApprovalDecision) (nc.NativeEvidence, error) {
	return nc.NativeEvidence{}, Refuse(nc.ReasonUnsupported)
}
func (p *Transport) Capabilities() nc.Capabilities   { return Matrix(p.q).Capabilities }
func (p *Transport) Events() <-chan nc.ProviderEvent { return p.events }
func (p *Transport) Identity() nc.NativeIdentity {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.base
	id.NativeSessionRef = p.session
	if p.active != nil {
		id.NativeTurnRef = p.active.ref
	}
	return id
}

func (p *Transport) Close(ctx context.Context) (nc.ExitEvidence, error) {
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stop) })
	exit, err := p.wire.Close(ctx)
	return p.exit(exit, err)
}
func (p *Transport) Wait(ctx context.Context) (nc.ExitEvidence, error) {
	exit, err := p.wire.Wait(ctx)
	return p.exit(exit, err)
}

func (p *Transport) exit(e nc.ExitEvidence, err error) (nc.ExitEvidence, error) {
	e, err = ConfirmExit(p.base, e, err)
	if err == nil {
		p.mu.Lock()
		p.closing = true
		if !p.ended {
			p.ended = true
			close(p.events)
		}
		p.mu.Unlock()
		p.stopOnce.Do(func() { close(p.stop) })
	}
	return e, err
}

func str(value string) *string { return &value }
func (p *Transport) emitLocked(frame *nc.StructuredFrame) {
	if p.ended {
		return
	}
	identity := p.base
	identity.NativeSessionRef = p.session
	if p.active != nil {
		identity.NativeTurnRef = p.active.ref
	}
	event := nc.ProviderEvent{SourceSequence: p.sequence + 1, Identity: identity, Structured: frame}
	select {
	case p.events <- event:
		p.sequence++
	default:
		p.failure = Refuse(nc.ReasonBufferFull)
	}
}

func (p *Transport) observe(raw json.RawMessage) {
	var frame struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Session string `json:"sessionId"`
			Update  struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				CallID string `json:"toolCallId"`
				Status string `json:"status"`
			} `json:"update"`
		} `json:"params"`
	}
	if Decode(raw, &frame) != nil || frame.JSONRPC != "2.0" {
		return
	}
	p.mu.Lock()
	if p.closing || p.failure != nil || p.session == "" || frame.Params.Session != p.session || p.active == nil {
		p.mu.Unlock()
		return
	}
	if frame.Method == "session/request_permission" || len(frame.ID) > 0 {
		p.failure = Refuse(nc.ReasonUnsupported)
		p.emitLocked(&nc.StructuredFrame{Kind: "turn_state", State: str("requires_action")})
		p.mu.Unlock()
		// Explicitly cancel a permission prompt; NEVER choose allow-always or
		// infer a human decision from text. Other client methods are refused.
		if len(frame.ID) > 0 && len(frame.ID) <= 130 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			reply := map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": map[string]any{"code": -32601, "message": "client method unavailable"}}
			if frame.Method == "session/request_permission" {
				delete(reply, "error")
				reply["result"] = map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
			}
			encoded, _ := json.Marshal(reply)
			_ = p.wire.Notify(ctx, CloneSpec(p.spec), encoded)
		}
		return
	}
	defer p.mu.Unlock()
	if frame.Method != "session/update" {
		return
	}
	u := frame.Params.Update
	switch u.Kind {
	case "agent_message_chunk":
		if u.Content.Type == "text" && ValidText(u.Content.Text) {
			// Text may contain whitespace/newlines; only native assistant text is
			// exported. Raw blocks, thought chunks and tool bodies stay local.
			p.emitLocked(&nc.StructuredFrame{Kind: "assistant_delta", Text: str(u.Content.Text), Reference: str(Digest(p.spec.Binding.RuntimeEpoch + "\x00" + p.active.ref))})
		}
	case "tool_call", "tool_call_update":
		if ValidID(u.CallID, 256) && (u.Status == "pending" || u.Status == "in_progress" || u.Status == "completed" || u.Status == "failed") {
			p.emitLocked(&nc.StructuredFrame{Kind: "tool_status", Reference: str(Digest(p.spec.Binding.RuntimeEpoch + "\x00tool\x00" + u.CallID)), State: str(u.Status)})
		}
	}
}

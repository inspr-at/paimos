// SPDX-License-Identifier: AGPL-3.0-only

package pi

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	nc "github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeproviders/acp"
)

type Launcher struct {
	Qualification acp.Qualification
	OpenWire      acp.OpenWire
	Now           func() time.Time
}

var _ nc.Launcher = (*Launcher)(nil)
var _ nc.NativeTransport = (*Transport)(nil)

type Transport struct {
	q                       acp.Qualification
	spec                    nc.LaunchSpec
	wire                    acp.Wire
	base                    nc.NativeIdentity
	now                     func() time.Time
	provider, model, effort string
	control                 chan struct{}
	next                    atomic.Int64
	mu                      sync.Mutex
	session                 string
	sequence                uint64
	sends                   int64
	failure                 error
	closing                 bool
	ended                   bool
	events                  chan nc.ProviderEvent
}

func (l *Launcher) Open(ctx context.Context, spec nc.LaunchSpec) (nc.NativeTransport, error) {
	if l.Qualification.Harness != "pi" {
		return nil, acp.Refuse(nc.ReasonUnsupported)
	}
	now := l.Now
	if now == nil {
		now = time.Now
	}
	if err := acp.ValidateSpec(l.Qualification, spec, now(), Protocol); err != nil {
		return nil, err
	}
	args, provider, model, effort, err := arguments(spec)
	if err != nil {
		return nil, err
	}
	if l.OpenWire == nil {
		return nil, acp.Refuse(nc.ReasonQualificationMissing)
	}
	spec = acp.CloneSpec(spec)
	wire, err := l.OpenWire(ctx, acp.CloneSpec(spec), args)
	if wire == nil {
		return nil, acp.Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	p := &Transport{q: l.Qualification, spec: spec, wire: wire, base: wire.Identity(), now: now, provider: provider, model: model, effort: effort, control: make(chan struct{}, 1), events: make(chan nc.ProviderEvent, 64)}
	if err != nil {
		p.failure = acp.Refuse(nc.ReasonOwnershipUnconfirmed)
		return p, p.failure
	}
	if err = acp.ValidateIdentity(p.base, "pi", spec); err != nil {
		p.failure = err
		return p, err
	}
	wire.Observe(p.observe)
	if err = p.state(ctx); err != nil {
		p.mu.Lock()
		p.failure = err
		p.mu.Unlock()
		return p, err
	}
	return p, nil
}

func (p *Transport) rpc(ctx context.Context, method string, fields map[string]any) (json.RawMessage, error) {
	if err := acp.CheckWire(ctx, p.wire, p.spec, p.base, p.now()); err != nil {
		return nil, err
	}
	id := strconv.FormatInt(p.next.Add(1), 10)
	frame := map[string]any{"id": id, "type": method}
	for key, value := range fields {
		frame[key] = value
	}
	raw, _ := json.Marshal(frame)
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reply, err := p.wire.Request(op, acp.CloneSpec(p.spec), raw, nil)
	if err != nil {
		return nil, acp.Refuse(nc.ReasonInputOutcomeUnconfirmed)
	}
	var r struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Command string          `json:"command"`
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := acp.Decode(reply, &r); err != nil {
		return nil, err
	}
	if r.Type != "response" || r.ID != id || r.Command != method || r.Success == nil || !*r.Success {
		return nil, acp.Refuse(nc.ReasonInputOutcomeUnconfirmed)
	}
	return r.Data, nil
}

func (p *Transport) state(ctx context.Context) error {
	raw, err := p.rpc(ctx, "get_state", nil)
	if err != nil {
		return err
	}
	var state struct {
		Session string `json:"sessionId"`
		Model   struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		} `json:"model"`
		Effort string `json:"thinkingLevel"`
	}
	if acp.Decode(raw, &state) != nil || !acp.ValidID(state.Session, 128) || state.Model.Provider != p.provider || state.Model.ID != p.model || state.Effort != p.effort {
		return acp.Refuse(nc.ReasonIdentityMismatch)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != "" && p.session != state.Session {
		return acp.Refuse(nc.ReasonIdentityMismatch)
	}
	p.session = state.Session
	return nil
}

// The frozen C0 contract pairs Input with ChatSupported; neither is qualified.
func (p *Transport) Send(ctx context.Context, input nc.Input) (nc.NativeEvidence, error) {
	return nc.NativeEvidence{}, acp.Refuse(nc.ReasonUnsupported)
}

// NextStep is an optional local worker-control extension, not the C0 chat port.
// Success proves only a native command acknowledgement. I2 must retain current
// bounded worker authority; it cannot expose this as correlated NativeInput.
func (p *Transport) NextStep(ctx context.Context, input nc.Input) (nc.NativeEvidence, error) {
	if err := acp.ValidateInput(p.spec.Binding, input, p.now()); err != nil {
		return nc.NativeEvidence{}, err
	}
	select {
	case p.control <- struct{}{}:
		defer func() { <-p.control }()
	case <-ctx.Done():
		return nc.NativeEvidence{}, ctx.Err()
	case <-p.spec.Lifetime.Done():
		return nc.NativeEvidence{}, acp.Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	p.mu.Lock()
	if p.failure != nil || p.closing {
		err := p.failure
		p.mu.Unlock()
		if err == nil {
			err = acp.Refuse(nc.ReasonOwnershipUnconfirmed)
		}
		return nc.NativeEvidence{}, err
	}
	if p.sends >= p.spec.Policy.MaximumTurns {
		p.mu.Unlock()
		return nc.NativeEvidence{}, acp.Refuse(nc.ReasonUnsupported)
	}
	p.mu.Unlock()
	if err := p.state(ctx); err != nil {
		p.mu.Lock()
		p.failure = err
		p.mu.Unlock()
		return nc.NativeEvidence{}, err
	}
	if err := acp.ValidateInput(p.spec.Binding, input, p.now()); err != nil {
		return nc.NativeEvidence{}, err
	}
	behavior := "steer"
	if input.Mode == "after_turn" {
		behavior = "followUp"
	}
	_, err := p.rpc(ctx, "prompt", map[string]any{"message": input.Text, "streamingBehavior": behavior})
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.failure = acp.Refuse(nc.ReasonInputOutcomeUnconfirmed)
		return nc.NativeEvidence{}, p.failure
	}
	if p.failure != nil {
		return nc.NativeEvidence{}, p.failure
	}
	if p.closing {
		return nc.NativeEvidence{}, acp.Refuse(nc.ReasonOwnershipUnconfirmed)
	}
	p.sends++
	return nc.NativeEvidence{Stage: "write_acknowledged", InputID: input.InputID, RequestID: input.RequestID, SourceSequence: p.sequence, ObservedAt: p.now()}, nil
}

// Abort/clear_queue can lose input without durable held-queue reconciliation.
// This slice does not fabricate interruption or resume evidence.
func (p *Transport) Interrupt(context.Context, nc.Interrupt) (nc.NativeEvidence, error) {
	return nc.NativeEvidence{}, acp.Refuse(nc.ReasonUnsupported)
}
func (p *Transport) Decide(context.Context, nc.ApprovalDecision) (nc.NativeEvidence, error) {
	return nc.NativeEvidence{}, acp.Refuse(nc.ReasonUnsupported)
}
func (p *Transport) Capabilities() nc.Capabilities   { return Matrix(p.q).Capabilities }
func (p *Transport) Events() <-chan nc.ProviderEvent { return p.events }
func (p *Transport) Identity() nc.NativeIdentity {
	p.mu.Lock()
	defer p.mu.Unlock()
	identity := p.base
	identity.NativeSessionRef = p.session
	return identity
}
func (p *Transport) Close(ctx context.Context) (nc.ExitEvidence, error) {
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
	exit, err := p.wire.Close(ctx)
	return p.exit(exit, err)
}
func (p *Transport) Wait(ctx context.Context) (nc.ExitEvidence, error) {
	exit, err := p.wire.Wait(ctx)
	return p.exit(exit, err)
}

func (p *Transport) exit(e nc.ExitEvidence, err error) (nc.ExitEvidence, error) {
	e, err = acp.ConfirmExit(p.base, e, err)
	if err == nil {
		p.mu.Lock()
		p.closing = true
		if !p.ended {
			p.ended = true
			close(p.events)
		}
		p.mu.Unlock()
	}
	return e, err
}

func text(value string) *string { return &value }
func (p *Transport) observe(raw json.RawMessage) {
	var frame struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Delta   struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
		CallID  string `json:"toolCallId"`
		IsError *bool  `json:"isError"`
	}
	if acp.Decode(raw, &frame) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing || p.failure != nil || p.session == "" {
		return
	}
	var out *nc.StructuredFrame
	switch frame.Type {
	case "agent_start":
		out = &nc.StructuredFrame{Kind: "turn_state", State: text("running")}
	case "agent_end":
		out = &nc.StructuredFrame{Kind: "turn_state", State: text("idle")}
	case "message_update":
		var message struct {
			Role     string `json:"role"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		}
		if acp.Decode(frame.Message, &message) != nil {
			return
		}
		// Partial events often omit provider/model. The owner verifies the
		// exact native connection and effective state; reject explicit drift.
		if message.Role != "assistant" || message.Provider != "" && message.Provider != p.provider || message.Model != "" && message.Model != p.model {
			return
		}
		if frame.Delta.Type == "text_delta" && acp.ValidText(frame.Delta.Delta) {
			out = &nc.StructuredFrame{Kind: "assistant_delta", Text: text(frame.Delta.Delta)}
		}
	case "tool_execution_start", "tool_execution_end":
		if !acp.ValidID(frame.CallID, 256) {
			return
		}
		state := "in_progress"
		if frame.Type == "tool_execution_end" {
			if frame.IsError == nil {
				return
			}
			state = "completed"
			if *frame.IsError {
				state = "failed"
			}
		}
		out = &nc.StructuredFrame{Kind: "tool_status", Reference: text(acp.Digest(p.spec.Binding.RuntimeEpoch + "\x00tool\x00" + frame.CallID)), State: text(state)}
	case "extension_ui_request":
		p.failure = acp.Refuse(nc.ReasonUnsupported)
		out = &nc.StructuredFrame{Kind: "turn_state", State: text("requires_action")}
	default:
		return // Includes uncorrelated message_end, usage and raw finals.
	}
	if out == nil {
		return
	}
	identity := p.base
	identity.NativeSessionRef = p.session
	select {
	case p.events <- nc.ProviderEvent{SourceSequence: p.sequence + 1, Identity: identity, Structured: out}:
		p.sequence++
	default:
		p.failure = acp.Refuse(nc.ReasonBufferFull)
	}
}

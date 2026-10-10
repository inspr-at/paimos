// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

// ACPAdapter adds Gemini and OpenCode to the existing owned wire transport.
// Local auth is the explicitly paired profile. Version checks establish launcher
// availability only; session/new establishes an ACP session. Provider authentication
// may fail at prompt time and is never inferred from a successful version probe.
type ACPAdapter struct {
	Harness     string
	Path        string
	Homes       map[string]string
	Nodes       map[string]harnesslaunch.Node
	IdleTimeout time.Duration
}

func NewGeminiAdapter(path string, homes map[string]string) *ACPAdapter {
	return &ACPAdapter{Harness: Gemini, Path: path, Homes: homes}
}
func NewOpenCodeAdapter(path string, homes map[string]string) *ACPAdapter {
	return &ACPAdapter{Harness: OpenCode, Path: path, Homes: homes}
}
func (a *ACPAdapter) Name() string { return a.Harness }

func (a *ACPAdapter) environment(key string) ([]string, error) {
	home, err := localHome(a.Homes, key)
	if err != nil || a.Harness != Gemini && a.Harness != OpenCode {
		return nil, harnesslaunch.ErrStart
	}
	// No inherited provider credentials, runtime injections, shell PATH or
	// daemon key. The vendor retains its paired, private HOME login profile.
	env := []string{"HOME=" + home, "LANG=C", "LC_ALL=C"}
	if a.Harness == Gemini {
		env = append(env, "GEMINI_CLI_HOME="+home, "GEMINI_CLI_NO_RELAUNCH=true")
	} else {
		env = append(env, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
			"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
			"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
			"XDG_CACHE_HOME="+filepath.Join(home, ".cache"))
	}
	return harnesslaunch.Environment(env, a.Nodes[key].Path), nil
}

func (a *ACPAdapter) Probe(ctx context.Context, key string) bool {
	return a.ProbeStatus(ctx, key).OK
}

func (a *ACPAdapter) ProbeStatus(ctx context.Context, key string) ProbeStatus {
	env, err := a.environment(key)
	if err != nil || harnesslaunch.Validate(a.Path, a.Nodes[key].Path) != nil {
		return ProbeStatus{Failure: ProbeLaunchFailed}
	}
	raw, code, err := probeRun(ctx, a.Path, env, "--version")
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return ProbeStatus{Failure: ProbeTimeout}
	case errors.Is(err, harnesslaunch.ErrStart), code != 0:
		return ProbeStatus{Failure: ProbeLaunchFailed}
	case err != nil, strings.TrimSpace(string(raw)) == "":
		return ProbeStatus{Failure: ProbeProtocol}
	}
	// Neither harness has a qualified quota-neutral sign-in probe. A version
	// response proves no account identity, authentication or execution health.
	return ProbeStatus{Failure: ProbeUnverified}
}
func (a *ACPAdapter) CapacitySupport(string) string {
	return "not available: vendor quota-neutral capacity API unqualified"
}
func (a *ACPAdapter) CanCaptureCapacity(string) bool                             { return false }
func (a *ACPAdapter) CaptureCapacity(context.Context, string) []capacity.Reading { return nil }

type acpTurn struct {
	id       string
	hadTools bool
	accepted chan struct{}
	once     sync.Once
	err      error
}

func (t *acpTurn) accept() { t.once.Do(func() { close(t.accepted) }) }

func boundedACPAction(op string) string {
	if validChatID(op, 32) {
		return op
	}
	return "unsupported"
}

type acpProcess struct {
	*wireProcess
	mu                               sync.Mutex
	harness, model                   string
	persistent                       bool
	turn                             *acpTurn
	idle                             chan struct{}
	done                             chan struct{}
	closed                           bool
	terminalErr                      error
	timer                            *time.Timer
	idleTimeout                      time.Duration
	idleGeneration                   uint64
	turnTimeout                      time.Duration
	beforePrompt                     func() error
	turnComplete                     func() error
	costMicros                       int64
	input, output, cached, reasoning int64
}

func (p *acpProcess) finishLocked(err error) {
	if p.closed {
		return
	}
	p.closed, p.terminalErr = true, err
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.turn != nil {
		p.turn.err = err
		p.turn.accept()
	}
	close(p.done)
}

func (p *acpProcess) Wait() (err error) {
	defer func() { err = errors.Join(err, p.finishReader()) }()
	select {
	case <-p.done:
	case <-p.waitDone:
		// Drain buffered terminal frames before deciding whether the turn ended.
		if drain := p.finishReader(); drain != nil {
			return drain
		}
		p.mu.Lock()
		if !p.closed {
			p.finishLocked(errors.New("ACP child exited before session completion"))
		}
		p.mu.Unlock()
	case <-p.readDone:
		p.mu.Lock()
		if !p.closed {
			p.finishLocked(errors.New("ACP event stream ended before session completion"))
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	err = p.terminalErr
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return errors.Join(err, p.wireProcess.Stop(ctx))
}

func (p *acpProcess) promptLocked(ctx context.Context, text string) (*acpTurn, error) {
	if p.closed {
		return nil, ErrNotOwned
	}
	if p.turn != nil {
		return nil, ErrUnsupported
	}
	if len(text) == 0 || len(text) > rules.MaxBytes+64<<10 {
		return nil, errors.New("ACP prompt exceeds the input bound")
	}
	if p.beforePrompt != nil {
		if err := p.beforePrompt(); err != nil {
			p.finishLocked(err)
			return nil, err
		}
	}
	p.idleGeneration++
	if p.timer != nil {
		p.timer.Stop()
	}
	t := &acpTurn{id: strconv.FormatInt(p.next.Add(1), 10), accepted: make(chan struct{})}
	p.turn, p.idle = t, make(chan struct{})
	if p.turnTimeout > 0 {
		p.timer = time.AfterFunc(p.turnTimeout, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.turn == t {
				p.finishLocked(errors.New("ACP turn exceeded its deadline"))
			}
		})
	}
	n, _ := strconv.ParseInt(t.id, 10, 64)
	if err := p.sendContext(ctx, map[string]any{"jsonrpc": "2.0", "id": n, "method": "session/prompt", "params": map[string]any{
		"sessionId": p.sessionID, "prompt": []map[string]string{{"type": "text", "text": text}},
	}}); err != nil {
		p.finishLocked(err)
		return nil, err
	}
	p.observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1, Activity: "busy"})
	u := chatState("running")
	p.observe(AdapterEvent{Chat: &u})
	return t, nil
}

func (p *acpProcess) Control(ctx context.Context, op, text string) error {
	if op != "inbox" && op != "steer" && op != "interrupt" {
		return fmt.Errorf("%w: ACP has no %s action", ErrUnsupported, boundedACPAction(op))
	}
	if (op == "inbox" || op == "steer") && (len(text) == 0 || len(text) > 64<<10) || op == "interrupt" && text != "" {
		return errors.New("invalid ACP control body")
	}
	// Grok's qualified native transport has no cancellation acknowledgement.
	// Gemini steering never invokes cancel implicitly: replacement is lossy
	// and requires a separately labelled, explicitly authorized product action.
	if p.harness == Grok && op == "interrupt" {
		return fmt.Errorf("%w: native Grok interrupt is unavailable; use Stop", ErrUnsupported)
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrNotOwned
	}
	if op == "interrupt" {
		if p.turn == nil {
			p.mu.Unlock()
			return ErrNotOwned
		}
		idle := p.idle
		err := p.sendContext(ctx, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": p.sessionID}})
		p.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-idle:
			return nil
		case <-p.done:
			p.mu.Lock()
			err := p.terminalErr
			p.mu.Unlock()
			if err != nil {
				return ErrControlUnconfirmed
			}
			return nil
		case <-ctx.Done():
			return ErrControlUnconfirmed
		}
	}
	if p.turn != nil {
		p.mu.Unlock()
		return ErrUnsupported
	}
	t, err := p.promptLocked(ctx, text)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-t.accepted:
		p.mu.Lock()
		err = t.err
		p.mu.Unlock()
		return err
	case <-ctx.Done():
		return ErrControlUnconfirmed
	case <-p.readDone:
		return ErrControlUnconfirmed
	}
}

func (p *acpProcess) event(raw json.RawMessage) {
	var frame struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
		Params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind string `json:"sessionUpdate"`
				Cost *struct {
					Amount   json.RawMessage `json:"amount"`
					Currency string          `json:"currency"`
				} `json:"cost"`
			} `json:"update"`
		} `json:"params"`
	}
	if decodeProbeJSON(raw, &frame) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if frame.Method != "" {
		if frame.Params.SessionID != p.sessionID {
			p.chatDropped.Add(1)
			return
		}
		if frame.Method == "session/request_permission" {
			p.observeACPChat(raw)
			// Never select a permission option on behalf of a person. Reject
			// explicitly so the owned child does not hang on an unanswered call.
			_ = p.send(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}})
			p.finishLocked(errors.New("ACP permission requires the local operator"))
			return
		}
		if frame.Method != "session/update" {
			p.chatDropped.Add(1)
			if len(frame.ID) > 0 {
				_ = p.send(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": map[string]any{"code": -32601, "message": "client method unavailable"}})
			}
			return
		}
		if p.turn == nil {
			p.chatDropped.Add(1)
			return
		}
		p.observeACPChat(raw)
		if p.turn != nil && (frame.Params.Update.Kind == "agent_message_chunk" || frame.Params.Update.Kind == "tool_call") {
			p.turn.accept()
		}
		if frame.Params.Update.Kind == "tool_call" {
			if p.turn != nil {
				p.turn.hadTools = true
			}
			p.observe(AdapterEvent{Kind: "tool"})
		}
		if c := frame.Params.Update.Cost; frame.Params.Update.Kind == "usage_update" && c != nil && c.Currency == "USD" {
			if amount, ok := usdMicros(c.Amount); ok {
				if delta := cumulativeDelta(amount, &p.costMicros); delta > 0 {
					p.observe(AdapterEvent{Kind: "usage", CostMicrosDelta: delta})
				}
			}
		}
		return
	}
	if p.turn == nil || strings.Trim(string(frame.ID), "\"") != p.turn.id {
		return
	}
	var result struct {
		StopReason string          `json:"stopReason"`
		Usage      json.RawMessage `json:"usage"`
		Meta       struct {
			Quota struct {
				Models []struct {
					Model string `json:"model"`
				} `json:"model_usage"`
			} `json:"quota"`
		} `json:"_meta"`
	}
	if len(frame.Error) > 0 && string(frame.Error) != "null" || decodeProbeJSON(frame.Result, &result) != nil || result.StopReason != "end_turn" && result.StopReason != "cancelled" || p.harness == Grok && result.StopReason != "end_turn" {
		if hit := capacity.VendorLimit(p.harness, raw, nil, time.Now().UTC()); hit != nil {
			p.observe(limitEvent(hit))
		}
		p.finishLocked(errors.New("ACP prompt failed"))
		return
	}
	if p.turnComplete != nil {
		if err := p.turnComplete(); err != nil {
			p.finishLocked(err)
			return
		}
	}
	model := p.model
	// OpenCode v1.14.48's prompt response contains only the last assistant
	// message. A tool turn can have earlier billable steps; never report the
	// last step as a complete turn. Its cumulative USD updates remain usable.
	if p.harness == OpenCode && p.turn.hadTools {
		model = ""
	}
	// Gemini may switch models during a turn. Aggregated counters cannot be
	// split across those models, so leave that measurement unknown.
	if p.harness == Gemini && (len(result.Meta.Quota.Models) != 1 || result.Meta.Quota.Models[0].Model != model) {
		model = ""
	}
	report, ok := sessionusage.ACPPromptUsage(p.harness, result.Usage, model)
	if p.harness == Cursor {
		var usage map[string]json.RawMessage
		if json.Unmarshal(result.Usage, &usage) == nil {
			report, ok = sessionusage.CursorPromptUsage(usage, model)
		}
	}
	if ok {
		in, out, cached, thought := usageCount(report.InputTokens), usageCount(report.OutputTokens), usageCount(report.CachedInputTokens), usageCount(report.ReasoningTokens)
		p.input += in
		p.output += out
		p.cached += cached
		p.reasoning += thought
		totalIn, totalOut, totalCache, totalThought := p.input, p.output, p.cached, p.reasoning
		report.InputTokens = &totalIn
		report.OutputTokens = &totalOut
		report.CachedInputTokens = &totalCache
		report.ReasoningTokens = &totalThought
		// The observer synchronously copies reports into its durable queue.
		p.observe(AdapterEvent{Kind: "usage", SessionUsage: &report, InputTokensDelta: in, OutputTokensDelta: out, CachedInputTokensDelta: cached, ReasoningTokensDelta: thought})
	}
	p.turn.accept()
	p.turn = nil
	p.chatTools = nil
	if p.timer != nil {
		p.timer.Stop()
	}
	close(p.idle)
	u := chatState("idle")
	p.observe(AdapterEvent{Chat: &u})
	if !p.persistent {
		p.finishLocked(nil)
		return
	}
	p.observe(AdapterEvent{Kind: "turn", Activity: "idle"})
	generation := p.idleGeneration
	p.timer = time.AfterFunc(p.idleTimeout, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.turn == nil && p.idleGeneration == generation {
			p.finishLocked(nil)
		}
	})
}

func (a *ACPAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	if err := validExecutionMode(r.Run, a); err != nil {
		return nil, err
	}
	if r.ManagedPolicy {
		return nil, ErrUnsupported
	}
	env, err := a.environment(r.AccountKey)
	if err != nil {
		return nil, err
	}
	if err = launcherReady(ctx, a.Path, a.Nodes[r.AccountKey].Path, env); err != nil {
		return nil, err
	}
	args := []string{"acp"}
	if a.Harness == Gemini {
		settings, err := harnesslaunch.GeminiSettings(r.Profile.Model, r.Profile.Effort)
		if err != nil {
			return nil, err
		}
		env = append(env, "GEMINI_CLI_SYSTEM_SETTINGS_PATH="+settings)
		args = []string{"--acp", "--model", r.Profile.Model}
	} else if !strings.Contains(r.Profile.Model, "/") {
		return nil, errors.New("OpenCode requires provider/model")
	}
	w, err := launchWire(a.Path, args, r.Workspace, env, "jsonrpc", observe)
	if err != nil {
		return nil, err
	}
	timeout := a.IdleTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	p := &acpProcess{wireProcess: w, harness: a.Harness, model: r.Profile.Model, persistent: r.InboxEnabled, done: make(chan struct{}), idleTimeout: timeout}
	w.setOnEvent(p.event)
	op, cancel := operationContext(ctx)
	defer cancel()
	raw, err := w.request(op, "jsonrpc", "initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "aeon-agentd", "version": "1"}, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}})
	var init struct {
		Version int `json:"protocolVersion"`
	}
	if err != nil || decodeProbeJSON(raw, &init) != nil || init.Version != 1 {
		return w.failStart(errors.New("ACP initialize failed"))
	}
	mcpServers := []any{}
	if r.Tools != nil {
		mcpServers = append(mcpServers, map[string]any{"name": "aeon", "type": "http", "url": r.Tools.URL,
			"headers": []map[string]string{{"name": "Authorization", "value": "Bearer " + r.Tools.Token}}})
	}
	raw, err = w.request(op, "jsonrpc", "session/new", map[string]any{"cwd": r.Workspace, "mcpServers": mcpServers})
	var session struct {
		ID     string `json:"sessionId"`
		Models struct {
			Current string `json:"currentModelId"`
		} `json:"models"`
	}
	if err != nil || decodeProbeJSON(raw, &session) != nil || (session.ID == "" || len(session.ID) > 128 || strings.ContainsAny(session.ID, "\r\n\x00")) {
		return w.failStart(errors.New("ACP account session unavailable; sign in with the vendor normally"))
	}
	w.eventMu.Lock()
	w.sessionID = session.ID
	w.eventMu.Unlock()
	confirmedEffort := ""
	if a.Harness == OpenCode {
		// Current OpenCode returns the applied model and variant as ACP config
		// options. An RPC acknowledgement without those values is insufficient.
		raw, err = w.request(op, "jsonrpc", "session/set_config_option", map[string]string{"sessionId": session.ID, "configId": "model", "value": r.Profile.Model})
		var config struct {
			Options []struct {
				ID    string `json:"id"`
				Value string `json:"currentValue"`
			} `json:"configOptions"`
		}
		if err != nil || decodeProbeJSON(raw, &config) != nil || !acpOption(config.Options, "model", r.Profile.Model) {
			return w.failStart(errors.New("OpenCode effective model unavailable"))
		}
		hasEffort := false
		for _, option := range config.Options {
			if option.ID == "effort" {
				hasEffort = true
			}
		}
		if r.Profile.Effort != "default" || hasEffort {
			raw, err = w.request(op, "jsonrpc", "session/set_config_option", map[string]string{"sessionId": session.ID, "configId": "effort", "value": r.Profile.Effort})
			if err != nil || decodeProbeJSON(raw, &config) != nil || !acpOption(config.Options, "effort", r.Profile.Effort) || !acpOption(config.Options, "model", r.Profile.Model) {
				return w.failStart(errors.New("OpenCode effective effort unavailable"))
			}
			confirmedEffort = r.Profile.Effort
		}
	} else if session.Models.Current != r.Profile.Model {
		return w.failStart(errors.New("Gemini effective model mismatch"))
	}
	observe(AdapterEvent{VendorSessionID: session.ID, HarnessModel: r.Profile.Model, HarnessEffort: confirmedEffort, EffectiveModel: r.Profile.Model, ModelEvidence: "vendor_reported"})
	prompt := r.Prompt
	if r.Rules != "" {
		prompt = r.Rules + "\n\n" + prompt
	}
	p.mu.Lock()
	_, err = p.promptLocked(op, prompt)
	p.mu.Unlock()
	if err != nil {
		return w.failStart(err)
	}
	return p, nil
}

func acpOption(options []struct {
	ID    string `json:"id"`
	Value string `json:"currentValue"`
}, id, value string) bool {
	for _, o := range options {
		if o.ID == id {
			return o.Value == value
		}
	}
	return false
}

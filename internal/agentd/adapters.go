// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inspr-at/paimos/internal/localjournal"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

func operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 20*time.Second)
}

func localHome(homes map[string]string, key string) (string, error) {
	home := homes[key]
	if key == "" || home == "" || !filepath.IsAbs(home) {
		return "", errors.New("local account key is not enrolled")
	}
	physical, err := filepath.EvalSymlinks(home)
	if err != nil || physical != home {
		return "", errors.New("local account home is not physical")
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("local account home is not private")
	}
	return home, nil
}

func pinnedFile(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("adapter file must be absolute")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return errors.New("adapter file is not pinned")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("adapter file unavailable")
	}
	return nil
}

func withEnv(name, value string) []string {
	prefix := name + "="
	env := make([]string, 0)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			env = append(env, entry)
		}
	}
	return append(env, prefix+value)
}

// Claude authenticates from its pinned account directory. Never inherit daemon
// credentials, loader flags, provider overrides or arbitrary host configuration.
func claudeEnvironment(home, nodePath, claudePath string) []string {
	return []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + home,
		"PATH=" + strings.Join([]string{filepath.Dir(nodePath), filepath.Dir(claudePath), "/usr/bin", "/bin"}, string(os.PathListSeparator)),
		"LANG=C", "LC_ALL=C"}
}

func eventProbe(raw json.RawMessage) (method, kind, model string) {
	var frame struct {
		Method string `json:"method"`
		Kind   string `json:"kind"`
		Params struct {
			Model string `json:"model"`
		} `json:"params"`
		EffectiveModel string `json:"effective_model"`
	}
	_ = json.Unmarshal(raw, &frame)
	return frame.Method, frame.Kind, firstNonempty(frame.EffectiveModel, frame.Params.Model)
}
func firstNonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// CodexAdapter speaks the app-server thread and turn protocol.
type CodexAdapter struct {
	Path   string
	Homes  map[string]string
	Emails map[string]string
}

func NewCodexAdapter(path string, homes map[string]string) *CodexAdapter {
	return &CodexAdapter{Path: path, Homes: homes}
}
func (a *CodexAdapter) SetExpectedEmails(emails map[string]string) { a.Emails = emails }
func (*CodexAdapter) Name() string                                 { return Codex }

type codexProcess struct {
	*wireProcess
	done                      chan bool
	once                      sync.Once
	usage                     *sessionusage.ManagedCodex
	inputTokens, outputTokens int64
	terminal                  *sessionusage.CodexTerminal
	terminalSeen, invalid     bool
	acknowledged, sealed      bool
	abandoned                 atomic.Bool // drain failure; never needs eventMu to publish
}

func (p *codexProcess) Wait() error {
	return p.waitForTurn(p.wireProcess.Stop, 2*time.Second)
}
func (p *codexProcess) Control(ctx context.Context, op, text string) error {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	if op == "steer" {
		var result struct {
			TurnID string `json:"turnId"`
		}
		raw, err := p.request(ctx, "jsonrpc", "turn/steer", map[string]any{"threadId": p.threadID, "expectedTurnId": p.turnID, "input": []map[string]string{{"type": "text", "text": text}}})
		if err != nil || json.Unmarshal(raw, &result) != nil || result.TurnID != p.turnID {
			return errors.New("Codex steer acknowledgement mismatch")
		}
		p.observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
		return nil
	}
	if op == "interrupt" {
		_, err := p.request(ctx, "jsonrpc", "turn/interrupt", map[string]string{"threadId": p.threadID, "turnId": p.turnID})
		return err
	}
	return ErrUnsupported
}
func (a *CodexAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	if err := validExecutionMode(r.Run, a); err != nil {
		return nil, err
	}

	if !a.Probe(ctx, r.AccountKey) {
		return nil, errors.New("Codex account probe unavailable")
	}
	home, err := localHome(a.Homes, r.AccountKey)
	if err != nil {
		return nil, err
	}
	p, err := launchWire(a.Path, []string{"app-server", "--listen", "stdio://"}, r.Workspace, withEnv("CODEX_HOME", home), "jsonrpc", observe)
	if err != nil {
		return nil, err
	}
	cp := &codexProcess{wireProcess: p, done: make(chan bool, 1)}
	p.setOnEvent(cp.notification)
	fail := p.failStart
	op, cancel := operationContext(ctx)
	defer cancel()
	if _, err := p.request(op, "jsonrpc", "initialize", map[string]any{"clientInfo": map[string]string{"name": "aeon-agentd", "title": "AEON agentd", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		return fail(err)
	}
	if err := p.send(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}); err != nil {
		return fail(err)
	}
	expectedEmail := strings.TrimSpace(a.Emails[r.AccountKey])
	if expectedEmail == "" {
		return fail(errors.New("Codex expected account identity unavailable"))
	}
	raw, err := p.request(op, "jsonrpc", "account/read", map[string]any{"refreshToken": false})
	var account struct {
		Account *struct {
			Type  string  `json:"type"`
			Email *string `json:"email"`
		} `json:"account"`
	}
	if err != nil || json.Unmarshal(raw, &account) != nil || account.Account == nil || account.Account.Type != "chatgpt" || account.Account.Email == nil ||
		!strings.EqualFold(strings.TrimSpace(*account.Account.Email), expectedEmail) {
		return fail(errors.New("Codex account identity mismatch"))
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model  string `json:"model"`
		Effort string `json:"reasoningEffort"`
	}
	threadArgs := map[string]any{"cwd": r.Workspace, "approvalPolicy": "never", "model": r.Profile.Model}
	if r.Tools != nil {
		threadArgs["config"] = map[string]any{"mcp_servers": map[string]any{"aeon": map[string]any{
			"url": r.Tools.URL, "http_headers": map[string]string{"Authorization": "Bearer " + r.Tools.Token}, "required": true,
		}}}
		threadArgs["sandboxPolicy"] = map[string]any{"type": "workspaceWrite", "writableRoots": []string{r.Workspace}, "networkAccess": false}
	}
	raw, err = p.request(op, "jsonrpc", "thread/start", threadArgs)
	if err != nil || json.Unmarshal(raw, &thread) != nil || thread.Thread.ID == "" {
		return fail(errors.New("Codex thread start failed"))
	}
	p.eventMu.Lock()
	p.threadID = thread.Thread.ID
	// An unsupported attribution leaves session tokens unreported; it must not
	// prevent the existing run protocol and settlement from operating.
	cp.usage, _ = sessionusage.NewManagedCodex(p.threadID, r.Profile.Model)
	p.eventMu.Unlock()
	// The response reports the resolved settings of this newly owned thread.
	// Launch parameters alone are not evidence of its effective settings.
	observe(AdapterEvent{HarnessModel: thread.Model, HarnessEffort: thread.Effort})
	if err := cp.startTurn(op, r); err != nil {
		return fail(errors.New("Codex turn start failed"))
	}
	observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
	return cp, nil
}

// PiAdapter speaks Pi's JSONL RPC and verifies the effective state before
// sending the first prompt or any steer.
type PiAdapter struct {
	Path  string
	Homes map[string]string
}

func NewPiAdapter(path string, homes map[string]string) *PiAdapter {
	return &PiAdapter{Path: path, Homes: homes}
}
func (*PiAdapter) Name() string { return Pi }

type piProcess struct {
	*wireProcess
	provider, model, effort string
	queue                   *localjournal.Journal[piHeldQueue]
	scope                   piHeldQueue
	controlMu               sync.Mutex
}

func (p *piProcess) Control(ctx context.Context, op, text string) error {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	ctx, cancel := operationContext(ctx)
	defer cancel()
	if err := p.verify(ctx); err != nil {
		return err
	}
	held := p.queue.Snapshot()
	switch op {
	case "steer":
		if len(held) > 0 {
			return errors.New("Pi delivery is held")
		}
		_, err := p.request(ctx, "pi", "prompt", map[string]any{"message": text, "streamingBehavior": "steer"})
		if err == nil {
			p.observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
		}
		return err
	case "interrupt":
		if len(held) > 0 {
			return errors.New("Pi delivery is already held")
		}
		raw, err := p.request(ctx, "pi", "clear_queue", nil)
		if err != nil {
			return err
		}
		var data struct {
			Steering []string `json:"steering"`
			FollowUp []string `json:"followUp"`
		}
		if json.Unmarshal(raw, &data) != nil {
			return errors.New("Pi clear_queue response invalid")
		}
		q := p.scope
		q.Steering = data.Steering
		q.FollowUp = data.FollowUp
		if !validPiQueue(q) {
			return errors.New("Pi held queue exceeds bound")
		}
		if err := p.queue.Put(q); err != nil {
			return err
		}
		_, err = p.request(ctx, "pi", "abort", nil)
		return err
	case "resume":
		if len(held) != 1 || held[0].Ambiguous {
			return errors.New("Pi held queue is unavailable or ambiguous")
		}
		q := held[0]
		q.Ambiguous = true
		if err := p.queue.Put(q); err != nil {
			return err
		}
		for _, message := range held[0].Steering {
			if _, err := p.request(ctx, "pi", "prompt", map[string]any{"message": message, "streamingBehavior": "steer"}); err != nil {
				return err
			}
			p.observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
		}
		for _, message := range held[0].FollowUp {
			if _, err := p.request(ctx, "pi", "prompt", map[string]any{"message": message, "streamingBehavior": "followUp"}); err != nil {
				return err
			}
			p.observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
		}
		return p.queue.Delete(q.RunID)
	default:
		return ErrUnsupported
	}
}
func (p *piProcess) verify(ctx context.Context) error {
	raw, err := p.request(ctx, "pi", "get_state", nil)
	if err != nil {
		return err
	}
	var state struct {
		Model struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		} `json:"model"`
		ThinkingLevel string `json:"thinkingLevel"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Model.Provider != p.provider || state.Model.ID != p.model || state.ThinkingLevel != p.effort {
		return errors.New("Pi effective model mismatch")
	}
	return nil
}
func (a *PiAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	if err := validExecutionMode(r.Run, a); err != nil {
		return nil, err
	}

	if !a.Probe(ctx, r.AccountKey) {
		return nil, errors.New("Pi account context unavailable")
	}
	home, err := localHome(a.Homes, r.AccountKey)
	if err != nil {
		return nil, err
	}
	provider, model, ok := strings.Cut(r.Profile.Model, "/")
	if !ok || provider == "" || model == "" {
		return nil, errors.New("Pi model requires provider/model")
	}
	queue, err := openPiQueue(r)
	if err != nil {
		return nil, err
	}
	if len(queue.Snapshot()) != 0 {
		return nil, errors.New("Pi held queue requires explicit operator reconciliation")
	}
	p, err := launchWire(a.Path, []string{"--mode", "rpc", "--no-session", "--provider", provider, "--model", model, "--thinking", r.Profile.Effort}, r.Workspace, withEnv("PI_CODING_AGENT_DIR", home), "pi", observe)
	if err != nil {
		return nil, err
	}
	pp := &piProcess{wireProcess: p, provider: provider, model: model, effort: r.Profile.Effort, queue: queue,
		scope: piHeldQueue{TenantID: r.TenantID, PrincipalID: r.PrincipalID, RunID: r.Run.ID, Generation: r.Generation}}
	p.setOnEvent(func(raw json.RawMessage) {
		var frame struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &frame) == nil {
			if frame.Type == "agent_end" {
				observe(AdapterEvent{Kind: "turn"})
			}
			if frame.Type == "tool_execution_start" {
				observe(AdapterEvent{Kind: "tool"})
			}
		}
	})
	op, cancel := operationContext(ctx)
	defer cancel()
	if err := pp.verify(op); err != nil {
		return p.failStart(err)
	}
	if _, err := p.request(op, "pi", "prompt", map[string]any{"message": r.Prompt}); err != nil {
		return p.failStart(err)
	}
	observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1, EffectiveModel: r.Profile.Model, ModelEvidence: "vendor_reported"})
	return pp, nil
}

// CursorAdapter speaks ACP. Account identity is checked with Cursor's
// bounded status JSON before a child is launched.
type CursorAdapter struct {
	Path       string
	Identities map[string]string
}

func NewCursorAdapter(path string, identities map[string]string) *CursorAdapter {
	return &CursorAdapter{Path: path, Identities: identities}
}
func (*CursorAdapter) Name() string { return Cursor }

type cursorProcess struct {
	*wireProcess
	promptID    string
	done        chan struct{}
	doneOnce    sync.Once
	terminalErr error
}

func (p *cursorProcess) finish(err error) {
	p.doneOnce.Do(func() { p.terminalErr = err; close(p.done) })
}

func (p *cursorProcess) Wait() (err error) {
	defer func() { err = errors.Join(err, p.finishReader()) }()
	select {
	case <-p.done:
	case <-p.waitDone:
		return errors.New("Cursor ACP child exited before prompt completion")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.wireProcess.Stop(ctx)
	return p.terminalErr
}

func (p *cursorProcess) Control(ctx context.Context, op, text string) error {
	if op != "interrupt" {
		return ErrUnsupported
	}
	select {
	case <-p.done:
		return ErrNotOwned
	default:
	}
	if err := p.send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": p.sessionID}}); err != nil {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.readDone:
		return errors.New("Cursor ACP closed before cancel acknowledgement")
	}
}
func (a *CursorAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	if err := validExecutionMode(r.Run, a); err != nil {
		return nil, err
	}

	if !a.Probe(ctx, r.AccountKey) {
		return nil, errors.New("Cursor account identity unavailable")
	}
	path, err := pinnedExecutable(a.Path)
	if err != nil {
		return nil, err
	}
	op, cancel := operationContext(ctx)
	defer cancel()
	args := []string{"--trust", "--model", r.Profile.Model, "acp"}
	p, err := launchWire(path, args, r.Workspace, nil, "jsonrpc", observe)
	if err != nil {
		return nil, err
	}
	cp := &cursorProcess{wireProcess: p, done: make(chan struct{})}
	var costMicros int64
	p.setOnEvent(func(raw json.RawMessage) {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
			Params struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					SessionUpdate string `json:"sessionUpdate"`
					Cost          *struct {
						Amount   json.RawMessage `json:"amount"`
						Currency string          `json:"currency"`
					} `json:"cost"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			return
		}
		if len(frame.ID) > 0 && strings.Trim(string(frame.ID), "\"") == cp.promptID {
			var result struct {
				StopReason string `json:"stopReason"`
			}
			if len(frame.Error) > 0 && string(frame.Error) != "null" || json.Unmarshal(frame.Result, &result) != nil || result.StopReason != "end_turn" {
				cp.finish(errors.New("Cursor ACP prompt failed"))
			} else {
				cp.finish(nil)
			}
			return
		}
		method := frame.Method
		if method == "session/update" && frame.Params.SessionID == p.sessionID &&
			frame.Params.Update.SessionUpdate == "usage_update" && frame.Params.Update.Cost != nil &&
			frame.Params.Update.Cost.Currency == "USD" {
			if current, ok := usdMicros(frame.Params.Update.Cost.Amount); ok {
				if delta := cumulativeDelta(current, &costMicros); delta > 0 {
					observe(AdapterEvent{Kind: "usage", CostMicrosDelta: delta})
				}
			}
		} else if method == "session/request_permission" {
			cp.finish(errors.New("Cursor ACP decision requires local operator"))
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = p.Stop(ctx)
			}()
		}
	})
	fail := p.failStart
	raw, err := p.request(op, "jsonrpc", "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}, "clientInfo": map[string]string{"name": "aeon-agentd", "title": "AEON agentd", "version": "1"}})
	var init struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err != nil || json.Unmarshal(raw, &init) != nil || init.ProtocolVersion != 1 {
		return fail(errors.New("Cursor ACP initialize failed"))
	}
	mcpServers := []any{}
	if r.Tools != nil {
		mcpServers = append(mcpServers, map[string]any{"name": "aeon", "type": "http", "url": r.Tools.URL,
			"headers": map[string]string{"Authorization": "Bearer " + r.Tools.Token}})
	}
	raw, err = p.request(op, "jsonrpc", "session/new", map[string]any{"cwd": r.Workspace, "mcpServers": mcpServers})
	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			Current string `json:"currentModelId"`
		} `json:"models"`
		Modes struct {
			Current string `json:"currentModeId"`
		} `json:"modes"`
	}
	expectedModel := r.Profile.Model
	if expectedModel == "composer-2.5" {
		expectedModel = "composer-2.5[fast=true]"
	}
	if err != nil || json.Unmarshal(raw, &session) != nil || session.SessionID == "" || session.Models.Current != expectedModel || session.Modes.Current == "" {
		return fail(errors.New("Cursor ACP session failed"))
	}
	p.eventMu.Lock()
	p.sessionID = session.SessionID
	p.eventMu.Unlock()
	cp.promptID = strconv.FormatInt(p.next.Add(1), 10)
	id, _ := strconv.ParseInt(cp.promptID, 10, 64)
	if err := p.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/prompt", "params": map[string]any{"sessionId": p.sessionID, "prompt": []map[string]string{{"type": "text", "text": r.Prompt}}}}); err != nil {
		return fail(err)
	}
	observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
	return cp, nil
}

//go:embed claudeassets/bridge.mjs
var claudeAssets embed.FS

type ClaudeAdapter struct {
	NodePath, SDKPath, ClaudePath string
	Homes                         map[string]string
	Emails                        map[string]string
}

func NewClaudeAdapter(nodePath, sdkPath, claudePath string, homes map[string]string) *ClaudeAdapter {
	return &ClaudeAdapter{NodePath: nodePath, SDKPath: sdkPath, ClaudePath: claudePath, Homes: homes}
}
func (*ClaudeAdapter) Name() string                                 { return Claude }
func (a *ClaudeAdapter) SetExpectedEmails(emails map[string]string) { a.Emails = emails }

type claudeProcess struct {
	*wireProcess
	assetDir  string
	ready     chan error
	controlMu sync.Mutex
	controls  map[string]chan bool
}

func (p *claudeProcess) Wait() error {
	err := p.wireProcess.Wait()
	_ = os.Remove(filepath.Join(p.assetDir, "bridge.mjs"))
	_ = os.Remove(p.assetDir)
	return err
}
func (p *claudeProcess) Control(ctx context.Context, op, text string) error {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	if op != "steer" && op != "interrupt" && op != "stop" && op != "model" && op != "effort" {
		return ErrUnsupported
	}
	correlation, err := randomID()
	if err != nil {
		return err
	}
	ch := make(chan bool, 1)
	p.controlMu.Lock()
	p.controls[correlation] = ch
	p.controlMu.Unlock()
	defer func() { p.controlMu.Lock(); delete(p.controls, correlation); p.controlMu.Unlock() }()
	frame := map[string]any{"op": op, "correlation_id": correlation}
	if isSetting(op) {
		frame["value"] = text
	} else {
		frame["text"] = text
	}
	if err := p.send(frame); err != nil {
		return err
	}
	select {
	case ok := <-ch:
		if ok {
			return nil
		}
		if isSetting(op) {
			return ErrSettingRejected
		}
		return errors.New("Claude bridge rejected control")
	case <-ctx.Done():
		return ctx.Err()
	case <-p.readDone:
		// A native close can acknowledge and reach EOF in the same reader pass.
		// Preserve its already-read receipt instead of racing it against EOF.
		select {
		case ok := <-ch:
			if ok {
				return nil
			}
		default:
		}
		return errors.New("Claude bridge closed")
	}
}
func (a *ClaudeAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	if err := validExecutionMode(r.Run, a); err != nil {
		return nil, err
	}

	if !a.Probe(ctx, r.AccountKey) {
		return nil, errors.New("Claude account probe unavailable")
	}
	home, err := localHome(a.Homes, r.AccountKey)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{a.NodePath, a.ClaudePath} {
		if _, err := pinnedExecutable(path); err != nil {
			return nil, err
		}
	}
	if err := pinnedFile(a.SDKPath); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "aeon-claude-bridge-")
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = os.Remove(filepath.Join(dir, "bridge.mjs"))
			_ = os.Remove(dir)
		}
	}()
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	for _, name := range []string{"bridge.mjs"} {
		data, e := claudeAssets.ReadFile("claudeassets/" + name)
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(filepath.Join(dir, name), data, 0600); e != nil {
			return nil, e
		}
	}
	p, err := launchWire(a.NodePath, []string{filepath.Join(dir, "bridge.mjs"), a.SDKPath, a.ClaudePath, r.Workspace}, r.Workspace, claudeEnvironment(home, a.NodePath, a.ClaudePath), "bridge", observe)
	if err != nil {
		return nil, err
	}
	cp := &claudeProcess{wireProcess: p, assetDir: dir, ready: make(chan error, 1), controls: map[string]chan bool{}}
	var inputTokens, outputTokens, costMicros int64
	p.setOnEvent(func(raw json.RawMessage) {
		var frame struct {
			Kind            string          `json:"kind"`
			Reason          string          `json:"reason"`
			CorrelationID   string          `json:"correlation_id"`
			EffectiveModel  string          `json:"effective_model"`
			EffectiveEffort string          `json:"effective_effort"`
			ModelEvidence   string          `json:"model_evidence_status"`
			InputTokens     int64           `json:"input_tokens_total"`
			OutputTokens    int64           `json:"output_tokens_total"`
			CostUSD         json.RawMessage `json:"cost_usd_total"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			return
		}
		switch frame.Kind {
		case "session_started":
			observe(AdapterEvent{Kind: "status", EffectiveModel: frame.EffectiveModel, ModelEvidence: frame.ModelEvidence, HarnessModel: frame.EffectiveModel})
			select {
			case cp.ready <- nil:
			default:
			}
		case "settings_changed":
			observe(AdapterEvent{HarnessModel: frame.EffectiveModel, HarnessEffort: frame.EffectiveEffort})
		case "budget_exhausted":
			if frame.Reason == "token_budget_exhausted" || frame.Reason == "turn_budget_exhausted" {
				observe(AdapterEvent{BudgetExhausted: frame.Reason})
			}
		case "turn_completed":
			observe(AdapterEvent{BudgetTurnsDelta: 1})
		case "turn_started":
			observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
		case "usage":
			if frame.InputTokens < inputTokens || frame.OutputTokens < outputTokens {
				break
			}
			ev := AdapterEvent{Kind: "usage", InputTokensDelta: cumulativeDelta(frame.InputTokens, &inputTokens),
				OutputTokensDelta: cumulativeDelta(frame.OutputTokens, &outputTokens)}
			if cost, ok := usdMicros(frame.CostUSD); ok {
				ev.CostMicrosDelta = cumulativeDelta(cost, &costMicros)
			}
			if ev.InputTokensDelta > 0 || ev.OutputTokensDelta > 0 || ev.CostMicrosDelta > 0 {
				observe(ev)
			}
		case "tool_started":
			observe(AdapterEvent{Kind: "tool"})
		case "control_applied", "control_failed":
			cp.controlMu.Lock()
			ch := cp.controls[frame.CorrelationID]
			cp.controlMu.Unlock()
			if ch != nil {
				ch <- frame.Kind == "control_applied"
			}
		}
	})
	if err := p.send(map[string]any{"op": "start", "prompt": r.Prompt, "model": r.Profile.Model, "effort": r.Profile.Effort, "correlation_id": "initial", "tools": r.Tools, "purpose": r.Run.Purpose, "rules": r.Rules, "max_turns": r.MaxTurns, "max_tokens": r.MaxTokens}); err != nil {
		return p.failStart(err)
	}
	op, cancel := operationContext(ctx)
	defer cancel()
	select {
	case <-cp.ready:
		started = true
		return cp, nil
	case <-op.Done():
		return p.failStart(fmt.Errorf("Claude bridge readiness: %w", op.Err()))
	case <-p.readDone:
		return p.failStart(errors.New("Claude bridge ended before readiness"))
	}
}

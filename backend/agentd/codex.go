// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/ownedprocess"
)

const (
	maxCodexFrameBytes    = 8 << 20
	codexOperationTimeout = 20 * time.Second
	codexExitGracePeriod  = 250 * time.Millisecond
)

type CodexAdapter struct {
	path          string
	clientVersion string
	command       func(string, ...string) *exec.Cmd
}

func NewCodexAdapter(path, clientVersion string) *CodexAdapter {
	return &CodexAdapter{path: strings.TrimSpace(path), clientVersion: strings.TrimSpace(clientVersion), command: exec.Command}
}

func (*CodexAdapter) Name() string { return AdapterCodex }
func (*CodexAdapter) Capabilities() []Capability {
	return []Capability{CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop}
}

func (a *CodexAdapter) AccountLabel(ctx context.Context) string {
	path, err := a.executable()
	if err != nil {
		return "unknown"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := runAccountProbe(probeCtx, path, 512, "login", "status")
	if err != nil {
		return "unknown"
	}
	status := strings.TrimSpace(string(output))
	switch {
	case status == "Logged in using ChatGPT":
		return "chatgpt"
	case strings.HasPrefix(status, "Logged in using an API key"):
		return "api_key"
	default:
		return "unknown"
	}
}

func (a *CodexAdapter) executable() (string, error) {
	if a.path != "" {
		if !filepath.IsAbs(a.path) {
			return "", errors.New("configured Codex executable must be an absolute path")
		}
		return a.path, nil
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", errors.New("Codex adapter requires the operator-authenticated codex CLI in PATH")
	}
	return path, nil
}

// Start owns one documented app-server stdio child and creates the thread and
// turn through that same initialized RPC connection. The returned Process is
// therefore the only live control object; no vendor session ID can reconstruct
// it after restart.
func (a *CodexAdapter) Start(ctx context.Context, request StartRequest, observe func(AdapterEvent)) (_ Process, returnErr error) {
	path, err := a.executable()
	if err != nil {
		return nil, err
	}
	command := a.command
	if command == nil {
		command = exec.Command
	}
	cmd := command(path, "app-server", "--listen", "stdio://") // #nosec G204 G702 -- fixed adapter argv and operator-selected executable.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("open Codex app-server stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("open Codex app-server stdout")
	}
	cmd.Stderr = io.Discard
	configured := ownedprocess.Configure(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errors.New("start Codex app-server child")
	}
	if err := ownedprocess.Verify(cmd, configured); err != nil {
		_ = ownedprocess.Signal(cmd, true)
		_ = cmd.Wait()
		return nil, err
	}
	process := newCodexProcess(cmd, stdin, stdout, observe)
	defer func() {
		if returnErr != nil {
			_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "agentd-codex-start-failed"})
		}
	}()

	operationCtx, cancel := context.WithTimeout(ctx, codexOperationTimeout)
	defer cancel()
	version := a.clientVersion
	if version == "" {
		version = "dev"
	}
	if err := process.call(operationCtx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "paimos-agentd", "title": "PAIMOS agentd", "version": version},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := process.notify("initialized", map[string]any{}); err != nil {
		return nil, errors.New("notify Codex app-server initialized")
	}
	var threadResponse struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	threadStart := map[string]any{"cwd": request.Workspace, "approvalPolicy": "never"}
	if request.ResolvedProfile != nil {
		threadStart["model"] = request.ResolvedProfile.Model
	}
	if err := process.call(operationCtx, "thread/start", threadStart, &threadResponse); err != nil {
		return nil, fmt.Errorf("start Codex app-server thread: %w", err)
	}
	if !validOpaqueID(threadResponse.Thread.ID) {
		return nil, errors.New("Codex app-server returned an invalid thread")
	}
	process.setThread(threadResponse.Thread.ID)
	process.observeEvent(AdapterEvent{Kind: EventSessionStarted, HarnessSessionID: threadResponse.Thread.ID})

	var turnResponse struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	turnStart := map[string]any{
		"threadId": threadResponse.Thread.ID,
		"input":    []map[string]string{{"type": "text", "text": request.Prompt}},
	}
	if request.ResolvedProfile != nil {
		turnStart["model"] = request.ResolvedProfile.Model
		turnStart["effort"] = request.ResolvedProfile.Effort
	}
	if err := process.call(operationCtx, "turn/start", turnStart, &turnResponse); err != nil {
		return nil, fmt.Errorf("start Codex app-server turn: %w", err)
	}
	if !validOpaqueID(turnResponse.Turn.ID) || turnResponse.Turn.Status != "inProgress" {
		return nil, errors.New("Codex app-server returned an invalid active turn")
	}
	process.setTurn(turnResponse.Turn.ID)
	process.observeEvent(AdapterEvent{Kind: EventTurnStarted})
	return process, nil
}

type codexRPCError struct {
	Code int `json:"code"`
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *codexRPCError  `json:"error"`
}

type codexTurnResult struct{ failed bool }

type codexProcess struct {
	*ownedProcess
	stdin   io.WriteCloser
	observe func(AdapterEvent)

	writeMu sync.Mutex
	rpcMu   sync.Mutex
	nextID  int
	pending map[string]chan codexRPCMessage

	stateMu             sync.Mutex
	threadID            string
	turnID              string
	earlyCompleted      string
	earlyCompletedState string
	turnDone            chan codexTurnResult
	turnDoneOnce        sync.Once
	streamDone          chan struct{}
	streamDoneOnce      sync.Once
}

func newCodexProcess(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader, observe func(AdapterEvent)) *codexProcess {
	p := &codexProcess{ownedProcess: newOwnedProcess(cmd), stdin: stdin, observe: observe,
		pending: map[string]chan codexRPCMessage{}, turnDone: make(chan codexTurnResult, 1), streamDone: make(chan struct{})}
	go p.readLoop(stdout)
	return p
}

func (p *codexProcess) observeEvent(event AdapterEvent) {
	if p.observe != nil {
		p.observe(event)
	}
}

func (p *codexProcess) readLoop(reader io.Reader) {
	defer func() {
		p.closeInput()
		p.streamDoneOnce.Do(func() { close(p.streamDone) })
		p.finishAfterDrain()
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCodexFrameBytes)
	for scanner.Scan() {
		var message codexRPCMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			p.observeEvent(AdapterEvent{Kind: EventToolStarted, ErrorCode: ErrorAppServerProtocol})
			p.abortStream()
			return
		}
		if len(message.ID) > 0 && message.Method == "" {
			p.rpcMu.Lock()
			response := p.pending[string(message.ID)]
			p.rpcMu.Unlock()
			if response != nil {
				select {
				case response <- message:
				default:
				}
			}
			continue
		}
		p.handleNotification(message)
	}
	if scanner.Err() != nil {
		p.observeEvent(AdapterEvent{Kind: EventToolStarted, ErrorCode: ErrorEventStreamBound})
		p.abortStream()
	}
}

func (p *codexProcess) abortStream() {
	p.closeInput()
	_, _ = p.signalOwned(true)
}

func (p *codexProcess) handleNotification(message codexRPCMessage) {
	var params struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	switch message.Method {
	case "thread/started":
		if json.Unmarshal(message.Params, &params) == nil && validOpaqueID(params.Thread.ID) {
			p.observeEvent(AdapterEvent{Kind: EventSessionStarted, HarnessSessionID: params.Thread.ID})
		}
	case "turn/started":
		if json.Unmarshal(message.Params, &params) == nil && validOpaqueID(params.Turn.ID) {
			p.observeEvent(AdapterEvent{Kind: EventTurnStarted})
		}
	case "item/started":
		p.observeEvent(AdapterEvent{Kind: EventToolStarted})
	case "turn/completed":
		if json.Unmarshal(message.Params, &params) == nil && validOpaqueID(params.Turn.ID) {
			p.observeEvent(AdapterEvent{Kind: EventTurnCompleted})
			p.recordCompletion(params.Turn.ID, params.Turn.Status)
		}
	}
}

func (p *codexProcess) recordCompletion(turnID, status string) {
	p.stateMu.Lock()
	if p.turnID == "" {
		p.earlyCompleted, p.earlyCompletedState = turnID, status
		p.stateMu.Unlock()
		return
	}
	matches := p.turnID == turnID
	p.stateMu.Unlock()
	if matches {
		p.completeTurn(status)
	}
}

func (p *codexProcess) completeTurn(status string) {
	p.turnDoneOnce.Do(func() {
		p.turnDone <- codexTurnResult{failed: status != "completed" && status != "interrupted"}
	})
}

func (p *codexProcess) setThread(threadID string) {
	p.stateMu.Lock()
	p.threadID = threadID
	p.stateMu.Unlock()
}

func (p *codexProcess) setTurn(turnID string) {
	p.stateMu.Lock()
	p.turnID = turnID
	earlyID, earlyStatus := p.earlyCompleted, p.earlyCompletedState
	p.stateMu.Unlock()
	if earlyID == turnID {
		p.completeTurn(earlyStatus)
	}
}

func (p *codexProcess) target() (string, string, error) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.threadID == "" || p.turnID == "" {
		return "", "", ErrCapabilityMissing
	}
	return p.threadID, p.turnID, nil
}

func (p *codexProcess) send(value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode Codex app-server request")
	}
	body = append(body, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return ErrSessionNotRunning
	}
	if _, err := p.stdin.Write(body); err != nil {
		return errors.New("write Codex app-server request")
	}
	return nil
}

func (p *codexProcess) notify(method string, params any) error {
	return p.send(map[string]any{"method": method, "params": params})
}

func (p *codexProcess) call(ctx context.Context, method string, params, result any) error {
	p.rpcMu.Lock()
	p.nextID++
	requestID := p.nextID
	id := strconv.Itoa(requestID)
	response := make(chan codexRPCMessage, 1)
	p.pending[id] = response
	p.rpcMu.Unlock()
	defer func() {
		p.rpcMu.Lock()
		delete(p.pending, id)
		p.rpcMu.Unlock()
	}()
	if err := p.send(map[string]any{"id": requestID, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case message := <-response:
		if message.Error != nil {
			return fmt.Errorf("Codex app-server rejected %s (code %d)", method, message.Error.Code)
		}
		if result == nil {
			return nil
		}
		if len(message.Result) == 0 || json.Unmarshal(message.Result, result) != nil {
			return errors.New("Codex app-server returned an invalid response")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return errors.New("Codex app-server exited during request")
	case <-p.streamDone:
		return errors.New("Codex app-server event stream ended during request")
	}
}

func codexControlContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, codexOperationTimeout)
}

func (p *codexProcess) Steer(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	threadID, turnID, err := p.target()
	if err != nil {
		return ControlEffect{}, err
	}
	operationCtx, cancel := codexControlContext(ctx)
	defer cancel()
	var response struct {
		TurnID string `json:"turnId"`
	}
	err = p.call(operationCtx, "turn/steer", map[string]any{
		"threadId": threadID, "expectedTurnId": turnID,
		"input": []map[string]string{{"type": "text", "text": request.Text}},
	}, &response)
	if err != nil {
		return ControlEffect{}, err
	}
	if response.TurnID != turnID {
		return ControlEffect{}, errors.New("Codex app-server returned mismatched steer evidence")
	}
	p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: "codex app-server turn/steer", CorrelationID: request.CorrelationID, VendorMessageID: turnID}, nil
}

func (p *codexProcess) Interrupt(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	threadID, turnID, err := p.target()
	if err != nil {
		return ControlEffect{}, err
	}
	operationCtx, cancel := codexControlContext(ctx)
	defer cancel()
	if err := p.call(operationCtx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, &struct{}{}); err != nil {
		return ControlEffect{}, err
	}
	p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: "codex app-server turn/interrupt", CorrelationID: request.CorrelationID, VendorMessageID: turnID}, nil
}

func (p *codexProcess) closeInput() {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
}

func (p *codexProcess) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	// Interrupt is best-effort here: Stop's authoritative effect is terminating
	// the exact owned app-server process group, even if the turn just completed.
	_, _ = p.Interrupt(ctx, request)
	p.closeInput()
	effect, err := p.ownedProcess.Stop(ctx, request)
	if err == nil {
		p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	}
	return effect, err
}

func (p *codexProcess) Wait() error {
	select {
	case result := <-p.turnDone:
		p.closeInput()
		select {
		case <-p.done:
		case <-time.After(codexExitGracePeriod):
			_, _ = p.ownedProcess.Stop(context.Background(), ControlRequest{CorrelationID: "agentd-turn-complete"})
		}
		_ = p.ownedProcess.Wait()
		if result.failed {
			return errors.New("Codex turn failed")
		}
		return nil
	case <-p.done:
		select {
		case result := <-p.turnDone:
			_ = p.ownedProcess.Wait()
			if result.failed {
				return errors.New("Codex turn failed")
			}
			return nil
		default:
			_ = p.ownedProcess.Wait()
			return errors.New("Codex app-server exited before turn completion")
		}
	case <-p.streamDone:
		select {
		case result := <-p.turnDone:
			_ = p.ownedProcess.Wait()
			if result.failed {
				return errors.New("Codex turn failed")
			}
			return nil
		default:
			// readLoop closes streamDone before finishAfterDrain signals and
			// reaps the exact child. EOF is not process-exit proof.
			_ = p.ownedProcess.Wait()
			return errors.New("Codex app-server event stream ended before turn completion")
		}
	}
}

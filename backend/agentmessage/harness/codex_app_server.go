// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Codex steer transport (PAI-825 follow-up, PAI-831).
//
// The Codex app-server daemon serves its control plane on a Unix socket. The
// documented client path is `codex app-server proxy`, which opens exactly one
// raw stream connection to that socket and proxies bytes to stdio. That
// stream is not newline-delimited JSON: per the vendor app-server README it
// "carries the websocket HTTP Upgrade handshake followed by websocket frames",
// one JSON-RPC 2.0 message per text frame with the "jsonrpc" header omitted
// on the wire. PAIMOS 5.17.3 and 5.18.0 wrote JSON lines into that stream, so
// the daemon kept waiting for an HTTP request head, `initialize` never got an
// answer, and the timeout cleanup of the npm wrapper orphaned the native proxy
// and surfaced as `initialize Codex app-server: EOF`. This file speaks the
// documented framing through the vendor proxy. PAIMOS still never opens the
// socket itself, never starts its own app-server, and never invents a
// `codex steer` CLI.

// CodexSteerTimeout bounds one steer attempt end to end: daemon start, proxy
// spawn, WebSocket handshake, and the JSON-RPC exchange. Tests lower it.
var CodexSteerTimeout = 20 * time.Second

// codexProxyMaxPayloadBytes caps one inbound frame. The stable
// `thread/read {includeTurns:true}` returns the whole thread history in a
// single frame, which is tens of megabytes for a long gauntlet thread.
const codexProxyMaxPayloadBytes = 512 << 20

const codexProxyReapDelay = 2 * time.Second

// codexRPCInvalidRequest is the JSON-RPC code the app-server uses for its
// documented request rejections (`invalid_request` in codex-rs).
const codexRPCInvalidRequest = -32600

type codexRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *codexRPCError) Error() string {
	return fmt.Sprintf("Codex app-server error %d: %s", e.Code, e.Message)
}

// codexRPCMessage is one server-to-client wire message. Responses carry an id
// plus result or error; notifications carry a method without an id; server
// requests carry both and are never answered by this client.
type codexRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *codexRPCError  `json:"error"`
}

type codexTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// CodexProxyTimeoutError names the wire phase that produced no answer within
// the steer budget so an operator can tell a dead daemon from a wrong
// framing assumption. It is a transport failure, not an UNSUPPORTED
// primitive. The delivery layer reports it and degrades to the exact queue
// primitive with fallback_reason=transport_error.
type CodexProxyTimeoutError struct {
	Phase   string
	Timeout time.Duration
	Daemon  string
}

func (e *CodexProxyTimeoutError) Error() string {
	msg := fmt.Sprintf("Codex app-server proxy: no response to %s within %s (the proxied stream is a WebSocket byte pipe to the app-server daemon)", e.Phase, e.Timeout)
	if e.Daemon != "" {
		msg += "; " + e.Daemon
	}
	return msg
}

// codexProxyStream adapts the proxy's stdio pipes to the io.ReadWriteCloser
// the WebSocket client expects. Closing it closes the proxy's stdin.
type codexProxyStream struct {
	io.Reader
	io.WriteCloser
}

func (s codexProxyStream) Close() error { return s.WriteCloser.Close() }

// codexAppServerSession is one initialized JSON-RPC session over WebSocket
// frames through a `codex app-server proxy` child process.
type codexAppServerSession struct {
	proxy    *exec.Cmd
	conn     *websocket.Conn
	messages chan codexRPCMessage
	stop     chan struct{}
	readDone chan struct{}
	readErr  error
	nextID   int
	once     sync.Once
}

// codexCommand builds a bounded, group-owned vendor CLI invocation. Killing
// the whole process group on cancellation matters because the npm `codex`
// wrapper is a Node parent whose native child would otherwise outlive it and
// keep our pipes open.
func codexCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	// #nosec G204 G702 -- codex is resolved from the operator-controlled PATH and every argv entry is fixed.
	cmd := exec.CommandContext(ctx, path, args...)
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = codexProxyReapDelay
	return cmd
}

// openCodexAppServerSession starts the daemon idempotently, spawns the vendor
// proxy, performs the WebSocket Upgrade handshake, and completes the
// documented initialize/initialized exchange. Every blocking step is bounded
// by ctx; on timeout the proxy process group is killed before returning.
func openCodexAppServerSession(ctx context.Context, path string, stderr io.Writer, clientVersion string) (*codexAppServerSession, error) {
	stderr = writerOrDiscard(stderr)
	if clientVersion == "" {
		clientVersion = defaultClientVersion
	}
	daemon := codexCommand(ctx, path, "app-server", "daemon", "start")
	daemon.Stdout = io.Discard
	daemon.Stderr = stderr
	if err := daemon.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, codexTimeout(path, "app-server daemon start")
		}
		return nil, fmt.Errorf("start Codex app-server daemon: %w", err)
	}
	proxy := codexCommand(ctx, path, "app-server", "proxy")
	stdin, err := proxy.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex proxy stdin: %w", err)
	}
	stdoutPipe, err := proxy.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex proxy stdout: %w", err)
	}
	proxy.Stderr = stderr
	if err := proxy.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server proxy: %w", err)
	}
	session := &codexAppServerSession{
		proxy:    proxy,
		messages: make(chan codexRPCMessage, 16),
		stop:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	config, err := websocket.NewConfig("ws://codex-app-server.local/", "http://codex-app-server.local/")
	if err != nil {
		session.reap()
		return nil, fmt.Errorf("configure Codex proxy websocket client: %w", err)
	}
	type handshake struct {
		conn *websocket.Conn
		err  error
	}
	done := make(chan handshake, 1)
	go func() {
		conn, err := websocket.NewClient(config, codexProxyStream{Reader: stdoutPipe, WriteCloser: stdin})
		done <- handshake{conn: conn, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			session.reap()
			if ctx.Err() != nil {
				return nil, codexTimeout(path, "websocket handshake")
			}
			return nil, fmt.Errorf("Codex app-server proxy websocket handshake: %w", result.err)
		}
		session.conn = result.conn
	case <-ctx.Done():
		session.reap()
		return nil, codexTimeout(path, "websocket handshake")
	}
	session.conn.MaxPayloadBytes = codexProxyMaxPayloadBytes
	go session.readLoop()
	if err := session.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "paimos", "title": "PAIMOS agent bus", "version": clientVersion},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		session.close()
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := session.notify("initialized", map[string]any{}); err != nil {
		session.close()
		return nil, fmt.Errorf("notify Codex app-server initialized: %w", err)
	}
	return session, nil
}

func (s *codexAppServerSession) readLoop() {
	defer close(s.messages)
	defer close(s.readDone)
	for {
		var text string
		if err := websocket.Message.Receive(s.conn, &text); err != nil {
			s.readErr = err
			return
		}
		var msg codexRPCMessage
		if err := json.Unmarshal([]byte(text), &msg); err != nil {
			continue
		}
		select {
		case s.messages <- msg:
		case <-s.stop:
			return
		}
	}
}

func (s *codexAppServerSession) send(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return websocket.Message.Send(s.conn, string(raw))
}

func (s *codexAppServerSession) notify(method string, params any) error {
	return s.send(map[string]any{"method": method, "params": params})
}

// call sends one request and waits for its response, skipping notifications
// and server-initiated requests. A JSON-RPC error is returned as
// *codexRPCError; a closed stream or an expired ctx is a transport error.
func (s *codexAppServerSession) call(ctx context.Context, method string, params any, result any) error {
	s.nextID++
	id := strconv.Itoa(s.nextID)
	if err := s.send(map[string]any{"id": s.nextID, "method": method, "params": params}); err != nil {
		return fmt.Errorf("send %s: %w", method, err)
	}
	for {
		select {
		case msg, ok := <-s.messages:
			if !ok {
				<-s.readDone
				if ctx.Err() != nil {
					return &CodexProxyTimeoutError{Phase: method, Timeout: CodexSteerTimeout}
				}
				return fmt.Errorf("Codex app-server proxy closed the stream while waiting for %s: %w", method, s.readErr)
			}
			if msg.Method != "" || string(msg.ID) != id {
				continue
			}
			if msg.Error != nil {
				return msg.Error
			}
			if result == nil || len(msg.Result) == 0 {
				return nil
			}
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
			return nil
		case <-ctx.Done():
			return &CodexProxyTimeoutError{Phase: method, Timeout: CodexSteerTimeout}
		}
	}
}

// activeTurn returns the id of the turn currently in progress on the thread,
// or "" plus the typed simple-fallback reason when nothing steerable is
// running in this daemon. The stable `thread/read {includeTurns:true}` schema
// is the only turn-discovery call; the latest nonempty in-progress turn wins.
func (s *codexAppServerSession) activeTurn(ctx context.Context, threadID string) (string, string, error) {
	type threadRead struct {
		Thread struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
			Turns []codexTurn `json:"turns"`
		} `json:"thread"`
	}
	var history threadRead
	if err := s.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &history); err != nil {
		return "", "", fmt.Errorf("read Codex thread: %w", err)
	}
	if history.Thread.Status.Type != "active" {
		return "", "idle", nil
	}
	for index := len(history.Thread.Turns) - 1; index >= 0; index-- {
		if history.Thread.Turns[index].Status == "inProgress" && history.Thread.Turns[index].ID != "" {
			return history.Thread.Turns[index].ID, "", nil
		}
	}
	return "", "idle", nil
}

// close ends the session: the WebSocket close frame and the proxy's stdin go
// first so the vendor proxy exits on its own; a lingering process group is
// killed after codexProxyReapDelay.
func (s *codexAppServerSession) close() {
	s.once.Do(func() {
		close(s.stop)
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.reap()
	})
}

func (s *codexAppServerSession) reap() {
	waited := make(chan struct{})
	go func() {
		_ = s.proxy.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(codexProxyReapDelay):
		_ = killProcessGroup(s.proxy)
		<-waited
	}
}

// classifyCodexSteerRejection maps a `turn/steer` JSON-RPC error onto the
// documented simple-fallback reasons and reports false for everything else.
// The vendor rejects a steer with an invalid-request error in exactly three
// documented situations: the active turn cannot accept same-turn steering
// (structured `activeTurnNotSteerable` error info and the "cannot steer a
// review|compact turn" message), the expectedTurnId precondition no longer
// matches ("expected active turn id `x` but found `y`"), or the turn finished
// first ("no active turn to steer"). Unknown methods, malformed requests,
// sub-agent ownership rejections, and internal errors are not fallbacks: they
// mean the primitive is broken and must surface as delivery failures.
func classifyCodexSteerRejection(err *codexRPCError) (string, bool) {
	if err == nil {
		return "", false
	}
	if strings.Contains(string(err.Data), "activeTurnNotSteerable") {
		return "not_steerable", true
	}
	if err.Code != codexRPCInvalidRequest {
		return "", false
	}
	switch message := strings.TrimSpace(err.Message); {
	case strings.HasPrefix(message, "cannot steer a "):
		return "not_steerable", true
	case strings.HasPrefix(message, "expected active turn id "):
		return "not_steerable", true
	case message == "no active turn to steer":
		return "idle", true
	}
	return "", false
}

// codexTimeout builds the precise timeout error for a blocking phase and
// attaches the daemon/CLI version pair when the vendor CLI can report it.
func codexTimeout(path, phase string) error {
	return &CodexProxyTimeoutError{Phase: phase, Timeout: CodexSteerTimeout, Daemon: describeCodexDaemon(path)}
}

// describeCodexDaemon reports the installed CLI and running app-server
// versions from the documented `codex app-server daemon version` JSON, or ""
// when that is unavailable. It is only consulted on failure paths.
func describeCodexDaemon(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := codexCommand(ctx, path, "app-server", "daemon", "version").Output()
	if err != nil {
		return ""
	}
	var info struct {
		Status           string `json:"status"`
		CLIVersion       string `json:"cliVersion"`
		AppServerVersion string `json:"appServerVersion"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return ""
	}
	return fmt.Sprintf("codex cli=%s app-server=%s status=%s", info.CLIVersion, info.AppServerVersion, info.Status)
}

// DeliverCodexSteer performs one mid-turn steer through the documented
// app-server sequence: daemon start, proxy, WebSocket handshake, initialize,
// initialized, active-turn discovery, then `turn/steer` with the required
// expectedTurnId and typed text input. A clean idle thread or a documented
// rejection (not steerable, expected-turn race, turn finished first) is
// returned as a typed simple-fallback decision. Transport failures in the
// daemon/proxy/RPC path also degrade to the exact queue primitive with the
// truthful `transport_error` reason. Undocumented remote rejections remain
// retryable errors so a broken request contract is never masked.
func DeliverCodexSteer(ctx context.Context, body, target string, stderr io.Writer, clientVersion string) (bool, string, error) {
	stderr = writerOrDiscard(stderr)
	if strings.TrimSpace(target) == "" {
		return false, "", &UnavailableError{Message: "Codex steer requires a receiver-owned thread target"}
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return false, "", &UnavailableError{Message: "Codex delivery requires the codex CLI in PATH"}
	}
	operationCtx, cancel := context.WithTimeout(ctx, CodexSteerTimeout)
	defer cancel()
	session, err := openCodexAppServerSession(operationCtx, path, stderr, clientVersion)
	if err != nil {
		return codexTransportFallback(stderr, "app-server session", err)
	}
	defer session.close()
	turnID, reason, err := session.activeTurn(operationCtx, target)
	if err != nil {
		return codexTransportFallback(stderr, "thread/read", err)
	}
	if turnID == "" {
		fmt.Fprintf(stderr, "codex: no steerable turn in progress; delivering as simple via codex queue (fallback_reason=%s)\n", reason)
		return false, reason, nil
	}
	var steered struct {
		TurnID string `json:"turnId"`
	}
	err = session.call(operationCtx, "turn/steer", map[string]any{
		"threadId":       target,
		"expectedTurnId": turnID,
		"input":          []map[string]string{{"type": "text", "text": body}},
	}, &steered)
	if err != nil {
		var remote *codexRPCError
		if errors.As(err, &remote) {
			if reason, documented := classifyCodexSteerRejection(remote); documented {
				fmt.Fprintf(stderr, "codex: turn/steer rejected for expected turn %s (%v); delivering as simple via codex queue (fallback_reason=%s)\n", turnID, remote, reason)
				return false, reason, nil
			}
			// Anything else (unknown method, malformed request, ownership
			// rejection, internal failure) means the steer primitive itself is
			// broken or unavailable. Falling back to the queue would mask that,
			// so the delivery fails and stays undelivered for a retry.
			return false, "", fmt.Errorf("steer Codex turn (expected turn %s): unexpected app-server rejection: %w", turnID, remote)
		}
		return codexTransportFallback(stderr, "turn/steer", err)
	}
	// Evidence for operators: the turn that accepted the input is the exact
	// expected-turn precondition the daemon verified. No body is echoed.
	fmt.Fprintf(stderr, "codex: turn/steer accepted by turn %s (expected turn %s)\n", steered.TurnID, turnID)
	return true, "", nil
}

func codexTransportFallback(stderr io.Writer, phase string, err error) (bool, string, error) {
	fmt.Fprintf(stderr, "codex: %s failed (%v); delivering as simple via codex queue (fallback_reason=transport_error)\n", phase, err)
	return false, "transport_error", nil
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

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

// Codex steer transport (PAI-825 follow-up).
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

// codexSteerTimeout bounds one steer attempt end to end: daemon start, proxy
// spawn, WebSocket handshake, and the JSON-RPC exchange. Tests lower it.
var codexSteerTimeout = 20 * time.Second

// codexProxyMaxPayloadBytes caps one inbound frame. The stable
// `thread/read {includeTurns:true}` fallback returns the whole thread history
// in a single frame, which is tens of megabytes for a long gauntlet thread.
const codexProxyMaxPayloadBytes = 512 << 20

const codexProxyReapDelay = 2 * time.Second

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

// codexProxyTimeoutError names the wire phase that produced no answer within
// the steer budget so an operator can tell a dead daemon from a wrong
// framing assumption.
type codexProxyTimeoutError struct {
	phase   string
	timeout time.Duration
	daemon  string
}

func (e *codexProxyTimeoutError) Error() string {
	msg := fmt.Sprintf("Codex app-server proxy: no response to %s within %s (the proxied stream is a WebSocket byte pipe to the app-server daemon)", e.phase, e.timeout)
	if e.daemon != "" {
		msg += "; " + e.daemon
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
	cmd.Cancel = func() error { return signalOwnedProcess(cmd, true) }
	cmd.WaitDelay = codexProxyReapDelay
	return cmd
}

// openCodexAppServerSession starts the daemon idempotently, spawns the vendor
// proxy, performs the WebSocket Upgrade handshake, and completes the
// documented initialize/initialized exchange. Every blocking step is bounded
// by ctx; on timeout the proxy process group is killed before returning.
func openCodexAppServerSession(ctx context.Context, path string) (*codexAppServerSession, error) {
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
		"clientInfo":   map[string]string{"name": "paimos", "title": "PAIMOS agent bus", "version": Version},
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
					return &codexProxyTimeoutError{phase: method, timeout: codexSteerTimeout}
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
			return &codexProxyTimeoutError{phase: method, timeout: codexSteerTimeout}
		}
	}
}

// activeTurn returns the id of the turn currently in progress on the thread,
// or "" plus the typed simple-fallback reason when nothing steerable is
// running in this daemon. The cheap `thread/read` status check runs first;
// only an active thread pays for turn discovery, which prefers the paginated
// `thread/turns/list` page and falls back to the stable full-history
// `thread/read {includeTurns:true}` when the daemon rejects the paginated
// method (0.150.x gates it behind the experimentalApi capability).
func (s *codexAppServerSession) activeTurn(ctx context.Context, threadID string) (string, string, error) {
	type threadRead struct {
		Thread struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
			Turns []codexTurn `json:"turns"`
		} `json:"thread"`
	}
	var status threadRead
	if err := s.call(ctx, "thread/read", map[string]any{"threadId": threadID}, &status); err != nil {
		return "", "", fmt.Errorf("read Codex thread: %w", err)
	}
	if status.Thread.Status.Type != "active" {
		return "", "idle", nil
	}
	var turns []codexTurn
	var page struct {
		Data []codexTurn `json:"data"`
	}
	err := s.call(ctx, "thread/turns/list", map[string]any{
		"threadId": threadID, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded",
	}, &page)
	var remote *codexRPCError
	switch {
	case err == nil:
		turns = page.Data
	case errors.As(err, &remote):
		var history threadRead
		if err := s.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &history); err != nil {
			return "", "", fmt.Errorf("read Codex thread turns: %w", err)
		}
		turns = history.Thread.Turns
	default:
		return "", "", fmt.Errorf("list Codex thread turns: %w", err)
	}
	for index := len(turns) - 1; index >= 0; index-- {
		if turns[index].Status == "inProgress" && turns[index].ID != "" {
			return turns[index].ID, "", nil
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
		_ = signalOwnedProcess(s.proxy, true)
		<-waited
	}
}

// codexTimeout builds the precise timeout error for a blocking phase and
// attaches the daemon/CLI version pair when the vendor CLI can report it.
func codexTimeout(path, phase string) error {
	return &codexProxyTimeoutError{phase: phase, timeout: codexSteerTimeout, daemon: describeCodexDaemon(path)}
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

// deliverCodexSteer performs one mid-turn steer through the documented
// app-server sequence: daemon start, proxy, WebSocket handshake, initialize,
// initialized, active-turn discovery, then `turn/steer` with the required
// expectedTurnId and typed text input. A clean idle thread or a
// rejected/raced steer is returned as a typed simple-fallback decision;
// transport and handshake failures remain retryable errors.
func deliverCodexSteer(ctx context.Context, body, target string) (bool, string, error) {
	if strings.TrimSpace(target) == "" {
		return false, "", &adapterUnavailableError{message: "Codex steer requires a receiver-owned thread target"}
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return false, "", &adapterUnavailableError{message: "Codex delivery requires the codex CLI in PATH"}
	}
	operationCtx, cancel := context.WithTimeout(ctx, codexSteerTimeout)
	defer cancel()
	session, err := openCodexAppServerSession(operationCtx, path)
	if err != nil {
		return false, "", err
	}
	defer session.close()
	turnID, reason, err := session.activeTurn(operationCtx, target)
	if err != nil {
		return false, "", err
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
			fmt.Fprintf(stderr, "codex: turn/steer rejected for expected turn %s (%v); delivering as simple via codex queue (fallback_reason=not_steerable)\n", turnID, remote)
			return false, "not_steerable", nil
		}
		return false, "", fmt.Errorf("steer Codex turn: %w", err)
	}
	// Evidence for operators: the turn that accepted the input is the exact
	// expected-turn precondition the daemon verified. No body is echoed.
	fmt.Fprintf(stderr, "codex: turn/steer accepted by turn %s (expected turn %s)\n", steered.TurnID, turnID)
	return true, "", nil
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// TestHelperFakeCodex is not a test. It is the body of the fake `codex` CLI
// that the shell shim from installFakeCodexAppServer re-executes for
// `app-server proxy`. Like the vendor proxy it moves raw bytes between stdio
// and a websocket server, so the client under test must perform a real HTTP
// Upgrade handshake and frame every JSON-RPC message.
func TestHelperFakeCodex(t *testing.T) {
	if os.Getenv("PAIMOS_FAKE_CODEX_HELPER") != "1" {
		t.Skip("fake codex helper process only")
	}
	os.Exit(runFakeCodexProxy(flag.Args()))
}

func runFakeCodexProxy(args []string) int {
	if len(args) < 2 || args[0] != "app-server" || args[1] != "proxy" {
		fmt.Fprintf(os.Stderr, "fake codex: unsupported argv %q\n", args)
		return 2
	}
	if pidFile := os.Getenv("PAIMOS_TEST_PIDFILE"); pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if os.Getenv("PAIMOS_TEST_SCENARIO") == "hang" {
		// The real daemon behaves this way when it receives JSON lines instead
		// of an HTTP request head: it waits forever and never writes.
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: listen: %v\n", err)
		return 2
	}
	server := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   fakeCodexAppServer,
	}
	go func() { _ = http.Serve(listener, server) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: dial: %v\n", err)
		return 2
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(os.Stdout, conn)
		done <- struct{}{}
	}()
	<-done
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	return 0
}

// fakeCodexAppServer scripts the daemon side from environment variables:
//
//	PAIMOS_TEST_THREAD_STATUS  thread/read status type (default active)
//	PAIMOS_TEST_TURNS_LIST     JSON {"result":{...}} or {"error":{...}} for thread/turns/list
//	PAIMOS_TEST_THREAD_TURNS   JSON turns array for thread/read includeTurns
//	PAIMOS_TEST_STEER          JSON {"result":{...}} or {"error":{...}} for turn/steer
//	PAIMOS_TEST_FRAMES         file receiving every client frame, one per line
func fakeCodexAppServer(ws *websocket.Conn) {
	defer ws.Close()
	frames, _ := os.OpenFile(os.Getenv("PAIMOS_TEST_FRAMES"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if frames != nil {
		defer frames.Close()
	}
	reply := func(id json.RawMessage, envelope string) {
		var body map[string]any
		if envelope == "" || json.Unmarshal([]byte(envelope), &body) != nil {
			body = map[string]any{"error": map[string]any{"code": -32603, "message": "fake codex: bad scripted envelope"}}
		}
		body["id"] = id
		raw, _ := json.Marshal(body)
		_ = websocket.Message.Send(ws, string(raw))
	}
	for {
		var text string
		if err := websocket.Message.Receive(ws, &text); err != nil {
			return
		}
		if frames != nil {
			_, _ = frames.WriteString(text + "\n")
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				IncludeTurns bool `json:"includeTurns"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(text), &request); err != nil || len(request.ID) == 0 {
			continue
		}
		switch request.Method {
		case "initialize":
			_ = websocket.Message.Send(ws, `{"method":"remoteControl/status/changed","params":{"status":"disabled"}}`)
			reply(request.ID, `{"result":{"userAgent":"fake-codex"}}`)
		case "thread/read":
			status := os.Getenv("PAIMOS_TEST_THREAD_STATUS")
			if status == "" {
				status = "active"
			}
			turns := "[]"
			if request.Params.IncludeTurns {
				if scripted := os.Getenv("PAIMOS_TEST_THREAD_TURNS"); scripted != "" {
					turns = scripted
				}
			}
			reply(request.ID, `{"result":{"thread":{"id":"thread","status":{"type":"`+status+`","activeFlags":[]},"turns":`+turns+`}}}`)
		case "thread/turns/list":
			envelope := os.Getenv("PAIMOS_TEST_TURNS_LIST")
			if envelope == "" {
				envelope = `{"result":{"data":[]}}`
			}
			reply(request.ID, envelope)
		case "turn/steer":
			envelope := os.Getenv("PAIMOS_TEST_STEER")
			if envelope == "" {
				envelope = `{"result":{"turnId":"turn-live"}}`
			}
			reply(request.ID, envelope)
		default:
			reply(request.ID, `{"error":{"code":-32601,"message":"method not found"}}`)
		}
	}
}

// installFakeCodexAppServer puts a fake `codex` CLI first in PATH. `queue`
// and `app-server daemon` calls only record argv; `app-server proxy` runs the
// websocket helper above as a grandchild of the shim, mirroring the npm
// wrapper's Node parent plus native child layout.
func installFakeCodexAppServer(t *testing.T) (argsFile, framesFile string) {
	t.Helper()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	framesFile = filepath.Join(dir, "frames")
	body := `#!/bin/sh
printf '%s\n' "$@" >> "$PAIMOS_TEST_ARGS"
if [ "$1" = "queue" ]; then exit 0; fi
if [ "$1" = "app-server" ] && [ "$2" = "daemon" ] && [ "$3" = "version" ]; then
  printf '%s\n' '{"status":"running","cliVersion":"0.149.1-fake","appServerVersion":"0.150.1-fake"}'
  exit 0
fi
if [ "$1" = "app-server" ] && [ "$2" = "daemon" ]; then exit 0; fi
if [ "$1" = "app-server" ] && [ "$2" = "proxy" ]; then
  PAIMOS_FAKE_CODEX_HELPER=1 "$PAIMOS_TEST_HELPER" -test.run='^TestHelperFakeCodex$' -- "$@"
  exit $?
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_HELPER", helper)
	t.Setenv("PAIMOS_TEST_ARGS", argsFile)
	t.Setenv("PAIMOS_TEST_FRAMES", framesFile)
	t.Setenv("PAIMOS_TEST_SCENARIO", "")
	t.Setenv("PAIMOS_TEST_THREAD_STATUS", "")
	t.Setenv("PAIMOS_TEST_TURNS_LIST", "")
	t.Setenv("PAIMOS_TEST_THREAD_TURNS", "")
	t.Setenv("PAIMOS_TEST_STEER", "")
	t.Setenv("PAIMOS_TEST_PIDFILE", "")
	return argsFile, framesFile
}

func steerMessage(target string) messageEnvelope {
	return messageEnvelope{DeliveryLevel: "steer", DeliveryWork: &messageDeliveryWork{
		DeliveryID: "delivery-" + target, Adapter: "codex", TargetRef: target, MaximumLevel: "steer", RequestedLevel: "steer",
	}}
}

func readFrames(t *testing.T, framesFile string) string {
	t.Helper()
	raw, err := os.ReadFile(framesFile)
	if err != nil {
		t.Fatalf("no proxy frames were recorded: %v", err)
	}
	return string(raw)
}

func TestDeliverCodexMessageSteersInProgressTurn(t *testing.T) {
	argsFile, framesFile := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_TURNS_LIST", `{"result":{"data":[{"id":"turn-live","status":"inProgress"}],"nextCursor":null}}`)
	t.Setenv("PAIMOS_TEST_STEER", `{"result":{"turnId":"turn-live"}}`)
	outcome, err := deliverCodexMessage(context.Background(), steerMessage("thread-live"), "steer payload", "ignored-process-target", "queue")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.EffectiveLevel != "steer" || outcome.FallbackReason != "" {
		t.Fatalf("outcome=%#v", outcome)
	}
	args, _ := os.ReadFile(argsFile)
	if strings.Contains(string(args), "queue\n") {
		t.Fatalf("successful steer unexpectedly queued: %q", args)
	}
	if !strings.Contains(string(args), "app-server\ndaemon\nstart\n") || !strings.Contains(string(args), "app-server\nproxy\n") {
		t.Fatalf("argv missing daemon start or proxy: %q", args)
	}
	frames := readFrames(t, framesFile)
	for _, required := range []string{
		`"method":"initialize"`, `"name":"paimos"`, `"experimentalApi":true`,
		`"method":"initialized"`,
		`"method":"thread/read"`, `"threadId":"thread-live"`,
		`"method":"thread/turns/list"`, `"limit":1`, `"sortDirection":"desc"`, `"itemsView":"notLoaded"`,
		`"method":"turn/steer"`, `"expectedTurnId":"turn-live"`, `"type":"text"`, `"text":"steer payload"`,
	} {
		if !strings.Contains(frames, required) {
			t.Fatalf("frames missing %s: %s", required, frames)
		}
	}
	if strings.Contains(frames, `"jsonrpc"`) {
		t.Fatalf("frames must omit the jsonrpc header on the wire: %s", frames)
	}
	if strings.Contains(frames, `"includeTurns":true`) {
		t.Fatalf("paginated turn page succeeded; full history must not be loaded: %s", frames)
	}
	order := []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"thread/read"`, `"method":"thread/turns/list"`, `"method":"turn/steer"`}
	last := -1
	for _, step := range order {
		index := strings.Index(frames, step)
		if index <= last {
			t.Fatalf("frames out of documented order at %s: %s", step, frames)
		}
		last = index
	}
}

func TestDeliverCodexMessageBusLevelIgnoresLegacyProcessMode(t *testing.T) {
	argsFile, _ := installFakeCodexAppServer(t)
	message := messageEnvelope{DeliveryLevel: "simple", DeliveryWork: &messageDeliveryWork{
		DeliveryID: "delivery-simple", Adapter: "codex", TargetRef: "thread-simple", MaximumLevel: "steer", RequestedLevel: "simple",
	}}
	outcome, err := deliverCodexMessage(context.Background(), message, "simple payload", "ignored-process-target", "steer")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != "" {
		t.Fatalf("outcome=%#v", outcome)
	}
	args, _ := os.ReadFile(argsFile)
	if got, want := string(args), "queue\n--thread\nthread-simple\n--message\nsimple payload\n"; got != want {
		t.Fatalf("argv=%q want exact per-message queue %q", got, want)
	}
}

func TestDeliverCodexMessageFallsBackToQueueWhenIdle(t *testing.T) {
	for _, status := range []string{"idle", "notLoaded"} {
		t.Run(status, func(t *testing.T) {
			argsFile, framesFile := installFakeCodexAppServer(t)
			t.Setenv("PAIMOS_TEST_THREAD_STATUS", status)
			outcome, err := deliverCodexMessage(context.Background(), steerMessage("thread-idle"), "idle payload", "ignored", "queue")
			if err != nil {
				t.Fatal(err)
			}
			if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != "idle" {
				t.Fatalf("outcome=%#v", outcome)
			}
			args, _ := os.ReadFile(argsFile)
			if !strings.Contains(string(args), "queue\n--thread\nthread-idle\n--message\nidle payload\n") {
				t.Fatalf("queue argv missing: %q", args)
			}
			frames := readFrames(t, framesFile)
			for _, forbidden := range []string{`"method":"thread/turns/list"`, `"includeTurns":true`, `"method":"turn/steer"`} {
				if strings.Contains(frames, forbidden) {
					t.Fatalf("an inactive thread must not pay for %s: %s", forbidden, frames)
				}
			}
		})
	}
}

func TestDeliverCodexMessageActiveThreadWithoutInProgressTurnFallsBackToQueue(t *testing.T) {
	argsFile, _ := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_TURNS_LIST", `{"result":{"data":[{"id":"turn-old","status":"completed"}]}}`)
	outcome, err := deliverCodexMessage(context.Background(), steerMessage("thread-settling"), "settling payload", "ignored", "queue")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != "idle" {
		t.Fatalf("outcome=%#v", outcome)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "queue\n--thread\nthread-settling\n--message\nsettling payload\n") {
		t.Fatalf("queue argv missing: %q", args)
	}
}

func TestDeliverCodexMessageFallsBackToFullHistoryWhenTurnPageIsGated(t *testing.T) {
	argsFile, framesFile := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_TURNS_LIST", `{"error":{"code":-32600,"message":"thread/turns/list requires experimentalApi capability"}}`)
	t.Setenv("PAIMOS_TEST_THREAD_TURNS", `[{"id":"turn-old","status":"completed"},{"id":"turn-live","status":"inProgress"}]`)
	t.Setenv("PAIMOS_TEST_STEER", `{"result":{"turnId":"turn-live"}}`)
	outcome, err := deliverCodexMessage(context.Background(), steerMessage("thread-gated"), "gated payload", "ignored", "queue")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.EffectiveLevel != "steer" || outcome.FallbackReason != "" {
		t.Fatalf("outcome=%#v", outcome)
	}
	args, _ := os.ReadFile(argsFile)
	if strings.Contains(string(args), "queue\n") {
		t.Fatalf("successful steer unexpectedly queued: %q", args)
	}
	frames := readFrames(t, framesFile)
	for _, required := range []string{`"method":"thread/turns/list"`, `"includeTurns":true`, `"expectedTurnId":"turn-live"`} {
		if !strings.Contains(frames, required) {
			t.Fatalf("frames missing %s: %s", required, frames)
		}
	}
}

func TestDeliverCodexMessageFallsBackOnNotSteerableOrTurnRace(t *testing.T) {
	for _, errorData := range []string{
		`{"code":-32600,"message":"active turn cannot accept steer","data":{"codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"review"}}}}`,
		`{"code":-32600,"message":"expected turn does not match the active turn"}`,
	} {
		t.Run(errorData, func(t *testing.T) {
			argsFile, _ := installFakeCodexAppServer(t)
			t.Setenv("PAIMOS_TEST_TURNS_LIST", `{"result":{"data":[{"id":"turn-raced","status":"inProgress"}]}}`)
			t.Setenv("PAIMOS_TEST_STEER", `{"error":`+errorData+`}`)
			outcome, err := deliverCodexMessage(context.Background(), steerMessage("thread-raced"), "raced payload", "ignored", "queue")
			if err != nil {
				t.Fatal(err)
			}
			if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != "not_steerable" {
				t.Fatalf("outcome=%#v", outcome)
			}
			args, _ := os.ReadFile(argsFile)
			if !strings.Contains(string(args), "queue\n--thread\nthread-raced\n--message\nraced payload\n") {
				t.Fatalf("queue argv missing: %q", args)
			}
		})
	}
}

func TestDeliverCodexSteerRequiresTarget(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	_, _, err := deliverCodexSteer(context.Background(), "no target", "")
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "receiver-owned thread target") {
		t.Fatalf("error=%#v want missing target guidance", err)
	}
}

func TestDeliverCodexSteerSilentProxyFailsPreciselyWithinBudget(t *testing.T) {
	_, _ = installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_SCENARIO", "hang")
	previous := codexSteerTimeout
	codexSteerTimeout = 2 * time.Second
	t.Cleanup(func() { codexSteerTimeout = previous })
	started := time.Now()
	steered, reason, err := deliverCodexSteer(context.Background(), "hang payload", "thread-hang")
	elapsed := time.Since(started)
	if steered || reason != "" {
		t.Fatalf("steered=%v reason=%q on a silent proxy", steered, reason)
	}
	var timeout *codexProxyTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("error=%#v want a typed proxy timeout", err)
	}
	for _, required := range []string{"no response to websocket handshake within 2s", "WebSocket byte pipe", "codex cli=0.149.1-fake app-server=0.150.1-fake"} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("error=%q missing %q", err, required)
		}
	}
	if elapsed > 8*time.Second {
		t.Fatalf("silent proxy took %s; the budget plus reap delay must bound it", elapsed)
	}
	var unavailable *adapterUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("a transport timeout must stay retryable, got adapter unavailable: %v", err)
	}
}

// TestCodexAppServerProxyReadOnlyProbe is opt-in evidence gathering against
// the real local daemon: it performs the documented handshake through the
// vendor proxy and reads the named thread's status without steering it.
func TestCodexAppServerProxyReadOnlyProbe(t *testing.T) {
	threadID := strings.TrimSpace(os.Getenv("PAIMOS_CODEX_PROBE_THREAD"))
	if threadID == "" {
		t.Skip("set PAIMOS_CODEX_PROBE_THREAD to a real local Codex thread id")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex CLI not in PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexSteerTimeout)
	defer cancel()
	started := time.Now()
	session, err := openCodexAppServerSession(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	handshake := time.Since(started)
	turnID, reason, err := session.activeTurn(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PAI-825_PROBE thread=%s handshake_ms=%d total_ms=%d in_progress_turn=%q fallback_reason=%q",
		threadID, handshake.Milliseconds(), time.Since(started).Milliseconds(), turnID, reason)
}

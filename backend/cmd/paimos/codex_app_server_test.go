// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
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
//	PAIMOS_TEST_INITIALIZE     JSON {"result":{...}} or {"error":{...}} for initialize
//	PAIMOS_TEST_THREAD_STATUS  thread/read status type (default active)
//	PAIMOS_TEST_THREAD_READ    JSON {"result":{...}} or {"error":{...}} for thread/read
//	PAIMOS_TEST_THREAD_TURNS   JSON turns array for the default thread/read result
//	PAIMOS_TEST_STEER          JSON {"result":{...}} or {"error":{...}} for turn/steer
//	PAIMOS_TEST_CLOSE_ON       method whose request closes the transport without a response
//	PAIMOS_TEST_DECOY_ID       when set, send a wrong-id response before the real one
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
		if os.Getenv("PAIMOS_TEST_CLOSE_ON") == request.Method {
			if diagnostic := os.Getenv("PAIMOS_TEST_VENDOR_STDERR"); diagnostic != "" {
				fmt.Fprintln(os.Stderr, diagnostic)
			}
			return
		}
		if os.Getenv("PAIMOS_TEST_DECOY_ID") != "" {
			reply(json.RawMessage(`999999`), `{"result":{"turnId":"turn-decoy"}}`)
		}
		switch request.Method {
		case "initialize":
			_ = websocket.Message.Send(ws, `{"method":"remoteControl/status/changed","params":{"status":"disabled"}}`)
			envelope := os.Getenv("PAIMOS_TEST_INITIALIZE")
			if envelope == "" {
				envelope = `{"result":{"userAgent":"fake-codex"}}`
			}
			reply(request.ID, envelope)
		case "thread/read":
			if envelope := os.Getenv("PAIMOS_TEST_THREAD_READ"); envelope != "" {
				reply(request.ID, envelope)
				continue
			}
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
if [ "$1" = "queue" ]; then
  printf '%s\n' "$PAIMOS_TEST_QUEUE_VENDOR_OUTPUT"
  printf '%s\n' "$PAIMOS_TEST_QUEUE_VENDOR_OUTPUT" >&2
  exit "${PAIMOS_TEST_QUEUE_EXIT:-0}"
fi
if [ "$1" = "app-server" ] && [ "$2" = "daemon" ] && [ "$3" = "version" ]; then
  printf '%s\n' '{"status":"running","cliVersion":"0.149.1-fake","appServerVersion":"0.150.1-fake"}'
  exit 0
fi
if [ "$1" = "app-server" ] && [ "$2" = "daemon" ]; then
  if [ -n "$PAIMOS_TEST_DAEMON_FAIL" ]; then
    printf '%s\n' "$PAIMOS_TEST_VENDOR_STDERR" >&2
    exit 42
  fi
  exit 0
fi
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
	t.Setenv("PAIMOS_TEST_DAEMON_FAIL", "")
	t.Setenv("PAIMOS_TEST_INITIALIZE", "")
	t.Setenv("PAIMOS_TEST_THREAD_STATUS", "")
	t.Setenv("PAIMOS_TEST_THREAD_READ", "")
	t.Setenv("PAIMOS_TEST_THREAD_TURNS", "")
	t.Setenv("PAIMOS_TEST_STEER", "")
	t.Setenv("PAIMOS_TEST_CLOSE_ON", "")
	t.Setenv("PAIMOS_TEST_DECOY_ID", "")
	t.Setenv("PAIMOS_TEST_VENDOR_STDERR", "")
	t.Setenv("PAIMOS_TEST_QUEUE_VENDOR_OUTPUT", "")
	t.Setenv("PAIMOS_TEST_QUEUE_EXIT", "")
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
	t.Setenv("PAIMOS_TEST_THREAD_TURNS", `[{"id":"turn-old","status":"inProgress"},{"id":"turn-complete","status":"completed"},{"id":"turn-live","status":"inProgress"}]`)
	t.Setenv("PAIMOS_TEST_STEER", `{"result":{"turnId":"turn-live"}}`)
	t.Setenv("PAIMOS_TEST_DECOY_ID", "1")
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
		`"method":"thread/read"`, `"threadId":"thread-live"`, `"includeTurns":true`,
		`"method":"turn/steer"`, `"expectedTurnId":"turn-live"`, `"type":"text"`, `"text":"steer payload"`,
	} {
		if !strings.Contains(frames, required) {
			t.Fatalf("frames missing %s: %s", required, frames)
		}
	}
	if strings.Contains(frames, `"jsonrpc"`) {
		t.Fatalf("frames must omit the jsonrpc header on the wire: %s", frames)
	}
	if strings.Contains(frames, `"method":"thread/turns/list"`) {
		t.Fatalf("turn discovery must use only the stable thread/read schema: %s", frames)
	}
	if got := strings.Count(frames, `"method":"thread/read"`); got != 1 {
		t.Fatalf("thread/read calls=%d want exactly 1: %s", got, frames)
	}
	order := []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"thread/read"`, `"method":"turn/steer"`}
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

func TestDeliverCodexMessageTransportFailuresFallBackToExactQueue(t *testing.T) {
	const (
		leakTarget = "fixture-encrypted-target-ref-never-log"
		leakBody   = "Bearer-sk-body-never-log"
		leakSecret = "sk-secret-like-never-log-abcdefghijklmnopqrstuvwxyz"
	)
	leakDiagnostic := strings.Join([]string{leakTarget, leakBody, leakSecret}, " ")
	for _, tc := range []struct {
		name  string
		phase string
		setup func(*testing.T)
	}{
		{"daemon start", "app-server session", func(t *testing.T) {
			t.Setenv("PAIMOS_TEST_DAEMON_FAIL", "1")
			t.Setenv("PAIMOS_TEST_VENDOR_STDERR", leakDiagnostic)
		}},
		{"initialize", "app-server session", func(t *testing.T) {
			t.Setenv("PAIMOS_TEST_CLOSE_ON", "initialize")
			t.Setenv("PAIMOS_TEST_VENDOR_STDERR", leakDiagnostic)
		}},
		{"thread read", "thread/read", func(t *testing.T) {
			t.Setenv("PAIMOS_TEST_CLOSE_ON", "thread/read")
			t.Setenv("PAIMOS_TEST_VENDOR_STDERR", leakDiagnostic)
		}},
		{"turn steer transport", "turn/steer", func(t *testing.T) {
			t.Setenv("PAIMOS_TEST_THREAD_TURNS", `[{"id":"turn-live","status":"inProgress"}]`)
			t.Setenv("PAIMOS_TEST_CLOSE_ON", "turn/steer")
			t.Setenv("PAIMOS_TEST_VENDOR_STDERR", leakDiagnostic)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile, _ := installFakeCodexAppServer(t)
			var out, errOut bytes.Buffer
			oldStdout, oldStderr := stdout, stderr
			stdout, stderr = &out, &errOut
			t.Cleanup(func() { stdout, stderr = oldStdout, oldStderr })
			t.Setenv("PAIMOS_TEST_QUEUE_VENDOR_OUTPUT", leakDiagnostic)
			tc.setup(t)
			outcome, err := deliverCodexMessage(context.Background(), steerMessage(leakTarget), leakBody, "ignored", "queue")
			if err != nil {
				t.Fatal(err)
			}
			if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != "transport_error" {
				t.Fatalf("outcome=%#v want simple/transport_error", outcome)
			}
			args, _ := os.ReadFile(argsFile)
			if !strings.Contains(string(args), "queue\n--thread\n"+leakTarget+"\n--message\n"+leakBody+"\n") {
				t.Fatalf("exact queue argv missing: %q", args)
			}
			diagnostic := errOut.String()
			for _, required := range []string{tc.phase + " failed", "fallback_reason=transport_error"} {
				if !strings.Contains(diagnostic, required) {
					t.Fatalf("stderr=%q missing controlled diagnostic %q", diagnostic, required)
				}
			}
			combinedOutput := out.String() + diagnostic
			for _, forbidden := range []string{leakTarget, leakBody, leakSecret} {
				if strings.Contains(combinedOutput, forbidden) {
					t.Fatalf("listener output leaked %q: stdout=%q stderr=%q", forbidden, out.String(), diagnostic)
				}
			}
		})
	}
}

// A completed JSON-RPC response is not a transport failure. Remote rejections
// during session initialization or thread discovery signal request/schema
// drift and must remain hard delivery errors instead of silently queueing.
func TestDeliverCodexMessageRemoteSessionRejectionsFailWithoutQueue(t *testing.T) {
	const (
		leakTarget = "fixture-encrypted-target-ref-never-log"
		leakBody   = "Bearer-sk-body-never-log"
		leakSecret = "sk-secret-like-never-log-abcdefghijklmnopqrstuvwxyz"
	)
	leakDiagnostic := strings.Join([]string{leakTarget, leakBody, leakSecret}, " ")
	for _, tc := range []struct {
		name, phase, env string
	}{
		{"initialize internal error", "initialize Codex app-server", "PAIMOS_TEST_INITIALIZE"},
		{"thread read invalid request", "read Codex thread", "PAIMOS_TEST_THREAD_READ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile, _ := installFakeCodexAppServer(t)
			var out, errOut bytes.Buffer
			oldStdout, oldStderr := stdout, stderr
			stdout, stderr = &out, &errOut
			t.Cleanup(func() { stdout, stderr = oldStdout, oldStderr })
			code := -32603
			if tc.env == "PAIMOS_TEST_THREAD_READ" {
				code = -32600
			}
			t.Setenv(tc.env, fmt.Sprintf(`{"error":{"code":%d,"message":%q}}`, code, leakDiagnostic))
			outcome, err := deliverCodexMessage(context.Background(), steerMessage(leakTarget), leakBody, "ignored", "queue")
			if err == nil || outcome != nil {
				t.Fatalf("outcome=%#v error=%v want hard remote rejection", outcome, err)
			}
			if !strings.Contains(err.Error(), tc.phase+": unexpected app-server rejection") || !strings.Contains(err.Error(), fmt.Sprintf("code %d", code)) {
				t.Fatalf("error=%q must carry only controlled phase and code", err)
			}
			args, _ := os.ReadFile(argsFile)
			if strings.Contains(string(args), "queue\n") {
				t.Fatalf("remote rejection must not fall back to codex queue: %q", args)
			}
			combinedOutput := out.String() + errOut.String() + err.Error()
			for _, forbidden := range []string{leakTarget, leakBody, leakSecret} {
				if strings.Contains(combinedOutput, forbidden) {
					t.Fatalf("remote rejection leaked %q: stdout=%q stderr=%q error=%q", forbidden, out.String(), errOut.String(), err)
				}
			}
		})
	}
}

func TestDeliverCodexMessageQueueFailureDoesNotLeakVendorOutput(t *testing.T) {
	const (
		leakTarget = "fixture-encrypted-target-ref-never-log"
		leakBody   = "Bearer-sk-body-never-log"
		leakSecret = "sk-secret-like-never-log-abcdefghijklmnopqrstuvwxyz"
	)
	argsFile, _ := installFakeCodexAppServer(t)
	var out, errOut bytes.Buffer
	oldStdout, oldStderr := stdout, stderr
	stdout, stderr = &out, &errOut
	t.Cleanup(func() { stdout, stderr = oldStdout, oldStderr })
	t.Setenv("PAIMOS_TEST_DAEMON_FAIL", "1")
	t.Setenv("PAIMOS_TEST_QUEUE_VENDOR_OUTPUT", strings.Join([]string{leakTarget, leakBody, leakSecret}, " "))
	t.Setenv("PAIMOS_TEST_QUEUE_EXIT", "23")
	outcome, err := deliverCodexMessage(context.Background(), steerMessage(leakTarget), leakBody, "ignored", "queue")
	if err == nil || outcome != nil || err.Error() != "deliver to Codex thread: exit status 23" {
		t.Fatalf("outcome=%#v error=%v want controlled queue failure", outcome, err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "queue\n--thread\n"+leakTarget+"\n--message\n"+leakBody+"\n") {
		t.Fatalf("exact queue argv missing: %q", args)
	}
	combinedOutput := out.String() + errOut.String() + err.Error()
	for _, forbidden := range []string{leakTarget, leakBody, leakSecret} {
		if strings.Contains(combinedOutput, forbidden) {
			t.Fatalf("queue failure leaked %q: stdout=%q stderr=%q error=%q", forbidden, out.String(), errOut.String(), err)
		}
	}
	if !strings.Contains(errOut.String(), "fallback_reason=transport_error") {
		t.Fatalf("stderr=%q missing controlled transport fallback", errOut.String())
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
			if !strings.Contains(frames, `"includeTurns":true`) {
				t.Fatalf("thread discovery must use includeTurns even for an inactive result: %s", frames)
			}
			for _, forbidden := range []string{`"method":"thread/turns/list"`, `"method":"turn/steer"`} {
				if strings.Contains(frames, forbidden) {
					t.Fatalf("an inactive thread must not pay for %s: %s", forbidden, frames)
				}
			}
		})
	}
}

func TestDeliverCodexMessageActiveThreadWithoutInProgressTurnFallsBackToQueue(t *testing.T) {
	argsFile, _ := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_THREAD_TURNS", `[{"id":"turn-old","status":"completed"}]`)
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

// The rejection fixtures below are the exact shapes the app-server emits
// (codex-rs app-server turn_processor `turn_steer_inner`): every documented
// precondition failure is JSON-RPC -32600 `invalid request`, the
// not-steerable case additionally serializes a TurnError with
// `codexErrorInfo.activeTurnNotSteerable` into `error.data`.
const (
	codexRejectionReviewTurn = `{"code":-32600,"message":"cannot steer a review turn","data":{"message":"cannot steer a review turn","codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"review"}},"additionalDetails":null,"misalignment":null}}`
	codexRejectionTurnRace   = "{\"code\":-32600,\"message\":\"expected active turn id `turn-raced` but found `turn-next`\"}"
	codexRejectionNoTurn     = `{"code":-32600,"message":"no active turn to steer"}`
)

func TestDeliverCodexMessageFallsBackOnDocumentedSteerRejections(t *testing.T) {
	const (
		leakTarget = "fixture-encrypted-target-ref-never-log"
		leakBody   = "Bearer-sk-body-never-log"
		leakSecret = "sk-secret-like-never-log-abcdefghijklmnopqrstuvwxyz"
	)
	for _, tc := range []struct {
		name, rejection, reason string
	}{
		{"active turn not steerable (review)", `{"code":-32600,"message":"cannot steer a review turn ` + leakTarget + ` ` + leakBody + ` ` + leakSecret + `","data":{"codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"review"}}}}`, "not_steerable"},
		{"compact turn without structured data", `{"code":-32600,"message":"cannot steer a compact turn ` + leakTarget + ` ` + leakBody + ` ` + leakSecret + `"}`, "not_steerable"},
		{"expected turn race", `{"code":-32600,"message":"expected active turn id ` + "`turn-raced`" + ` but found ` + "`turn-next`" + ` ` + leakTarget + ` ` + leakBody + ` ` + leakSecret + `"}`, "not_steerable"},
		{"turn finished before steer", `{"code":-32600,"message":"no active turn to steer","data":{"additionalDetails":"` + leakTarget + ` ` + leakBody + ` ` + leakSecret + `"}}`, "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile, _ := installFakeCodexAppServer(t)
			var errOut bytes.Buffer
			oldStderr := stderr
			stderr = &errOut
			t.Cleanup(func() { stderr = oldStderr })
			t.Setenv("PAIMOS_TEST_THREAD_TURNS", `[{"id":"turn-raced","status":"inProgress"}]`)
			t.Setenv("PAIMOS_TEST_STEER", `{"error":`+tc.rejection+`}`)
			outcome, err := deliverCodexMessage(context.Background(), steerMessage(leakTarget), leakBody, "ignored", "queue")
			if err != nil {
				t.Fatal(err)
			}
			if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != tc.reason {
				t.Fatalf("outcome=%#v want simple/%s", outcome, tc.reason)
			}
			args, _ := os.ReadFile(argsFile)
			if !strings.Contains(string(args), "queue\n--thread\n"+leakTarget+"\n--message\n"+leakBody+"\n") {
				t.Fatalf("queue argv missing: %q", args)
			}
			diagnostic := errOut.String()
			if !strings.Contains(diagnostic, "turn/steer rejected") || !strings.Contains(diagnostic, "fallback_reason="+tc.reason) {
				t.Fatalf("stderr=%q missing controlled fallback diagnostic", diagnostic)
			}
			for _, forbidden := range []string{leakTarget, leakBody, leakSecret} {
				if strings.Contains(diagnostic, forbidden) {
					t.Fatalf("stderr leaked %q: %q", forbidden, diagnostic)
				}
			}
		})
	}
}

// An undocumented rejection means the steer primitive itself is broken or
// unavailable (method missing, request shape drift, ownership rule, internal
// failure). Queueing would mask that, so the delivery must fail, stay
// retryable, and leave nothing in the receiver thread.
func TestDeliverCodexMessageUnknownSteerRejectionFailsWithoutQueue(t *testing.T) {
	for _, tc := range []struct{ name, rejection string }{
		{"standard method not found", `{"code":-32601,"message":"Method not found"}`},
		{"0.150 unknown variant", "{\"code\":-32600,\"message\":\"Invalid request: unknown variant `turn/steer`, expected one of `initialize`, `thread/start`\"}"},
		{"request shape drift", "{\"code\":-32600,\"message\":\"Invalid request: missing field `input`\"}"},
		{"sub-agent ownership", `{"code":-32600,"message":"direct app-server input is not allowed for multi-agent v2 sub-agents"}`},
		{"empty input", `{"code":-32600,"message":"input must not be empty"}`},
		{"internal error", `{"code":-32603,"message":"failed to steer turn: boom"}`},
		{"documented text under a foreign code", `{"code":-32603,"message":"no active turn to steer"}`},
		{"documented marker under a foreign code", `{"code":-32603,"message":"steer failed","data":{"codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"review"}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile, _ := installFakeCodexAppServer(t)
			t.Setenv("PAIMOS_TEST_THREAD_TURNS", `[{"id":"turn-live","status":"inProgress"}]`)
			t.Setenv("PAIMOS_TEST_STEER", `{"error":`+tc.rejection+`}`)
			outcome, err := deliverCodexMessage(context.Background(), steerMessage("thread-broken"), "broken payload", "ignored", "queue")
			if err == nil {
				t.Fatalf("unexpected success outcome=%#v", outcome)
			}
			if !strings.Contains(err.Error(), "unexpected app-server rejection") || !strings.Contains(err.Error(), "Codex app-server error") || !strings.Contains(err.Error(), "expected turn turn-live") {
				t.Fatalf("error=%q must name the unexpected rejection and the expected turn", err)
			}
			var unavailable *adapterUnavailableError
			if errors.As(err, &unavailable) {
				t.Fatalf("a broken steer primitive must stay a retryable delivery error, got adapter unavailable: %v", err)
			}
			args, _ := os.ReadFile(argsFile)
			if strings.Contains(string(args), "queue\n") {
				t.Fatalf("unknown rejection must not fall back to codex queue: %q", args)
			}
		})
	}
}

// runListen must not complete the delivery row when the steer primitive is
// broken: the row stays open for a retry and the receiver cursor does not
// advance past the undelivered message.
func TestRunListenUnknownSteerRejectionKeepsDeliveryOpen(t *testing.T) {
	argsFile, _ := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_THREAD_TURNS", `[{"id":"turn-live","status":"inProgress"}]`)
	t.Setenv("PAIMOS_TEST_STEER", `{"error":{"code":-32601,"message":"Method not found"}}`)
	message := steerMessage("thread-broken")
	message.Cursor, message.MessageID = 7, "m-broken"
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "broken payload"})
	completeCalls, ackCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/listen"):
			_ = json.NewEncoder(w).Encode(inboxPage{NextCursor: 7, Messages: []messageEnvelope{message}})
		case strings.HasSuffix(r.URL.Path, "/delivery-complete"):
			completeCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/ack"):
			ackCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": 7})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	err := runListen(context.Background(), client, 1, "codex:codex", "codex", false, true, "codex", "", "queue", false, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "unexpected app-server rejection") {
		t.Fatalf("error=%v want the unexpected rejection surfaced from listen", err)
	}
	if completeCalls != 0 || ackCalls != 0 {
		t.Fatalf("delivery-complete=%d ack=%d want 0/0 for a failed steer", completeCalls, ackCalls)
	}
	args, _ := os.ReadFile(argsFile)
	if strings.Contains(string(args), "queue\n") {
		t.Fatalf("listen must not queue after an unknown steer rejection: %q", args)
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

func TestDeliverCodexSteerSilentProxyFallsBackPreciselyWithinBudget(t *testing.T) {
	argsFile, _ := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_SCENARIO", "hang")
	previous := harnessplugin.CodexSteerTimeout
	harnessplugin.CodexSteerTimeout = 2 * time.Second
	t.Cleanup(func() { harnessplugin.CodexSteerTimeout = previous })
	started := time.Now()
	outcome, err := deliverCodexMessage(context.Background(), steerMessage("thread-hang"), "hang payload", "ignored", "queue")
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != "transport_error" {
		t.Fatalf("outcome=%#v on a silent proxy", outcome)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "queue\n--thread\nthread-hang\n--message\nhang payload\n") {
		t.Fatalf("exact queue argv missing after proxy timeout: %q", args)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("silent proxy took %s; the budget plus reap delay must bound it", elapsed)
	}
}

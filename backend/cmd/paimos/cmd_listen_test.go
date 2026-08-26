// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunListenNoMessagesUsesExitThree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(agentAttrHeader); got != "codex" {
			t.Fatalf("agent header=%q", got)
		}
		_ = json.NewEncoder(w).Encode(inboxPage{Address: "codex:codex"})
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	err := runListen(context.Background(), client, 1, "codex:codex", "codex", false, false, "", "", "queue", false, time.Millisecond)
	exit, ok := err.(*listenExitCode)
	if !ok || exit.code != listenExitNoMessages {
		t.Fatalf("error=%#v want listen exit %d", err, listenExitNoMessages)
	}
}

func TestRunListenAdapterUnavailableUsesExitFour(t *testing.T) {
	message := messageEnvelope{Cursor: 1, MessageID: "m1"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "payload"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(inboxPage{NextCursor: 1, Messages: []messageEnvelope{message}})
	}))
	defer srv.Close()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	err := runListen(context.Background(), client, 1, "codex:codex", "codex", false, true, "codex", "", "queue", false, time.Millisecond)
	exit, ok := err.(*listenExitCode)
	if !ok || exit.code != listenExitAdapterUnavailable {
		t.Fatalf("error=%#v want listen exit %d", err, listenExitAdapterUnavailable)
	}
}

func TestRunListenAcknowledgesOnlyAfterOutput(t *testing.T) {
	var mu sync.Mutex
	acked := int64(0)
	message := messageEnvelope{Cursor: 9, MessageID: "m1", From: "paimos:claude", To: "codex:codex", ThreadID: "t1"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "untrusted payload"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects/1/messages/listen":
			_ = json.NewEncoder(w).Encode(inboxPage{
				Address: "codex:codex", NextCursor: 9, Messages: []messageEnvelope{message},
			})
		case "/api/projects/1/messages/ack":
			var body struct {
				Cursor int64 `json:"cursor"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			acked = body.Cursor
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": body.Cursor})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	oldStdout := stdout
	var out bytes.Buffer
	stdout = &out
	defer func() { stdout = oldStdout }()
	if err := runListen(context.Background(), client, 1, "codex:codex", "codex", false, true, "", "", "queue", false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "untrusted payload") {
		t.Fatalf("output=%q", out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if acked != 9 {
		t.Fatalf("acked=%d want 9", acked)
	}
}

type failingListenWriter struct{}

func (failingListenWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestRunListenDoesNotAcknowledgeFailedOutput(t *testing.T) {
	message := messageEnvelope{Cursor: 4, MessageID: "m4", From: "paimos:sender", To: "codex:receiver"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "payload"})
	ackCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/listen") {
			_ = json.NewEncoder(w).Encode(inboxPage{NextCursor: 4, Messages: []messageEnvelope{message}})
			return
		}
		ackCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"cursor": 4})
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	oldStdout := stdout
	stdout = failingListenWriter{}
	defer func() { stdout = oldStdout }()
	if err := runListen(context.Background(), client, 1, "codex:receiver", "receiver", false, true, "", "", "queue", false, time.Millisecond); err == nil {
		t.Fatal("expected output failure")
	}
	if ackCalls != 0 {
		t.Fatalf("ack calls=%d want 0", ackCalls)
	}
}

func TestDeliverCodexUsesQueueArgv(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PAIMOS_TEST_ARGS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_ARGS", argsFile)
	if err := deliverCodex(context.Background(), "hello from ledger", "thread-7"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), "queue\n--thread\nthread-7\n--message\nhello from ledger\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
}

func installFakeCodexAppServer(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	framesFile := filepath.Join(dir, "frames")
	script := filepath.Join(dir, "codex")
	body := `#!/bin/sh
printf '%s\n' "$@" >> "$PAIMOS_TEST_ARGS"
if [ "$1" = "queue" ]; then exit 0; fi
if [ "$1" = "app-server" ] && [ "$2" = "daemon" ]; then exit 0; fi
if [ "$1" = "app-server" ] && [ "$2" = "proxy" ]; then
  while IFS= read -r line; do
    printf '%s\n' "$line" >> "$PAIMOS_TEST_FRAMES"
    case "$line" in
      *'"id":1'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"codex","version":"test"}}}' ;;
      *'"id":2'*) printf '%s\n' "$PAIMOS_TEST_TURNS" ;;
      *'"id":3'*) printf '%s\n' "$PAIMOS_TEST_STEER" ;;
    esac
  done
fi
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_ARGS", argsFile)
	t.Setenv("PAIMOS_TEST_FRAMES", framesFile)
	return argsFile, framesFile
}

func TestDeliverCodexMessageSteersInProgressTurn(t *testing.T) {
	argsFile, framesFile := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_TURNS", `{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"turn-live","status":"inProgress"}]}}`)
	t.Setenv("PAIMOS_TEST_STEER", `{"jsonrpc":"2.0","id":3,"result":{"turnId":"turn-live"}}`)
	message := messageEnvelope{DeliveryLevel: "steer", DeliveryWork: &messageDeliveryWork{
		DeliveryID: "delivery-1", Adapter: "codex", TargetRef: "thread-live", MaximumLevel: "steer", RequestedLevel: "steer",
	}}
	outcome, err := deliverCodexMessage(context.Background(), message, "steer payload", "ignored-process-target", "queue")
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
	frames, _ := os.ReadFile(framesFile)
	for _, required := range []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"thread/turns/list"`, `"method":"turn/steer"`, `"expectedTurnId":"turn-live"`, `"type":"text"`, `"text":"steer payload"`} {
		if !strings.Contains(string(frames), required) {
			t.Fatalf("frames missing %s: %s", required, frames)
		}
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
	argsFile, _ := installFakeCodexAppServer(t)
	t.Setenv("PAIMOS_TEST_TURNS", `{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"turn-old","status":"completed"}]}}`)
	message := messageEnvelope{DeliveryLevel: "steer", DeliveryWork: &messageDeliveryWork{
		DeliveryID: "delivery-2", Adapter: "codex", TargetRef: "thread-idle", MaximumLevel: "steer", RequestedLevel: "steer",
	}}
	outcome, err := deliverCodexMessage(context.Background(), message, "idle payload", "ignored", "queue")
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
}

func TestDeliverCodexMessageFallsBackOnNotSteerableOrTurnRace(t *testing.T) {
	for _, errorData := range []string{
		`{"code":-32000,"message":"active turn cannot accept steer","data":{"activeTurnNotSteerable":{"kind":"review"}}}`,
		`{"code":-32001,"message":"expected turn does not match"}`,
	} {
		t.Run(errorData, func(t *testing.T) {
			_, _ = installFakeCodexAppServer(t)
			t.Setenv("PAIMOS_TEST_TURNS", `{"jsonrpc":"2.0","id":2,"result":{"data":[{"id":"turn-raced","status":"inProgress"}]}}`)
			t.Setenv("PAIMOS_TEST_STEER", `{"jsonrpc":"2.0","id":3,"error":`+errorData+`}`)
			message := messageEnvelope{DeliveryLevel: "steer", DeliveryWork: &messageDeliveryWork{
				DeliveryID: "delivery-3", Adapter: "codex", TargetRef: "thread-raced", MaximumLevel: "steer", RequestedLevel: "steer",
			}}
			outcome, err := deliverCodexMessage(context.Background(), message, "raced payload", "ignored", "queue")
			if err != nil {
				t.Fatal(err)
			}
			if outcome.EffectiveLevel != "simple" || outcome.FallbackReason != "not_steerable" {
				t.Fatalf("outcome=%#v", outcome)
			}
		})
	}
}

func TestDeliverCodexDefaultsToQueueWhenModeEmpty(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PAIMOS_TEST_ARGS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_ARGS", argsFile)
	if err := deliverCodex(context.Background(), "default mode test", "thread-9"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), "queue\n--thread\nthread-9\n--message\ndefault mode test\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
}

func TestDeliverCodexQueueRequiresTarget(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	err := deliverCodex(context.Background(), "no target", "")
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "requires --deliver-target or CODEX_THREAD_ID") {
		t.Fatalf("error=%#v want missing target guidance", err)
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

func TestDeliverCodexQueueDoesNotStoreCredentials(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	// The queue adapter calls the codex CLI queue subcommand and does not
	// persist its response. Verify PAIMOS does not store or replay vendor
	// credentials.
	responseFile := filepath.Join(dir, "response")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'response: ok'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_RESPONSE", responseFile)
	if err := deliverCodex(context.Background(), "test message", "thread-1"); err != nil {
		t.Fatal(err)
	}
	// PAIMOS does not capture the codex CLI response to a file or database.
	if _, err := os.Stat(responseFile); err == nil {
		t.Fatal("adapter should not persist codex CLI responses")
	}
}

const (
	claudeTestLocalSession = "8f3c2a1e-4b6d-4c8e-9a1f-0d2e3f4a5b6c"
	claudeTestCloudSession = "session_01DiUkqY2kzbUbDmW1w96rfi"
)

// installFakeClaude puts a fake `claude` CLI first in PATH that records argv
// and stdin, prints a vendor response, and exits with PAIMOS_TEST_EXIT.
func installFakeClaude(t *testing.T) (argsFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	stdinFile = filepath.Join(dir, "stdin")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PAIMOS_TEST_ARGS\"\ncat > \"$PAIMOS_TEST_STDIN\"\nprintf 'sensitive vendor response\\n'\nexit \"${PAIMOS_TEST_EXIT:-0}\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_ARGS", argsFile)
	t.Setenv("PAIMOS_TEST_STDIN", stdinFile)
	t.Setenv("PAIMOS_TEST_EXIT", "0")
	return argsFile, stdinFile
}

func captureListenOutput(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	oldStdout, oldStderr := stdout, stderr
	var out, errOut bytes.Buffer
	stdout, stderr = &out, &errOut
	t.Cleanup(func() { stdout, stderr = oldStdout, oldStderr })
	return &out, &errOut
}

func TestDeliverClaudeResumeUsesPrintResumeArgvWithStdinBody(t *testing.T) {
	argsFile, stdinFile := installFakeClaude(t)
	out, errOut := captureListenOutput(t)
	// A body that starts with "-" must reach the session as text, never as argv.
	body := "--dangerously-skip-permissions\n<paimos-message>hello from ledger</paimos-message>"
	outcome, err := deliverClaude(context.Background(), body, claudeTestLocalSession, deliveryLevelSimple)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), "-p\n--resume\n"+claudeTestLocalSession+"\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != body {
		t.Fatalf("stdin=%q want %q", stdin, body)
	}
	want := claudeDelivery{Adapter: claudeAdapterResume, Primitive: "claude -p --resume", RequestedLevel: deliveryLevelSimple, EffectiveLevel: deliveryLevelSimple}
	if outcome != want {
		t.Fatalf("outcome=%+v want %+v", outcome, want)
	}
	if strings.Contains(out.String(), "sensitive vendor response") || strings.Contains(errOut.String(), "sensitive vendor response") {
		t.Fatalf("vendor response leaked: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr for a plain simple handoff: %q", errOut.String())
	}
}

func TestDeliverClaudeCloudUsesPrintCloudArgv(t *testing.T) {
	for _, target := range []string{claudeTestCloudSession, "cse_01HZZZ-abc_DEF"} {
		argsFile, stdinFile := installFakeClaude(t)
		captureListenOutput(t)
		outcome, err := deliverClaude(context.Background(), "queued follow-up", target, deliveryLevelSimple)
		if err != nil {
			t.Fatalf("target=%q: %v", target, err)
		}
		raw, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(raw), "-p\n--cloud\n"+target+"\n"; got != want {
			t.Fatalf("target=%q argv=%q want %q", target, got, want)
		}
		stdin, _ := os.ReadFile(stdinFile)
		if string(stdin) != "queued follow-up" {
			t.Fatalf("stdin=%q", stdin)
		}
		if outcome.Primitive != "claude -p --cloud" || outcome.EffectiveLevel != deliveryLevelSimple || outcome.FallbackReason != "" {
			t.Fatalf("outcome=%+v", outcome)
		}
	}
}

func TestDeliverClaudeSteerFallsBackToSimple(t *testing.T) {
	argsFile, _ := installFakeClaude(t)
	_, errOut := captureListenOutput(t)
	outcome, err := deliverClaude(context.Background(), "interrupt please", claudeTestLocalSession, deliveryLevelSteer)
	if err != nil {
		t.Fatal(err)
	}
	// Steer is UNSUPPORTED for Claude: the exact simple primitive runs and the
	// downgrade is recorded, never a guessed steer command or socket frame.
	raw, _ := os.ReadFile(argsFile)
	if got, want := string(raw), "-p\n--resume\n"+claudeTestLocalSession+"\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
	want := claudeDelivery{Adapter: claudeAdapterResume, Primitive: "claude -p --resume", RequestedLevel: deliveryLevelSteer, EffectiveLevel: deliveryLevelSimple, FallbackReason: claudeFallbackUnsupported}
	if outcome != want {
		t.Fatalf("outcome=%+v want %+v", outcome, want)
	}
	if !strings.Contains(errOut.String(), "steer is unsupported") || !strings.Contains(errOut.String(), "fallback_reason=unsupported") {
		t.Fatalf("stderr=%q want fallback note", errOut.String())
	}
}

func TestDeliverClaudeRequiresExplicitSessionTarget(t *testing.T) {
	argsFile, _ := installFakeClaude(t)
	// The dead socket lookup is gone: a socket in the environment is not a target.
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	_, err := deliverClaude(context.Background(), "payload", "", deliveryLevelSimple)
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "requires --deliver-target") {
		t.Fatalf("error=%#v want missing target guidance", err)
	}
	if _, statErr := os.Stat(argsFile); statErr == nil {
		t.Fatal("claude must not be invoked without a session target")
	}
}

func TestDeliverClaudeRejectsNonSessionTargets(t *testing.T) {
	argsFile, _ := installFakeClaude(t)
	for _, target := range []string{
		"latest",
		"my-session-name",
		"/tmp/claude.sock",
		"unix:///tmp/claude.sock",
		"https://claude.ai/code/" + claudeTestCloudSession,
		strings.ToUpper(claudeTestLocalSession),
		"session_",
		"cse-01abc",
	} {
		_, err := deliverClaude(context.Background(), "payload", target, deliveryLevelSimple)
		var unavailable *adapterUnavailableError
		if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "session id") {
			t.Fatalf("target=%q error=%#v want session target guidance", target, err)
		}
	}
	if _, statErr := os.Stat(argsFile); statErr == nil {
		t.Fatal("claude must not be invoked for a rejected target")
	}
}

func TestDeliverClaudeRequiresClaudeCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := deliverClaude(context.Background(), "payload", claudeTestLocalSession, deliveryLevelSimple)
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "claude CLI in PATH") {
		t.Fatalf("error=%#v want native CLI guidance", err)
	}
}

func TestDeliverClaudeNonZeroExitIsNotHandoff(t *testing.T) {
	installFakeClaude(t)
	captureListenOutput(t)
	t.Setenv("PAIMOS_TEST_EXIT", "1")
	_, err := deliverClaude(context.Background(), "payload", claudeTestLocalSession, deliveryLevelSimple)
	if err == nil {
		t.Fatal("expected vendor failure")
	}
	var unavailable *adapterUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("a failed run is a retryable error, not adapter unavailability: %v", err)
	}
}

func claudeListenServer(t *testing.T, message messageEnvelope) (*httptest.Server, *int64, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	acked := int64(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(agentAttrHeader) != "claude" {
			t.Errorf("agent header=%q want claude", r.Header.Get(agentAttrHeader))
		}
		switch r.URL.Path {
		case "/api/projects/1/messages/listen":
			_ = json.NewEncoder(w).Encode(inboxPage{Address: "claude:claude", NextCursor: message.Cursor, Messages: []messageEnvelope{message}})
		case "/api/projects/1/messages/ack":
			var body struct {
				Cursor int64 `json:"cursor"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			acked = body.Cursor
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": body.Cursor})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &acked, &mu
}

func TestRunListenDeliversClaudeThenAcknowledges(t *testing.T) {
	message := messageEnvelope{Cursor: 21, MessageID: "m21", From: "paimos:codex", To: "claude:claude", ThreadID: "t21"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "framed untrusted payload"})
	srv, acked, mu := claudeListenServer(t, message)
	_, stdinFile := installFakeClaude(t)
	captureListenOutput(t)
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := runListen(context.Background(), client, 1, "claude:claude", "claude", false, true, "claude", claudeTestLocalSession, "queue", false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != "framed untrusted payload" {
		t.Fatalf("stdin=%q", stdin)
	}
	mu.Lock()
	defer mu.Unlock()
	if *acked != 21 {
		t.Fatalf("acked=%d want 21", *acked)
	}
}

func TestRunListenDoesNotAcknowledgeFailedClaudeHandoff(t *testing.T) {
	message := messageEnvelope{Cursor: 22, MessageID: "m22", From: "paimos:codex", To: "claude:claude"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "payload"})
	srv, acked, mu := claudeListenServer(t, message)
	installFakeClaude(t)
	captureListenOutput(t)
	t.Setenv("PAIMOS_TEST_EXIT", "2")
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	err := runListen(context.Background(), client, 1, "claude:claude", "claude", false, true, "claude", claudeTestLocalSession, "queue", false, time.Millisecond)
	if err == nil {
		t.Fatal("expected failed handoff to surface")
	}
	if exit, ok := err.(*listenExitCode); ok {
		t.Fatalf("vendor failure must not be reported as listen exit %d", exit.code)
	}
	mu.Lock()
	defer mu.Unlock()
	if *acked != 0 {
		t.Fatalf("acked=%d want 0 (cursor must not advance before handoff)", *acked)
	}
}

func TestRunListenClaudeSteerModeFallsBackToSimple(t *testing.T) {
	message := messageEnvelope{Cursor: 23, MessageID: "m23", From: "paimos:codex", To: "claude:claude"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "steer me"})
	srv, acked, mu := claudeListenServer(t, message)
	argsFile, _ := installFakeClaude(t)
	_, errOut := captureListenOutput(t)
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	// Before PAI-827 this returned listen exit 4; now it is a simple handoff.
	if err := runListen(context.Background(), client, 1, "claude:claude", "claude", false, true, "claude", claudeTestCloudSession, "steer", false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsFile)
	if got, want := string(raw), "-p\n--cloud\n"+claudeTestCloudSession+"\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
	if !strings.Contains(errOut.String(), "fallback_reason=unsupported") {
		t.Fatalf("stderr=%q want fallback note", errOut.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if *acked != 23 {
		t.Fatalf("acked=%d want 23", *acked)
	}
}

func TestDeliverClaudeMessageUsesDurableLevel(t *testing.T) {
	for _, tc := range []struct {
		name, level, legacyMode, wantFallback string
	}{
		{name: "bus steer level falls back to simple", level: "steer", legacyMode: "queue", wantFallback: claudeFallbackUnsupported},
		{name: "bus simple level ignores legacy steer mode", level: "simple", legacyMode: "steer", wantFallback: ""},
		{name: "pre-bus row uses legacy steer mode", level: "", legacyMode: "steer", wantFallback: claudeFallbackUnsupported},
		{name: "pre-bus row uses legacy queue mode", level: "", legacyMode: "queue", wantFallback: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile, stdinFile := installFakeClaude(t)
			captureListenOutput(t)
			message := messageEnvelope{Cursor: 31, MessageID: "m31", DeliveryLevel: tc.level}
			outcome, err := deliverClaudeMessage(context.Background(), message, "durable payload", claudeTestLocalSession, tc.legacyMode)
			if err != nil {
				t.Fatal(err)
			}
			// Every request runs the exact simple primitive; only the recorded
			// fallback differs.
			raw, _ := os.ReadFile(argsFile)
			if got, want := string(raw), "-p\n--resume\n"+claudeTestLocalSession+"\n"; got != want {
				t.Fatalf("argv=%q want %q", got, want)
			}
			stdin, _ := os.ReadFile(stdinFile)
			if string(stdin) != "durable payload" {
				t.Fatalf("stdin=%q", stdin)
			}
			want := &deliveryOutcome{EffectiveLevel: deliveryLevelSimple, FallbackReason: tc.wantFallback}
			if *outcome != *want {
				t.Fatalf("outcome=%+v want %+v", *outcome, *want)
			}
		})
	}
}

func TestRunListenClaudeDurableSteerLevelFallsBackToSimple(t *testing.T) {
	message := messageEnvelope{Cursor: 24, MessageID: "m24", From: "paimos:codex", To: "claude:claude", DeliveryLevel: "steer"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "durable steer"})
	srv, acked, mu := claudeListenServer(t, message)
	argsFile, _ := installFakeClaude(t)
	_, errOut := captureListenOutput(t)
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	// The bus level wins over the legacy process mode: a durable steer request
	// with --deliver-mode queue is still an unsupported-steer simple handoff.
	if err := runListen(context.Background(), client, 1, "claude:claude", "claude", false, true, "claude", claudeTestLocalSession, "queue", false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsFile)
	if got, want := string(raw), "-p\n--resume\n"+claudeTestLocalSession+"\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
	if !strings.Contains(errOut.String(), "fallback_reason=unsupported") {
		t.Fatalf("stderr=%q want fallback note", errOut.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if *acked != 24 {
		t.Fatalf("acked=%d want 24 (no delivery_work: plain cursor ack)", *acked)
	}
}

func TestClaudeDeliveryLevelHelpers(t *testing.T) {
	for in, want := range map[string]string{"": deliveryLevelSimple, "simple": deliveryLevelSimple, "queue": deliveryLevelSimple, "STEER": deliveryLevelSteer, " steer ": deliveryLevelSteer} {
		if got := normalizeDeliveryLevel(in); got != want {
			t.Fatalf("normalizeDeliveryLevel(%q)=%q want %q", in, got, want)
		}
	}
	if got := deliveryLevelFromMode("queue"); got != deliveryLevelSimple {
		t.Fatalf("queue mode=%q", got)
	}
	if got := deliveryLevelFromMode("steer"); got != deliveryLevelSteer {
		t.Fatalf("steer mode=%q", got)
	}
	channel := claudeSimpleDelivery(claudeAdapterChannel, "notifications/claude/channel", deliveryLevelSteer)
	if channel.EffectiveLevel != deliveryLevelSimple || channel.FallbackReason != claudeFallbackUnsupported || channel.RequestedLevel != deliveryLevelSteer {
		t.Fatalf("channel steer outcome=%+v", channel)
	}
}

func TestDeliverGrokRequiresExplicitGate(t *testing.T) {
	err := deliverGrok(context.Background(), "payload", "01991b7e-1847-7e18-bc1c-b28e8cfaad4a", false)
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "--enable-grok-build-delivery") {
		t.Fatalf("error=%#v want gate guidance", err)
	}
}

func TestDeliverGrokRequiresCanonicalSessionUUID(t *testing.T) {
	t.Setenv("GROK_SESSION_ID", "")
	for _, target := range []string{"", "latest", "01991B7E-1847-7E18-BC1C-B28E8CFAAD4A"} {
		err := deliverGrok(context.Background(), "payload", target, true)
		var unavailable *adapterUnavailableError
		if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "canonical lowercase session UUID") {
			t.Fatalf("target=%q error=%#v want UUID guidance", target, err)
		}
	}
}

func TestDeliverGrokRequiresNativeCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := deliverGrok(context.Background(), "payload", "01991b7e-1847-7e18-bc1c-b28e8cfaad4a", true)
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "grok CLI in PATH") {
		t.Fatalf("error=%#v want native CLI guidance", err)
	}
}

func TestDeliverGrokUsesBoundedNativeArgvAndDiscardsResponse(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "grok")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PAIMOS_TEST_ARGS\"\nprintf 'sensitive vendor response\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_ARGS", argsFile)
	oldStdout := stdout
	var out bytes.Buffer
	stdout = &out
	defer func() { stdout = oldStdout }()
	target := "01991b7e-1847-7e18-bc1c-b28e8cfaad4a"
	if err := deliverGrok(context.Background(), "hello from ledger", target, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "--single\nhello from ledger\n--resume\n" + target + "\n" +
		"--output-format\njson\n--permission-mode\ndontAsk\n--tools\n\n" +
		"--no-plan\n--no-subagents\n--disable-web-search\n--max-turns\n1\n--verbatim\n"
	if got := string(raw); got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
	if strings.Contains(out.String(), "sensitive vendor response") {
		t.Fatalf("vendor response leaked to stdout: %q", out.String())
	}
}

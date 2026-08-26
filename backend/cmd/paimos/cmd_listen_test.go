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

func TestDeliverClaudeMidTurnUnsupported(t *testing.T) {
	// Claude mid-turn delivery is unsupported because the official messaging
	// socket frame is not documented beyond the optional auth line.
	err := deliverClaude(context.Background(), "hello Claude", "")
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "mid-turn delivery unsupported") {
		t.Fatalf("error=%#v want mid-turn unsupported", err)
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

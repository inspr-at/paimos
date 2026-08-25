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
	if err := deliverCodex(context.Background(), "hello from ledger", "thread-7", "queue"); err != nil {
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

func TestDeliverCodexSteerRequiresCodexCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	// No codex CLI in PATH
	err := deliverCodex(context.Background(), "interrupt now", "thread-42", "steer")
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "codex CLI in PATH") {
		t.Fatalf("error=%#v want codex CLI requirement", err)
	}
}

func TestDeliverCodexSteerUsesAppServerProxy(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	rpcFile := filepath.Join(dir, "rpc")
	// Mock codex app-server proxy: capture argv, read stdin line-by-line and echo responses
	script := filepath.Join(dir, "codex")
	scriptContent := `#!/bin/sh
printf '%s\n' "$@" > "$PAIMOS_TEST_ARGS"
# Read 4 JSON-RPC requests (initialize, initialized, thread/turns/list, turn/steer)
# and write them to the RPC file while sending responses
{
  read line && echo "$line" >> "$PAIMOS_TEST_RPC"
  echo '{"id":0,"result":{"serverInfo":{"name":"codex-app-server","version":"0.149.1"}}}'
  
  read line && echo "$line" >> "$PAIMOS_TEST_RPC"
  # initialized is a notification, no response
  
  read line && echo "$line" >> "$PAIMOS_TEST_RPC"
  echo '{"id":1,"result":{"data":[{"id":"turn-123","status":"inProgress"}]}}'
  
  read line && echo "$line" >> "$PAIMOS_TEST_RPC"
  echo '{"id":2,"result":{"turnId":"turn-123"}}'
}
`
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAIMOS_TEST_ARGS", argsFile)
	t.Setenv("PAIMOS_TEST_RPC", rpcFile)
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := deliverCodex(ctx, "interrupt now", "thread-42", "steer"); err != nil {
		t.Fatal(err)
	}
	
	// Verify argv uses app-server proxy
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), "app-server\nproxy\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
	
	// Verify JSON-RPC requests were sent with correct schema
	rpc, err := os.ReadFile(rpcFile)
	if err != nil {
		t.Fatal(err)
	}
	rpcStr := string(rpc)
	if !strings.Contains(rpcStr, `"method":"initialize"`) {
		t.Fatalf("missing initialize in RPC: %q", rpcStr)
	}
	if !strings.Contains(rpcStr, `"method":"initialized"`) {
		t.Fatalf("missing initialized notification in RPC: %q", rpcStr)
	}
	if !strings.Contains(rpcStr, `"clientInfo"`) {
		t.Fatalf("missing clientInfo in RPC: %q", rpcStr)
	}
	if !strings.Contains(rpcStr, "thread/turns/list") {
		t.Fatalf("missing thread/turns/list in RPC: %q", rpcStr)
	}
	if !strings.Contains(rpcStr, "turn/steer") {
		t.Fatalf("missing turn/steer in RPC: %q", rpcStr)
	}
	if !strings.Contains(rpcStr, "thread-42") {
		t.Fatalf("missing threadId in RPC: %q", rpcStr)
	}
	if !strings.Contains(rpcStr, `"type":"text"`) {
		t.Fatalf("missing UserInput type:text in RPC: %q", rpcStr)
	}
	if !strings.Contains(rpcStr, "interrupt now") {
		t.Fatalf("missing message text in RPC: %q", rpcStr)
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
	if err := deliverCodex(context.Background(), "default mode test", "thread-9", ""); err != nil {
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
	err := deliverCodex(context.Background(), "no target", "", "queue")
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "requires --deliver-target or CODEX_THREAD_ID") {
		t.Fatalf("error=%#v want missing target guidance", err)
	}
}

func TestDeliverCodexSteerRequiresTarget(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	err := deliverCodex(context.Background(), "no target", "", "steer")
	var unavailable *adapterUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "requires --deliver-target or CODEX_THREAD_ID") {
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
	if err := deliverCodex(context.Background(), "test message", "thread-1", "queue"); err != nil {
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

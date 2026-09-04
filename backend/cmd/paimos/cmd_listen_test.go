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
	"strconv"
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

func TestRunAttentionListenAcknowledgesOnlyAfterBoundedOutput(t *testing.T) {
	page := attentionPage{Address: "codex:amy", NextCursor: 12, Items: []attentionItem{{
		Cursor: 12, AttentionID: "attention-12", SourceProjectID: 7, SourceKind: "harness_session_event",
		SourceID: "22222222-2222-4222-8222-222222222222", SourceSequence: 4,
		Kind: "worker_unknown", Reason: "heartbeat_stale", OccurredAt: "2026-09-03T01:02:03.000Z",
	}}}
	var ack struct {
		To      string `json:"to"`
		Cursor  int64  `json:"cursor"`
		BatchID string `json:"batch_id"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects/1/attention/listen":
			if got := r.Header.Get(agentAttrHeader); got != "amy" {
				t.Errorf("agent header=%q", got)
			}
			_ = json.NewEncoder(w).Encode(page)
		case "/api/projects/1/attention/ack":
			_ = json.NewDecoder(r.Body).Decode(&ack)
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": ack.Cursor})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldStdout := stdout
	var out bytes.Buffer
	stdout = &out
	defer func() { stdout = oldStdout }()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := runAttentionListen(context.Background(), client, 1, "codex:amy", "amy", false, true, "", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "kind=worker_unknown") || strings.Contains(out.String(), "body=") {
		t.Fatalf("output=%q", out.String())
	}
	if ack.To != "codex:amy" || ack.Cursor != 12 || ack.BatchID != "" {
		t.Fatalf("ack=%+v", ack)
	}
}

func TestRunAttentionListenDoesNotAckFailedOutput(t *testing.T) {
	page := attentionPage{Address: "codex:amy", NextCursor: 12, Items: []attentionItem{{
		Cursor: 12, AttentionID: "attention-12", SourceProjectID: 7, SourceKind: "harness_session_event",
		SourceID: "worker", SourceSequence: 4, Kind: "worker_dead", Reason: "process_failed", OccurredAt: "2026-09-03T01:02:03.000Z",
	}}}
	acks := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/listen") {
			_ = json.NewEncoder(w).Encode(page)
			return
		}
		acks++
		_ = json.NewEncoder(w).Encode(map[string]any{"cursor": 12})
	}))
	defer srv.Close()
	oldStdout := stdout
	stdout = failingListenWriter{}
	defer func() { stdout = oldStdout }()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := runAttentionListen(context.Background(), client, 1, "codex:amy", "amy", false, true, "", time.Millisecond); err == nil {
		t.Fatal("expected output failure")
	}
	if acks != 0 {
		t.Fatalf("acks=%d", acks)
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

func TestRunAttentionListenAdapterUnavailableUsesExitFour(t *testing.T) {
	page := attentionPage{Address: "codex:amy", NextCursor: 12, Items: []attentionItem{{
		Cursor: 12, AttentionID: "attention-12", SourceProjectID: 7, SourceKind: "harness_session_event",
		SourceID: "worker", SourceSequence: 4, Kind: "worker_dead", Reason: "process_failed", OccurredAt: "2026-09-03T01:02:03.000Z",
	}}, Work: &attentionWork{
		BatchID: "11111111-1111-4111-8111-111111111111", Instance: "ppm", ProjectID: 1, State: "leased",
		Adapter: "codex", TargetKind: "codex_thread", TargetRef: "22222222-2222-4222-8222-222222222222", MaximumLevel: "simple",
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	err := runAttentionListen(context.Background(), client, 1, "codex:amy", "amy", false, true, "codex", time.Millisecond)
	exit, ok := err.(*listenExitCode)
	if !ok || exit.code != listenExitAdapterUnavailable {
		t.Fatalf("error=%#v want listen exit %d", err, listenExitAdapterUnavailable)
	}
}

func TestRunAttentionListenFollowKeepsBlockedBatchAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	page := attentionPage{Address: "codex:amy", NextCursor: 12, Items: []attentionItem{{
		Cursor: 12, AttentionID: "attention-12", SourceProjectID: 7, SourceKind: "harness_session_event",
		SourceID: "worker", SourceSequence: 4, Kind: "worker_dead", Reason: "process_failed", OccurredAt: "2026-09-03T01:02:03.000Z",
	}}, Work: &attentionWork{
		BatchID: "11111111-1111-4111-8111-111111111111", Instance: "ppm", ProjectID: 1,
		State: "blocked", BlockedReason: "capability_missing",
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(page)
		if requests == 2 {
			cancel()
		}
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := runAttentionListen(ctx, client, 1, "codex:amy", "amy", true, true, "codex", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if requests < 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestRunAttentionListenFollowRetriesRequeueRequired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "operator requeue required", "code": "agent_attention_batch_requeue_required",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(attentionPage{Address: "codex:amy"})
		cancel()
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := runAttentionListen(ctx, client, 1, "codex:amy", "amy", true, true, "codex", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestRunListenForeignWorkerReturnsDistinctExitWithoutCompleting(t *testing.T) {
	message := messageEnvelope{Cursor: 2, MessageID: "m-foreign", DeliveryWork: &messageDeliveryWork{
		DeliveryID: "d-foreign", State: "pending", Adapter: "managed_harness", TargetKind: "harness_session", RequestedLevel: "steer",
	}}
	completes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects/1/messages/listen":
			_ = json.NewEncoder(w).Encode(inboxPage{NextCursor: 2, Messages: []messageEnvelope{message}})
		case "/api/projects/1/messages/delivery-complete":
			completes++
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	err := runListen(context.Background(), client, 1, "codex:worker", "worker", false, true, "agentd_codex", "", "queue", false, time.Millisecond)
	exit, ok := err.(*listenExitCode)
	if !ok || exit.code != listenExitForeignWorker {
		t.Fatalf("error=%#v want foreign-worker exit %d", err, listenExitForeignWorker)
	}
	if completes != 0 {
		t.Fatalf("foreign work completed %d times", completes)
	}
}

func TestDeliveryFallbackEvidenceDoesNotOverwriteAdapterReason(t *testing.T) {
	if got := chooseDeliveryFallbackReason("transport_error", "idle"); got != "transport_error" {
		t.Fatalf("adapter evidence overwritten: %q", got)
	}
	if got := chooseDeliveryFallbackReason("", "idle"); got != "idle" {
		t.Fatalf("empty adapter evidence did not inherit durable row reason: %q", got)
	}
}

func TestRunListenUnavailableAgentdReroutesLeaseWithoutCompletingIt(t *testing.T) {
	message := messageEnvelope{Cursor: 17, MessageID: "m-agentd", From: "paimos:sender", To: "codex:worker", DeliveryLevel: "steer",
		DeliveryWork: &messageDeliveryWork{DeliveryID: "11111111-1111-4111-8111-111111111111", Instance: "ppm-test", ProjectID: 1,
			State: "leased", Adapter: "agentd_codex",
			TargetKind: "agentd_session", TargetRef: `{"socket":"/tmp/missing-pai849-agentd.sock","session_id":"22222222-2222-4222-8222-222222222222"}`,
			MaximumLevel: "steer", RequestedLevel: "steer"}}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "leased managed steer"})
	var unavailable struct {
		To             string `json:"to"`
		Cursor         int64  `json:"cursor"`
		DeliveryID     string `json:"delivery_id"`
		FallbackReason string `json:"fallback_reason"`
	}
	reroutes, completes, acks := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(agentAttrHeader); got != "worker" {
			t.Errorf("agent header=%q", got)
		}
		switch r.URL.Path {
		case "/api/projects/1/messages/listen":
			_ = json.NewEncoder(w).Encode(inboxPage{Address: "codex:worker", NextCursor: 17, Messages: []messageEnvelope{message}})
		case "/api/projects/1/messages/delivery-unavailable":
			reroutes++
			_ = json.NewDecoder(r.Body).Decode(&unavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"delivery_id": unavailable.DeliveryID, "route": "simple_fallback", "target_id": "fallback-generation"})
		case "/api/projects/1/messages/delivery-complete":
			completes++
		case "/api/projects/1/messages/ack":
			acks++
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := runListen(context.Background(), client, 1, "codex:worker", "worker", false, true, "agentd_codex", "", "queue", false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if reroutes != 1 || completes != 0 || acks != 0 {
		t.Fatalf("reroutes=%d completes=%d acks=%d", reroutes, completes, acks)
	}
	if unavailable.To != "codex:worker" || unavailable.Cursor != 17 || unavailable.DeliveryID != message.DeliveryWork.DeliveryID || unavailable.FallbackReason != "transport_error" {
		t.Fatalf("unavailable payload=%+v", unavailable)
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

// registryCompletion is the delivery-complete payload a registry handoff must
// send instead of the plain cursor ack.
type registryCompletion struct {
	To             string `json:"to"`
	Cursor         int64  `json:"cursor"`
	DeliveryID     string `json:"delivery_id"`
	EffectiveLevel string `json:"effective_level"`
	FallbackReason string `json:"fallback_reason"`
}

// claudeRegistryListenServer serves one bus message carrying leased
// claude_resume delivery work, records the listen query and the
// delivery-complete payload, and rejects the plain cursor ack.
func claudeRegistryListenServer(t *testing.T, message messageEnvelope) (*httptest.Server, *registryCompletion, *string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var completed registryCompletion
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(agentAttrHeader) != "claude" {
			t.Errorf("agent header=%q want claude", r.Header.Get(agentAttrHeader))
		}
		switch r.URL.Path {
		case "/api/projects/1/messages/listen":
			mu.Lock()
			query = r.URL.RawQuery
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(inboxPage{Address: "claude:claude", NextCursor: message.Cursor, Messages: []messageEnvelope{message}})
		case "/api/projects/1/messages/delivery-complete":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&completed)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": message.Cursor})
		case "/api/projects/1/messages/ack":
			t.Errorf("registry delivery work must complete through delivery-complete, not the plain ack")
			http.Error(w, "unexpected ack", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &completed, &query, &mu
}

func leasedClaudeResumeMessage(cursor int64, level string) messageEnvelope {
	message := messageEnvelope{Cursor: cursor, MessageID: "m" + strconv.FormatInt(cursor, 10), From: "paimos:codex", To: "claude:claude", DeliveryLevel: level,
		DeliveryWork: &messageDeliveryWork{DeliveryID: "d" + strconv.FormatInt(cursor, 10), State: "leased", Adapter: "claude_resume",
			TargetKind: "claude_session", TargetRef: claudeTestLocalSession, MaximumLevel: "simple", RequestedLevel: level}}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "registry payload"})
	return message
}

func TestRunListenClaudeRegistryTargetCompletesDelivery(t *testing.T) {
	message := leasedClaudeResumeMessage(41, "steer")
	srv, completed, query, mu := claudeRegistryListenServer(t, message)
	argsFile, stdinFile := installFakeClaude(t)
	_, errOut := captureListenOutput(t)
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	// The legacy --deliver-target names a different (cloud) session and the
	// legacy mode is queue: the receiver-owned registry target and its durable
	// steer request win.
	if err := runListen(context.Background(), client, 1, "claude:claude", "claude", false, true, "claude", claudeTestCloudSession, "queue", false, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsFile)
	if got, want := string(raw), "-p\n--resume\n"+claudeTestLocalSession+"\n"; got != want {
		t.Fatalf("argv=%q want %q", got, want)
	}
	stdin, _ := os.ReadFile(stdinFile)
	if string(stdin) != "registry payload" {
		t.Fatalf("stdin=%q", stdin)
	}
	if !strings.Contains(errOut.String(), "fallback_reason=unsupported") {
		t.Fatalf("stderr=%q want fallback note", errOut.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(*query, "delivery=claude_resume") {
		t.Fatalf("listen query=%q must lease claude_resume work", *query)
	}
	want := registryCompletion{To: "claude:claude", Cursor: 41, DeliveryID: "d41", EffectiveLevel: "simple", FallbackReason: "unsupported"}
	if *completed != want {
		t.Fatalf("completion=%+v want %+v", *completed, want)
	}
}

func TestRunListenClaudeDoesNotCompleteFailedRegistryHandoff(t *testing.T) {
	message := leasedClaudeResumeMessage(42, "simple")
	srv, completed, _, mu := claudeRegistryListenServer(t, message)
	installFakeClaude(t)
	captureListenOutput(t)
	t.Setenv("PAIMOS_TEST_EXIT", "2")
	client := &Client{baseURL: srv.URL, http: srv.Client()}
	err := runListen(context.Background(), client, 1, "claude:claude", "claude", false, true, "claude", "", "queue", false, time.Millisecond)
	if err == nil {
		t.Fatal("expected failed handoff to surface")
	}
	if exit, ok := err.(*listenExitCode); ok {
		t.Fatalf("vendor failure must not be reported as listen exit %d", exit.code)
	}
	mu.Lock()
	defer mu.Unlock()
	if completed.DeliveryID != "" {
		t.Fatalf("failed handoff must leave the lease uncompleted: %+v", *completed)
	}
}

func TestDeliverClaudeMessageRefusesForeignOrMissingDeliveryWork(t *testing.T) {
	for name, work := range map[string]*messageDeliveryWork{
		"blocked without target":  {DeliveryID: "d1", State: "blocked", RequestedLevel: "simple"},
		"channel adapter":         {DeliveryID: "d2", State: "pending", Adapter: "claude_channel", TargetKind: "claude_session", RequestedLevel: "simple"},
		"codex adapter":           {DeliveryID: "d3", State: "pending", Adapter: "codex", TargetKind: "codex_thread", RequestedLevel: "simple"},
		"resume adapter unleased": {DeliveryID: "d4", State: "pending", Adapter: "claude_resume", TargetKind: "claude_session", RequestedLevel: "simple"},
	} {
		t.Run(name, func(t *testing.T) {
			argsFile, _ := installFakeClaude(t)
			captureListenOutput(t)
			message := messageEnvelope{Cursor: 5, MessageID: "m5", DeliveryWork: work}
			// A legacy --deliver-target must not paper over missing registry work.
			_, err := deliverClaudeMessage(context.Background(), message, "payload", claudeTestLocalSession, "queue")
			var unavailable *adapterUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("err=%v want adapter unavailable", err)
			}
			if _, statErr := os.Stat(argsFile); statErr == nil {
				t.Fatal("claude must not run without a usable receiver-owned target")
			}
		})
	}
}

func TestWorkerAdapterForMapsDeliverFlagToRegistryAdapter(t *testing.T) {
	for deliver, want := range map[string]string{"codex": "codex", "agentd_claude": "agentd_claude", "claude": "claude_resume", "grok": "", "": ""} {
		if got := workerAdapterFor(deliver); got != want {
			t.Fatalf("workerAdapterFor(%q)=%q want %q", deliver, got, want)
		}
	}
}

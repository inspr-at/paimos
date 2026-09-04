// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newTestBroker(t *testing.T) (*contextBroker, string) {
	t.Helper()
	root := t.TempDir()
	root, err := canonicalRepoRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	b := newContextBroker(nil, 6, "PAI", root, true)
	b.logger = log.New(io.Discard, "", 0)
	return b, root
}

func writeServeFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestServeLoopbackListenAddrValidation(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0", "localhost:8080"} {
		if !isLoopbackListenAddr(addr) {
			t.Fatalf("%s should be accepted as loopback", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.168.1.9:8080"} {
		if isLoopbackListenAddr(addr) {
			t.Fatalf("%s should be rejected without --unsafe-allow-remote", addr)
		}
	}
}

func TestServeResolveRepoPathRejectsTraversalAndSymlinkEscape(t *testing.T) {
	b, root := newTestBroker(t)
	outside := t.TempDir()
	writeServeFixture(t, root, "safe.go", "package main\n")
	writeServeFixture(t, outside, "secret.txt", "token=supersecretvalue\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := b.resolveRepoPath("safe.go"); err != nil {
		t.Fatalf("safe file rejected: %v", err)
	}
	if _, err := b.resolveRepoPath("../secret.txt"); err == nil {
		t.Fatal("path traversal should be rejected")
	}
	if _, err := b.resolveRepoPath("link.txt"); err == nil {
		t.Fatal("symlink escape should be rejected")
	}
}

func TestServeReadRedactsSecretsAndBlocksSecretFiles(t *testing.T) {
	b, root := newTestBroker(t)
	writeServeFixture(t, root, "config/app.txt", "api_key = \"abc123456789\"\nAuthorization: Bearer verylongtokenvalue\n")
	writeServeFixture(t, root, ".env", "PASSWORD=abc123456789\n")

	resp, err := b.readFile(contextReadRequest{Path: "config/app.txt"})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !resp.Redacted {
		t.Fatal("expected redacted=true")
	}
	if strings.Contains(resp.Content, "abc123456789") || strings.Contains(resp.Content, "verylongtokenvalue") {
		t.Fatalf("secret leaked in content: %q", resp.Content)
	}
	if _, err := b.readFile(contextReadRequest{Path: ".env"}); err == nil {
		t.Fatal(".env should be blocked")
	}
}

func TestServeHTTPRejectsRemoteClientsWhenLoopbackOnly(t *testing.T) {
	b, _ := newTestBroker(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "203.0.113.8:4444"
	rec := httptest.NewRecorder()
	b.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusForbidden)
	}

	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	rec = httptest.NewRecorder()
	b.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback status=%d want %d", rec.Code, http.StatusOK)
	}
}

func TestServeMCPStdioRepoState(t *testing.T) {
	b, root := newTestBroker(t)
	writeServeFixture(t, root, "AGENTS.md", "# agent notes\n")
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"paimos_repo_state","arguments":{}}}` + "\n")
	var out bytes.Buffer
	if err := b.serveMCP(input, &out); err != nil {
		t.Fatalf("serve MCP: %v", err)
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode MCP response: %v\n%s", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected MCP error: %#v", resp.Error)
	}
	if len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, "AGENTS.md") {
		t.Fatalf("repo state content missing AGENTS.md: %s", out.String())
	}
}

func TestServeMCPClaudeChannelCapabilityIsOptIn(t *testing.T) {
	b, _ := newTestBroker(t)
	result, err := b.handleMCPRequest(mcpRequest{Method: "initialize"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "claude/channel") {
		t.Fatalf("channel capability must be absent by default: %s", raw)
	}
	b.channelAddress, b.channelAgent = "claude:claude", "claude"
	result, err = b.handleMCPRequest(mcpRequest{Method: "initialize"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(result)
	if !strings.Contains(string(raw), `"claude/channel"`) || !strings.Contains(string(raw), `"instructions"`) {
		t.Fatalf("channel initialization incomplete: %s", raw)
	}
}

type channelNotification struct {
	Method string `json:"method"`
	Params struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	} `json:"params"`
}

// captureChannelNotification runs the opt-in Channels broker against a fake
// inbox holding one message and returns the single emitted JSON-RPC
// notification after the durable cursor was acknowledged.
func captureChannelNotification(t *testing.T, message messageEnvelope) channelNotification {
	t.Helper()
	acked := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/projects/6/messages/listen":
			if r.Header.Get(agentAttrHeader) != "claude" {
				t.Errorf("missing channel attribution")
			}
			_ = json.NewEncoder(w).Encode(inboxPage{Address: "claude:claude", NextCursor: message.Cursor, Messages: []messageEnvelope{message}})
		case "/api/projects/6/messages/ack":
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": message.Cursor})
			acked <- struct{}{}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	b, _ := newTestBroker(t)
	b.client = &Client{baseURL: srv.URL, http: srv.Client()}
	b.channelAddress, b.channelAgent, b.channelPollInterval = "claude:claude", "claude", time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var out bytes.Buffer
	go b.runClaudeChannel(ctx, newLockedJSONEncoder(&out))
	select {
	case <-acked:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("channel did not acknowledge delivered notification")
	}
	var notification channelNotification
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &notification); err != nil {
		t.Fatalf("decode notification: %v\n%s", err, out.String())
	}
	for key := range notification.Params.Meta {
		// Claude Code silently drops meta keys that are not identifiers.
		if !channelMetaKeyPattern.MatchString(key) {
			t.Fatalf("meta key %q is not an identifier", key)
		}
	}
	return notification
}

func TestServeMCPClaudeChannelNotifiesThenAcknowledges(t *testing.T) {
	message := messageEnvelope{Cursor: 12, MessageID: "m12", From: "paimos:codex", To: "claude:claude", ThreadID: "thread-12", TaskID: "PAI-816"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "framed channel payload"})
	notification := captureChannelNotification(t, message)
	if notification.Method != "notifications/claude/channel" || notification.Params.Content != "framed channel payload" || notification.Params.Meta["cursor"] != "12" {
		t.Fatalf("notification=%#v", notification)
	}
	// PAI-827: the Channels path is a simple handoff with explicit delivery state.
	if notification.Params.Meta["requested_level"] != "simple" || notification.Params.Meta["effective_level"] != "simple" {
		t.Fatalf("channel delivery state=%v want simple/simple", notification.Params.Meta)
	}
	if _, has := notification.Params.Meta["fallback_reason"]; has {
		t.Fatalf("simple request must not record a fallback: %v", notification.Params.Meta)
	}
}

func TestServeMCPClaudeChannelSteerLevelRecordsUnsupportedFallback(t *testing.T) {
	// A durable PAI-826 steer request still reaches the session as the same
	// simple channel push; the downgrade is recorded in meta, and no steer
	// command or socket frame is invented.
	message := messageEnvelope{Cursor: 13, MessageID: "m13", From: "paimos:codex", To: "claude:claude", DeliveryLevel: "steer"}
	message.Parts = append(message.Parts, struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}{Kind: "text", Text: "steer via channel"})
	notification := captureChannelNotification(t, message)
	if notification.Method != "notifications/claude/channel" || notification.Params.Content != "steer via channel" {
		t.Fatalf("notification=%#v", notification)
	}
	meta := notification.Params.Meta
	if meta["requested_level"] != "steer" || meta["effective_level"] != "simple" || meta["fallback_reason"] != "unsupported" {
		t.Fatalf("channel delivery state=%v want steer/simple/unsupported", meta)
	}
}

var channelMetaKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func TestServeMCPClaudeChannelCompletesLeasedRegistryWorkAndSkipsForeignRows(t *testing.T) {
	part := func(text string) struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} {
		return struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}{Kind: "text", Text: text}
	}
	leased := messageEnvelope{Cursor: 14, MessageID: "m14", From: "paimos:codex", To: "claude:claude", DeliveryLevel: "steer",
		DeliveryWork: &messageDeliveryWork{DeliveryID: "d14", State: "leased", Adapter: "claude_channel", TargetKind: "claude_session",
			TargetRef: claudeTestLocalSession, MaximumLevel: "simple", RequestedLevel: "steer"}}
	leased.Parts = append(leased.Parts, part("leased channel push"))
	foreign := messageEnvelope{Cursor: 15, MessageID: "m15", From: "paimos:codex", To: "claude:claude",
		DeliveryWork: &messageDeliveryWork{DeliveryID: "d15", State: "pending", Adapter: "claude_resume", TargetKind: "claude_session", RequestedLevel: "simple"}}
	foreign.Parts = append(foreign.Parts, part("belongs to the resume listener"))
	missing := messageEnvelope{Cursor: 16, MessageID: "m16", From: "paimos:codex", To: "claude:claude",
		DeliveryWork: &messageDeliveryWork{DeliveryID: "d16", State: "blocked", RequestedLevel: "simple"}}
	missing.Parts = append(missing.Parts, part("no target yet"))
	queries := make(chan string, 4)
	completed := make(chan registryCompletion, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/projects/6/messages/listen":
			queries <- r.URL.RawQuery
			_ = json.NewEncoder(w).Encode(inboxPage{Address: "claude:claude", NextCursor: 16, Messages: []messageEnvelope{leased, foreign, missing}})
		case "/api/projects/6/messages/delivery-complete":
			var completion registryCompletion
			_ = json.NewDecoder(r.Body).Decode(&completion)
			_ = json.NewEncoder(w).Encode(map[string]any{"cursor": completion.Cursor})
			completed <- completion
		case "/api/projects/6/messages/ack":
			t.Errorf("registry work must complete through delivery-complete; foreign or blocked rows must never be acknowledged")
			http.Error(w, "unexpected ack", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	b, _ := newTestBroker(t)
	b.client = &Client{baseURL: srv.URL, http: srv.Client()}
	b.channelAddress, b.channelAgent, b.channelPollInterval = "claude:claude", "claude", time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var out bytes.Buffer
	go b.runClaudeChannel(ctx, newLockedJSONEncoder(&out))
	var completion registryCompletion
	select {
	case completion = <-completed:
	case <-time.After(time.Second):
		t.Fatal("channel did not complete the leased delivery")
	}
	if query := <-queries; !strings.Contains(query, "delivery=claude_channel") {
		t.Fatalf("listen query=%q must lease claude_channel work", query)
	}
	want := registryCompletion{To: "claude:claude", Cursor: 14, DeliveryID: "d14", EffectiveLevel: "simple", FallbackReason: "unsupported"}
	if completion != want {
		t.Fatalf("completion=%+v want %+v", completion, want)
	}
	// Only the leased row was pushed; the foreign and blocked rows emit
	// nothing and are left to their own worker or an operator requeue.
	var notifications []channelNotification
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	for dec.More() {
		var notification channelNotification
		if err := dec.Decode(&notification); err != nil {
			t.Fatalf("decode notification: %v\n%s", err, out.String())
		}
		notifications = append(notifications, notification)
	}
	if len(notifications) != 1 || notifications[0].Params.Content != "leased channel push" || notifications[0].Params.Meta["message_id"] != "m14" {
		t.Fatalf("notifications=%#v", notifications)
	}
	meta := notifications[0].Params.Meta
	if meta["requested_level"] != "steer" || meta["effective_level"] != "simple" || meta["fallback_reason"] != "unsupported" {
		t.Fatalf("channel delivery state=%v want steer/simple/unsupported", meta)
	}
}

func TestChannelSkipAuditIsRecordedOncePerStateChange(t *testing.T) {
	b, _ := newTestBroker(t)
	var logs bytes.Buffer
	b.logger = log.New(&logs, "", 0)
	b.channelAddress = "claude:claude"
	b.auditChannelSkip("m1", "claude_resume", "pending")
	b.auditChannelSkip("m1", "claude_resume", "pending")
	b.auditChannelSkip("m1", "", "blocked")
	b.auditChannelSkip("m2", "", "blocked")
	if got := strings.Count(logs.String(), "claude_channel_skip"); got != 3 {
		t.Fatalf("skip audits=%d want 3 (one per message and state change)\n%s", got, logs.String())
	}
}

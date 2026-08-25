// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
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
	err := runListen(context.Background(), client, 1, "codex:codex", "codex", false, false, "", "", false, time.Millisecond)
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
	err := runListen(context.Background(), client, 1, "codex:codex", "codex", false, true, "codex", "", false, time.Millisecond)
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
	if err := runListen(context.Background(), client, 1, "codex:codex", "codex", false, true, "", "", false, time.Millisecond); err != nil {
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
	if err := runListen(context.Background(), client, 1, "codex:receiver", "receiver", false, true, "", "", false, time.Millisecond); err == nil {
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

func TestDeliverClaudeWritesAuthAndUserFrames(t *testing.T) {
	dir, err := os.MkdirTemp("", "pai816-claude-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "claude.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "test-token-never-logged")
	frames := make(chan []map[string]any, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		var got []map[string]any
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var frame map[string]any
			if json.Unmarshal(scanner.Bytes(), &frame) == nil {
				got = append(got, frame)
			}
		}
		frames <- got
	}()
	if err := deliverClaude(context.Background(), "hello Claude", socket); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-frames:
		if len(got) != 2 || got[0]["type"] != "auth" || got[1]["type"] != "user" {
			t.Fatalf("frames=%#v", got)
		}
		message := got[1]["message"].(map[string]any)
		if message["content"] != "hello Claude" {
			t.Fatalf("message=%#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Claude socket frames")
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

//go:build !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const cursorHelperEnvironment = "PAIMOS_CURSOR_ACP_HELPER"

type cursorProtocolTestAdapter struct{ *CursorAdapter }

func (cursorProtocolTestAdapter) AccountLabel(context.Context) string { return "unknown" }

func newTestCursorAdapter(t *testing.T, mode string) (*CursorAdapter, *[]string) {
	t.Helper()
	adapter := NewCursorAdapter(os.Args[0], "test")
	adapter.cliVersion = func(context.Context, string) (string, error) { return cursorSupportedCLIVersion, nil }
	var argv []string
	adapter.command = func(_ string, args ...string) *exec.Cmd {
		argv = append([]string(nil), args...)
		cmd := exec.Command(os.Args[0], "-test.run=^TestCursorACPHelperProcess$")
		cmd.Env = append(os.Environ(), cursorHelperEnvironment+"="+mode)
		return cmd
	}
	return adapter, &argv
}

func cursorTestProfile(t *testing.T) dispatchprofile.Profile {
	t.Helper()
	profile, err := dispatchprofile.Resolve("cursor-composer", dispatchprofile.CatalogVersion, AdapterCursor)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestCursorProcessOwnsExactACPSessionForControl(t *testing.T) {
	adapter, argv := newTestCursorAdapter(t, "serve")
	profile := cursorTestProfile(t)
	events := make(chan AdapterEvent, 16)
	process, err := adapter.Start(context.Background(), StartRequest{
		KeepAlive: true, Workspace: t.TempDir(), Prompt: "secret-not-persisted",
		Identity: "cursor:test", Adapter: AdapterCursor, ResolvedProfile: &profile,
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*argv, []string{"--model", "composer", "acp"}) || process.PID() <= 0 {
		t.Fatalf("argv=%q pid=%d", *argv, process.PID())
	}
	if _, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "steer-unsupported", Text: "same-turn"}); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("steer err=%v", err)
	}
	interrupt, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "control-live"})
	if err != nil {
		t.Fatal(err)
	}
	if interrupt.Primitive != cursorCancelPrimitive || interrupt.VendorMessageID != "sess-owned" {
		t.Fatalf("interrupt=%+v", interrupt)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "stop-live"}); err != nil {
		t.Fatal(err)
	}
	seenSession, seenTurn, seenControl := false, false, false
	deadline := time.After(2 * time.Second)
	for !seenSession || !seenTurn || !seenControl {
		select {
		case event := <-events:
			seenSession = seenSession || event.Kind == EventSessionStarted && event.HarnessSessionID == "sess-owned"
			seenTurn = seenTurn || event.Kind == EventTurnStarted
			seenControl = seenControl || event.Kind == EventControlApplied && event.CorrelationID != ""
		case <-deadline:
			t.Fatalf("events session=%t turn=%t control=%t", seenSession, seenTurn, seenControl)
		}
	}
}

func TestCursorInboxDeliversNextTurnAfterIdle(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "idle")
	profile := cursorTestProfile(t)
	process, err := adapter.Start(context.Background(), StartRequest{
		KeepAlive: true, Workspace: t.TempDir(), Prompt: "first-turn",
		Identity: "cursor:test", Adapter: AdapterCursor, ResolvedProfile: &profile,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	inboxProc, ok := process.(interface {
		Inbox(context.Context, ControlRequest) (ControlEffect, error)
		InboxReady() bool
	})
	if !ok {
		t.Fatal("cursor process does not implement inbox")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !inboxProc.InboxReady() {
		if time.Now().After(deadline) {
			t.Fatal("initial prompt did not become idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	inbox, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "delivery-next", Text: "next-turn"})
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Primitive != cursorPromptPrimitive || inbox.CorrelationID != "delivery-next" || inbox.VendorMessageID != "sess-owned" {
		t.Fatalf("inbox=%+v", inbox)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "idle-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRefusesUnapprovedAndAutoModels(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	for _, model := range []string{"auto", "Auto", "gpt-5", "sonnet-4-thinking"} {
		profile := dispatchprofile.Profile{
			ID: "cursor-unapproved", Version: "1", Harness: AdapterCursor, Model: model, Effort: "high",
			MachineSource: dispatchprofile.MachineAuthenticatedReporter, AccountSource: dispatchprofile.AccountLocalProbe, WorkspaceMode: "exclusive",
		}
		_, err := adapter.Start(context.Background(), StartRequest{
			Workspace: t.TempDir(), Prompt: "work", Identity: "cursor:test", Adapter: AdapterCursor, ResolvedProfile: &profile,
		}, nil)
		if err == nil {
			t.Fatalf("model %q was accepted", model)
		}
	}
}

func TestCursorStartRequiresDispatchProfile(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "serve")
	_, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Prompt: "work", Identity: "cursor:test", Adapter: AdapterCursor,
	}, nil)
	if !errors.Is(err, ErrDispatchProfile) {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorStartCancellationReapsInFlightACP(t *testing.T) {
	adapter := NewCursorAdapter(os.Args[0], "test")
	adapter.cliVersion = func(context.Context, string) (string, error) { return cursorSupportedCLIVersion, nil }
	var child *exec.Cmd
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		child = exec.Command(os.Args[0], "-test.run=^TestCursorACPHelperProcess$")
		child.Env = append(os.Environ(), cursorHelperEnvironment+"=block")
		return child
	}
	profile := cursorTestProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := adapter.Start(ctx, StartRequest{
		Workspace: t.TempDir(), Prompt: "work", Identity: "cursor:test", Adapter: AdapterCursor, ResolvedProfile: &profile,
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || child == nil || child.Process == nil || child.ProcessState == nil {
		t.Fatalf("err=%v child=%v", err, child)
	}
}

func TestCursorRejectsOversizedAndMalformedFrames(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "malformed")
	profile := cursorTestProfile(t)
	_, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Prompt: "work", Identity: "cursor:test", Adapter: AdapterCursor, ResolvedProfile: &profile,
	}, nil)
	if err == nil {
		t.Fatal("malformed ACP initialize was accepted")
	}
}

func TestCursorPermissionAndExtensionRequestsFailClosed(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "permission")
	profile := cursorTestProfile(t)
	events := make(chan AdapterEvent, 8)
	process, err := adapter.Start(context.Background(), StartRequest{
		KeepAlive: true, Workspace: t.TempDir(), Prompt: "work",
		Identity: "cursor:test", Adapter: AdapterCursor, ResolvedProfile: &profile,
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	ready, ok := process.(interface{ InboxReady() bool })
	if !ok {
		t.Fatal("cursor process does not implement inbox ready")
	}
	for !ready.InboxReady() {
		if time.Now().After(deadline) {
			t.Fatal("permission helper did not complete the refused turn")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "perm-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorPinnedVersionMismatchRefusesStart(t *testing.T) {
	adapter := NewCursorAdapter(os.Args[0], "test")
	adapter.cliVersion = func(context.Context, string) (string, error) { return "2026.01.01-deadbeef", nil }
	adapter.command = func(string, ...string) *exec.Cmd { t.Fatal("child must not spawn"); return nil }
	profile := cursorTestProfile(t)
	_, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Prompt: "work", Identity: "cursor:test", Adapter: AdapterCursor, ResolvedProfile: &profile,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "pinned Cursor CLI version") {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorAccountProbeUsesStatusJSONAndStaysUnknownWithoutClosedClass(t *testing.T) {
	probe := writeProbe(t, `#!/bin/sh
[ "$1:$2:$3" = "status:--format:json" ] || exit 9
printf '%s\n' '{"loggedIn":true,"email":"must-not-be-recorded@example.invalid"}'
`)
	if got := NewCursorAdapter(probe, "test").AccountLabel(context.Background()); got != "unknown" {
		t.Fatalf("closed label=%q", got)
	}
	loggedOut := writeProbe(t, `#!/bin/sh
printf '%s\n' '{"loggedIn":false}'
`)
	if got := NewCursorAdapter(loggedOut, "test").AccountLabel(context.Background()); got != "unknown" {
		t.Fatalf("logged-out label=%q", got)
	}
}

func TestCursorSupervisorReplayAndLostOwnership(t *testing.T) {
	adapter, _ := newTestCursorAdapter(t, "idle")
	profile := cursorTestProfile(t)
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-cursor", Adapters: []Adapter{cursorProtocolTestAdapter{adapter}}})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCursor, Workspace: t.TempDir(), Prompt: "work", Identity: "cursor:worker", ProjectID: 963, ResolvedProfile: &profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !supervisor.InboxReady(session.ID) {
		if time.Now().After(deadline) {
			t.Fatal("owned cursor session never became inbox-ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	first, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "delivery-one", Text: "next",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "delivery-one", Text: "next",
	})
	if err != nil || first != second {
		t.Fatalf("replay first=%+v second=%+v err=%v", first, second, err)
	}
	if _, err := supervisor.Steer(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "steer-one", Text: "mid-turn",
	}); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("steer err=%v", err)
	}
	if _, err := supervisor.Stop(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-cursor", ProjectID: 963, Identity: "cursor:worker", CorrelationID: "stop-one",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorACPHelperProcess(t *testing.T) {
	mode := os.Getenv(cursorHelperEnvironment)
	if mode == "" {
		t.Skip("cursor ACP helper")
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	var mu sync.Mutex
	respond := func(id any, result any) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	request := func(id any, method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}
	notify := func(method string, params any) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
	if mode == "malformed" {
		_, _ = os.Stdout.Write([]byte("{not-json\n"))
		os.Exit(0)
	}
	if mode == "block" {
		scanner.Scan()
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	promptIDs := map[any]bool{}
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		method, _ := message["method"].(string)
		id := message["id"]
		switch method {
		case "initialize":
			respond(id, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":        true,
					"promptCapabilities": map[string]any{"image": true, "audio": false, "embeddedContext": false},
				},
				"agentInfo": map[string]string{"name": "cursor-agent", "version": cursorSupportedCLIVersion},
			})
		case "session/new":
			respond(id, map[string]any{"sessionId": "sess-owned"})
			if mode == "permission" {
				request("perm-1", "session/request_permission", map[string]any{
					"sessionId": "sess-owned",
					"options": []map[string]string{
						{"optionId": "allow-always", "kind": "allow_always"},
						{"optionId": "reject-once", "kind": "reject_once"},
					},
				})
				request("q-1", "cursor/ask_question", map[string]any{"toolCallId": "call-q", "questions": []any{}})
				request("plan-1", "cursor/create_plan", map[string]any{"toolCallId": "call-p", "plan": "secret-plan"})
				request("unknown-1", "cursor/unknown_extension", map[string]any{"toolCallId": "call-x"})
			}
		case "session/prompt":
			promptIDs[id] = true
			notify("session/update", map[string]any{
				"sessionId": "sess-owned",
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "ok"}},
			})
			if mode == "idle" || mode == "permission" {
				respond(id, map[string]any{"stopReason": "end_turn"})
			}
		case "session/cancel":
			for promptID := range promptIDs {
				respond(promptID, map[string]any{"stopReason": "cancelled"})
				delete(promptIDs, promptID)
			}
		default:
			os.Exit(2)
		}
	}
	os.Exit(0)
}

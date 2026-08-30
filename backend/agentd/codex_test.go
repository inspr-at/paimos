//go:build !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"
)

const codexHelperEnvironment = "PAIMOS_CODEX_APP_SERVER_HELPER"

func TestCodexProcessOwnsExactAppServerSessionForControl(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	var argv []string
	adapter.command = func(_ string, args ...string) *exec.Cmd {
		argv = append([]string(nil), args...)
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=serve")
		return cmd
	}
	events := make(chan AdapterEvent, 16)
	process, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:test", Adapter: AdapterCodex,
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv, []string{"app-server", "--listen", "stdio://"}) || process.PID() <= 0 {
		t.Fatalf("argv=%q pid=%d", argv, process.PID())
	}
	steer, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "delivery-live", Text: "new direction"})
	if err != nil {
		t.Fatal(err)
	}
	if steer.Primitive != "codex app-server turn/steer" || steer.CorrelationID != "delivery-live" || steer.VendorMessageID != "turn-owned" {
		t.Fatalf("steer=%+v", steer)
	}
	interrupt, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "control-live"})
	if err != nil {
		t.Fatal(err)
	}
	if interrupt.Primitive != "codex app-server turn/interrupt" || interrupt.VendorMessageID != "turn-owned" {
		t.Fatalf("interrupt=%+v", interrupt)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	seenSession, seenTurn, seenControl := false, false, false
	for len(events) > 0 {
		event := <-events
		seenSession = seenSession || event.Kind == EventSessionStarted && event.HarnessSessionID == "thread-owned"
		seenTurn = seenTurn || event.Kind == EventTurnStarted
		seenControl = seenControl || event.Kind == EventControlApplied && event.CorrelationID != ""
	}
	if !seenSession || !seenTurn || !seenControl {
		t.Fatalf("events session=%t turn=%t control=%t", seenSession, seenTurn, seenControl)
	}
}

func TestCodexStartCancellationReapsInFlightAppServer(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	var child *exec.Cmd
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		child = exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		child.Env = append(os.Environ(), codexHelperEnvironment+"=block")
		return child
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := adapter.Start(ctx, StartRequest{Workspace: t.TempDir(), Prompt: "work", Identity: "codex:test", Adapter: AdapterCodex}, nil)
	if err == nil || child == nil || child.ProcessState == nil || !child.ProcessState.Exited() {
		t.Fatalf("err=%v child=%v state=%v", err, child, child.ProcessState)
	}
}

func TestCodexDrainsFinalCompletionBeforeReapingAppServer(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=complete-and-exit")
		return cmd
	}
	var eventsMu sync.Mutex
	var events []AdapterEvent
	process, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:drain", Adapter: AdapterCodex,
	}, func(event AdapterEvent) {
		// Keep the pipe measurably backlogged after the child exits. Calling
		// Cmd.Wait concurrently with this callback used to close StdoutPipe and
		// discard the final turn/completed frame.
		if event.Kind == EventToolStarted {
			time.Sleep(time.Millisecond)
		}
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("final completion was not drained before reap: %v", err)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, event := range events {
		if event.ErrorCode == ErrorEventStreamBound {
			t.Fatalf("normal child exit was misreported as a bounded stream error: %+v", events)
		}
	}
}

func TestCodexLiveOwnedAppServerSteer(t *testing.T) {
	if os.Getenv("PAIMOS_AGENTD_LIVE_CODEX") != "1" {
		t.Skip("set PAIMOS_AGENTD_LIVE_CODEX=1 with an authenticated codex CLI to run live proof")
	}
	adapter := NewCodexAdapter("", "live-proof")
	process, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Adapter: AdapterCodex, Identity: "codex:live-proof",
		Prompt: "Keep this turn active briefly while inspecting the empty workspace.",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "live-proof-cleanup"})
	})
	if _, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "live-proof-delivery", Text: "Acknowledge this steer before finishing."}); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "live-proof-interrupt"}); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAppServerHelperProcess(t *testing.T) {
	mode := os.Getenv(codexHelperEnvironment)
	if mode == "" {
		t.Skip("helper process")
	}
	if mode == "block" {
		select {}
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		respond := func(result any) {
			if encoder.Encode(map[string]any{"id": request.ID, "result": result}) != nil {
				os.Exit(2)
			}
		}
		switch request.Method {
		case "initialized":
			continue
		case "initialize":
			respond(map[string]any{})
		case "thread/start":
			respond(map[string]any{"thread": map[string]any{"id": "thread-owned"}})
			_ = encoder.Encode(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]any{"id": "thread-owned"}}})
		case "turn/start":
			var params struct {
				ThreadID string `json:"threadId"`
				Input    []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.ThreadID != "thread-owned" || len(params.Input) != 1 || params.Input[0].Text != "secret-not-persisted" {
				fmt.Fprintln(os.Stderr, "invalid turn/start")
				os.Exit(2)
			}
			respond(map[string]any{"turn": map[string]any{"id": "turn-owned", "status": "inProgress"}})
			_ = encoder.Encode(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": "inProgress"}}})
			if mode == "complete-and-exit" {
				for range 48 {
					_ = encoder.Encode(map[string]any{"method": "item/started", "params": map[string]any{}})
				}
				_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": "completed"}}})
				return
			}
		case "turn/steer":
			respond(map[string]any{"turnId": "turn-owned"})
		case "turn/interrupt":
			respond(map[string]any{})
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": "interrupted"}}})
		default:
			os.Exit(2)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/piprobe"
)

// TestFakeVendorProcess is invoked only through a private test wrapper. It
// models documented protocol acknowledgements without invoking vendor CLIs.
func TestFakeVendorProcess(t *testing.T) {
	vendor := os.Getenv("AEON_FAKE_VENDOR")
	if vendor == "" {
		return
	}
	read := bufio.NewScanner(os.Stdin)
	write := json.NewEncoder(os.Stdout)
	for read.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Type   string          `json:"type"`
		}
		if json.Unmarshal(read.Bytes(), &frame) != nil {
			os.Exit(2)
		}
		id := strings.Trim(string(frame.ID), "\"")
		if vendor == "claude" {
			var command struct {
				Op            string `json:"op"`
				CorrelationID string `json:"correlation_id"`
			}
			if json.Unmarshal(read.Bytes(), &command) != nil {
				os.Exit(2)
			}
			if command.Op == "start" {
				_ = write.Encode(map[string]string{"kind": "session_started", "harness_session_id": "claude-session", "effective_model": "test-model", "model_evidence_status": "vendor_reported"})
				_ = write.Encode(map[string]any{"kind": "turn_started"})
				_ = write.Encode(map[string]any{"kind": "usage", "input_tokens_total": 12, "output_tokens_total": 3, "cost_usd_total": json.Number("0.0000125")})
				_ = write.Encode(map[string]any{"kind": "usage", "input_tokens_total": 12, "output_tokens_total": 3, "cost_usd_total": json.Number("0.0000125")})
				_ = write.Encode(map[string]any{"kind": "usage", "input_tokens_total": 15, "output_tokens_total": 4, "cost_usd_total": json.Number("0.000020"),
					"models": []map[string]any{{"model": "test-model", "input_tokens": 15, "output_tokens": 4, "cached_input_tokens": 4}}})
				_ = write.Encode(map[string]any{"kind": "turn_completed"})
			} else {
				_ = write.Encode(map[string]string{"kind": "control_applied", "correlation_id": command.CorrelationID})
			}
			continue
		}
		if vendor == "pi" {
			data := any(map[string]any{})
			if frame.Type == "get_state" {
				data = map[string]any{"model": map[string]string{"provider": "anthropic", "id": "test-model"}, "thinkingLevel": "high"}
			}
			if frame.Type == "get_available_models" {
				data = map[string]any{"models": []map[string]string{{"provider": "anthropic", "id": "test-model"}}}
			}
			if frame.Type == "clear_queue" {
				data = map[string]any{"steering": []string{"held steer"}, "followUp": []string{"held follow"}}
			}
			_ = write.Encode(map[string]any{"id": id, "type": "response", "command": frame.Type, "success": true, "data": data})
			continue
		}
		if id == "" {
			continue
		}
		result := any(map[string]any{})
		switch frame.Method {
		case "account/rateLimits/read":
			if vendor == "codex_capacity" || vendor == "codex_limited" {
				result = map[string]any{"rateLimits": map[string]any{"primary": map[string]any{"usedPercent": 31, "windowDurationMins": 10080, "resetsAt": time.Now().Add(24 * time.Hour).Unix()}}, "ordinaryUsageAllowed": vendor != "codex_limited"}
			}
		case "account/read":
			result = map[string]any{"account": map[string]string{"type": "chatgpt", "email": "agent@example.test"}}
		case "thread/start":
			result = map[string]any{"thread": map[string]string{"id": "thread-1"}}
			if vendor == "codex_metadata" {
				result = map[string]any{"thread": map[string]string{"id": "thread-1"}, "model": "model-a", "reasoningEffort": "high"}
			}
		case "turn/start":
			if vendor == "codex_limit_error" {
				_ = write.Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "error": map[string]any{"code": -32000, "data": map[string]string{"codexErrorInfo": "usageLimitExceeded"}}})
				continue
			}
			if vendor == "codex_metadata" {
				for _, settings := range []map[string]any{
					{"model": "unrelated", "effort": "low", "threadId": "other-thread"},
					{"model": "model-b", "effort": "xhigh", "threadId": "thread-1"},
					{"model": "model-b", "effort": "xhigh", "threadId": "thread-1"},
					{"model": "model-c", "threadId": "thread-1"},
				} {
					threadID := settings["threadId"]
					delete(settings, "threadId")
					_ = write.Encode(map[string]any{"jsonrpc": "2.0", "method": "thread/settings/updated", "params": map[string]any{"threadId": threadID, "threadSettings": settings}})
				}
			}
			result = map[string]any{"turn": map[string]string{"id": "turn-1", "status": "inProgress"}}
			if vendor == "codex_idle" {
				if write.Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result}) != nil {
					os.Exit(2)
				}
				if write.Encode(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
					"threadId": "thread-1", "turn": map[string]string{"id": "turn-1", "status": "completed"},
				}}) != nil {
					os.Exit(2)
				}
				for read.Scan() {
				}
				return
			}
		case "turn/steer":
			for _, total := range []int{20, 20, 19} {
				_ = write.Encode(map[string]any{"jsonrpc": "2.0", "method": "thread/tokenUsage/updated", "params": map[string]any{
					"threadId": "thread-1", "tokenUsage": map[string]any{"total": map[string]int{"inputTokens": total, "outputTokens": 4}}}})
			}
			result = map[string]string{"turnId": "turn-1"}
		case "session/new":
			result = map[string]any{"sessionId": "session-1", "modes": map[string]string{"currentModeId": "default"}, "models": map[string]string{"currentModelId": "test-model"}}
		case "session/prompt":
			for i := 0; i < 2; i++ {
				_ = write.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"sessionId": "session-1", "update": map[string]any{"sessionUpdate": "usage_update", "used": 100,
						"size": 1000, "cost": map[string]any{"amount": json.Number("0.0000075"), "currency": "USD"}}}})
			}
			time.Sleep(500 * time.Millisecond)
			result = map[string]string{"stopReason": "end_turn"}
		case "initialize":
			if strings.HasPrefix(vendor, "cursor") {
				result = map[string]int{"protocolVersion": 1}
			}
		}
		_ = write.Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result})
		if vendor == "codex_capacity" && frame.Method == "turn/start" {
			_ = write.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]string{"id": "turn-1", "status": "completed"}}})
		}
	}
}

func fakeVendorPath(t *testing.T, vendor string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "fake-vendor")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = status ]; then printf '%%s\\n' '{\"status\":\"authenticated\",\"isAuthenticated\":true,\"userInfo\":{\"userId\":\"42\"}}'; exit 0; fi\nif [ \"$1\" = login ]; then printf '%%s\\n' 'Logged in using ChatGPT'; exit 0; fi\nif [ \"$1\" = auth ]; then printf '%%s\\n' '{\"loggedIn\":true}'; exit 0; fi\nAEON_FAKE_VENDOR=%s exec %q -test.run=TestFakeVendorProcess\n", vendor, exe)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func adapterRequest(t *testing.T) StartRequest {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	return StartRequest{TenantID: "tenant", PrincipalID: "agent", Run: Run{ID: "run", WorkOrderID: "order", AgentPrincipalID: "agent"},
		Profile: Profile{ID: "profile", Harness: Codex, Model: "test-model", Effort: "high"}, AccountKey: "account", Workspace: workspace, StateRoot: state, Prompt: "hello", Generation: "generation"}
}

func TestCodexAppServerProtocolAndSteer(t *testing.T) {
	r := adapterRequest(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(fakeVendorPath(t, "codex"), map[string]string{"account": home})
	a.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan AdapterEvent, 16)
	p, err := a.Start(ctx, r, func(ev AdapterEvent) { events <- ev })
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Control(ctx, "steer", "follow up"); err != nil {
		t.Fatal(err)
	}
	var turns, input, output int64
	for turns < 2 || input < 20 {
		select {
		case ev := <-events:
			turns += ev.TurnCountDelta
			input += ev.InputTokensDelta
			output += ev.OutputTokensDelta
		case <-ctx.Done():
			t.Fatalf("Codex usage missing: turns=%d input=%d output=%d", turns, input, output)
		}
	}
	if turns != 2 || input != 20 || output != 4 {
		t.Fatalf("Codex usage: turns=%d input=%d output=%d", turns, input, output)
	}
	if err := p.Control(ctx, "interrupt", ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCodexOwnedSettingsReachHarnessHeartbeats(t *testing.T) {
	r := adapterRequest(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(fakeVendorPath(t, "codex_metadata"), map[string]string{"account": home})
	a.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
	api := &fakeAPI{}
	s := &Supervisor{api: api, generation: r.Generation}
	entry := &owned{record: Record{Generation: r.Generation, State: "starting"}, harness: HarnessSession{ID: "owned-session", ProjectID: "project", Lease: "owned-lease"}}
	other := &owned{record: Record{Generation: r.Generation, State: "running"}, harness: HarnessSession{ID: "other-session", ProjectID: "project", Lease: "other-lease"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, err := a.Start(ctx, r, func(ev AdapterEvent) {
		if ev.HarnessModel != "" || ev.HarnessEffort != "" {
			s.observe(entry, ev)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(ctx)
	entry.mu.Lock()
	entry.record.State = "running"
	entry.mu.Unlock()
	if err := s.heartbeatHarness(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := s.heartbeatHarness(ctx, other); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	var ownedBeats []HarnessSession
	for _, beat := range api.harnessBeatSessions {
		if beat.ID == "owned-session" {
			ownedBeats = append(ownedBeats, beat)
		} else if beat.Model != "" || beat.ReasoningEffort != "" {
			t.Fatalf("unrelated session overwritten: %+v", beat)
		}
	}
	if len(ownedBeats) != 4 {
		t.Fatalf("expected three verified changes and one metadata-free heartbeat, got %+v", ownedBeats)
	}
	for i, want := range []struct{ model, effort string }{{"model-a", "high"}, {"model-b", "xhigh"}, {"model-c", ""}, {"", ""}} {
		if ownedBeats[i].Model != want.model || ownedBeats[i].ReasoningEffort != want.effort {
			t.Fatalf("heartbeat %d: model=%q effort=%q", i, ownedBeats[i].Model, ownedBeats[i].ReasoningEffort)
		}
		if ownedBeats[i].ActivitySequence != int64(i+1) {
			t.Fatalf("heartbeat %d lost activity sequence: %d", i, ownedBeats[i].ActivitySequence)
		}
	}
	entry.mu.Lock()
	entry.record.Generation = "old-generation"
	entry.mu.Unlock()
	if err := s.heartbeatHarness(ctx, entry); err != ErrGeneration {
		t.Fatalf("stale generation heartbeat was accepted: %v", err)
	}
}

func TestCodexMissingRuntimeMetadataIsOmitted(t *testing.T) {
	r := adapterRequest(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(fakeVendorPath(t, "codex_no_metadata"), map[string]string{"account": home})
	a.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
	events := make(chan AdapterEvent, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, err := a.Start(ctx, r, func(ev AdapterEvent) {
		if ev.HarnessModel != "" || ev.HarnessEffort != "" {
			events <- ev
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(ctx)
	select {
	case ev := <-events:
		t.Fatalf("launch request became runtime metadata: %+v", ev)
	default:
	}
}

func TestPiRPCStateAndSteer(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness = Pi
	r.Profile.Model = "anthropic/test-model"
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewPiAdapter(fakeVendorPath(t, "pi"), map[string]string{"account": home})
	a.SetExpectedProviders(map[string]string{"account": "anthropic"})
	// Exercise npm's env-node entrypoint with no node on the service PATH.
	script, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Path, []byte(strings.Replace(string(script), "#!/bin/sh", "#!/usr/bin/env node", 1)), 0700); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(home, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\n[ -z \"$NODE_OPTIONS\" ] && [ -z \"$ANTHROPIC_API_KEY\" ] || exit 3\nexec /bin/sh \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a.Nodes = map[string]piprobe.Node{"account": {Path: node, Version: "22.19.0"}}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("NODE_OPTIONS", "synthetic-not-an-option")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-not-a-credential")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrong := r
	wrong.Profile.Model = "openai/test-model"
	if process, err := a.Start(ctx, wrong, func(AdapterEvent) { t.Error("wrong provider prompted") }); err == nil || process != nil {
		t.Fatal("guided pi account accepted another provider")
	}
	var invented bool
	p, err := a.Start(ctx, r, func(ev AdapterEvent) {
		if ev.SessionUsage != nil || ev.InputTokensDelta != 0 || ev.OutputTokensDelta != 0 {
			invented = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Control(ctx, "steer", "follow up"); err != nil {
		t.Fatal(err)
	}
	if err := p.Control(ctx, "interrupt", ""); err != nil {
		t.Fatal(err)
	}
	pi := p.(*piProcess)
	if got := pi.queue.Snapshot(); len(got) != 1 || len(got[0].FollowUp) != 1 {
		t.Fatalf("held queue: %#v", got)
	}
	if err := p.Control(ctx, "steer", "must wait"); err == nil {
		t.Fatal("steer bypassed held queue")
	}
	if err := p.Control(ctx, "resume", ""); err != nil {
		t.Fatal(err)
	}
	if got := pi.queue.Snapshot(); len(got) != 0 {
		t.Fatalf("held queue not settled: %#v", got)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if invented {
		t.Fatal("Pi events invented token usage")
	}
}

func TestPiProbeBackoffAndFreshLaunch(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness, r.Profile.Model = Pi, "anthropic/test-model"
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := fakeVendorPath(t, "pi")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(home, "probe-count")
	prefix := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\n", count)
	script := prefix + strings.TrimPrefix(string(original), "#!/bin/sh\n")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := NewPiAdapter(path, map[string]string{"account": home})
	a.SetExpectedProviders(map[string]string{"account": "anthropic"})
	checkCount := func(want int) {
		t.Helper()
		raw, err := os.ReadFile(count)
		if err != nil || len(raw) != want {
			t.Fatalf("probe launches=%d, want %d: %v", len(raw), want, err)
		}
	}
	for range 3 {
		if available, err := a.ProbeStatus(t.Context(), "account"); !available || err != nil {
			t.Fatal("healthy probe failed", err)
		}
	}
	checkCount(1)
	// Expiry permits a new probe without sleeping in the test.
	a.probeMu.Lock()
	cached := a.probes["account"]
	cached.expires = time.Now().Add(-time.Second)
	a.probes["account"] = cached
	a.probeMu.Unlock()
	if !a.Probe(t.Context(), "account") {
		t.Fatal("expired probe not refreshed")
	}
	checkCount(2)
	// A different provider must never inherit a cached success.
	a.Providers["account"] = "openai"
	if available, err := a.ProbeStatus(t.Context(), "account"); available || err != nil {
		t.Fatal("missing provider misclassified", err)
	}
	checkCount(3)
	a.Providers["account"] = "anthropic"
	if !a.Probe(t.Context(), "account") {
		t.Fatal("restored provider not checked")
	}
	checkCount(4)
	// Even within the cache window, a run must freshly check the harness.
	if err := os.WriteFile(path, []byte(prefix+"exit 127\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if p, err := a.Start(t.Context(), r, func(AdapterEvent) { t.Error("failed harness emitted an event") }); p != nil || !errors.Is(err, piprobe.ErrStart) {
		t.Fatal("cached success allowed a failed harness to start", err)
	}
	checkCount(5)
	for range 3 {
		if available, err := a.ProbeStatus(t.Context(), "account"); available || !errors.Is(err, piprobe.ErrStart) {
			t.Fatal("startup failure became login failure", err)
		}
	}
	checkCount(5)
}

func TestCursorACPAndAccountBinding(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness = Cursor
	a := NewCursorAdapter(fakeVendorPath(t, "cursor"), map[string]string{"account": "42"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan AdapterEvent, 16)
	p, err := a.Start(ctx, r, func(ev AdapterEvent) { events <- ev })
	if err != nil {
		t.Fatal(err)
	}
	var turns, cost int64
	for turns < 1 || cost < 8 {
		select {
		case ev := <-events:
			if ev.SessionUsage != nil || ev.InputTokensDelta != 0 || ev.OutputTokensDelta != 0 {
				t.Fatal("Cursor ACP cost was converted to tokens")
			}
			turns += ev.TurnCountDelta
			cost += ev.CostMicrosDelta
		case <-ctx.Done():
			t.Fatalf("Cursor usage missing: turns=%d cost=%d", turns, cost)
		}
	}
	if turns != 1 || cost != 8 {
		t.Fatalf("Cursor usage: turns=%d cost=%d", turns, cost)
	}
	if err := p.Control(ctx, "steer", "unsafe"); err != ErrUnsupported {
		t.Fatalf("Cursor steer should be closed: %v", err)
	}
	if err := p.Control(ctx, "interrupt", ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAndGrokFailClosedWithoutBindings(t *testing.T) {
	r := adapterRequest(t)
	if _, err := NewClaudeAdapter("/missing", "/missing", "/missing", nil).Start(context.Background(), r, func(AdapterEvent) {}); err == nil {
		t.Fatal("Claude accepted unenrolled account")
	}
	if _, err := NewGrokAdapter().Start(context.Background(), r, func(AdapterEvent) {}); err == nil {
		t.Fatal("Grok workspace run unexpectedly accepted")
	}
}

func TestPiHeldQueueRefusesOtherGeneration(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness = Pi
	queue, err := openPiQueue(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Put(piHeldQueue{TenantID: r.TenantID, PrincipalID: r.PrincipalID, RunID: r.Run.ID, Generation: r.Generation, Steering: []string{"held"}}); err != nil {
		t.Fatal(err)
	}
	r.Generation = "new-generation"
	if _, err := openPiQueue(r); err == nil {
		t.Fatal("old queue silently resumed")
	}
}

func TestClaudeBridgeControlProtocol(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness = Claude
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := fakeVendorPath(t, "claude")
	node, sdk := claudeAdapterDependencies(t, path)
	a := NewClaudeAdapter(node, sdk, path, map[string]string{"account": home})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan AdapterEvent, 16)
	p, err := a.Start(ctx, r, func(ev AdapterEvent) { events <- ev })
	if err != nil {
		t.Fatal(err)
	}
	var turns, input, output, cost int64
	for turns < 1 || input < 15 || cost < 20 {
		select {
		case ev := <-events:
			turns += ev.TurnCountDelta
			input += ev.InputTokensDelta
			output += ev.OutputTokensDelta
			cost += ev.CostMicrosDelta
		case <-ctx.Done():
			t.Fatalf("Claude usage missing: turns=%d input=%d output=%d cost=%d", turns, input, output, cost)
		}
	}
	if turns != 1 || input != 15 || output != 4 || cost != 20 {
		t.Fatalf("Claude usage: turns=%d input=%d output=%d cost=%d", turns, input, output, cost)
	}
	if err := p.Control(ctx, "steer", "follow up"); err != nil {
		t.Fatal(err)
	}
	if err := p.Control(ctx, "interrupt", ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	_ = p.Wait()
}

func TestClaudeBridgeMapsSDKResultUsage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	bridgePath := filepath.Join(root, "bridge.mjs")
	if err := os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	sdkPath := filepath.Join(root, "sdk.mjs")
	const sdk = `export function query() {
  return {
    streamInput: async () => {}, interrupt: async () => ({ still_queued: [] }), close: () => {},
    async *[Symbol.asyncIterator]() {
      yield { type: 'system', subtype: 'init', session_id: 'fake-session', model: 'test-model', capabilities: ['interrupt_receipt_v1'] };
      yield { type: 'result', modelUsage: { model: { inputTokens: 10, cacheCreationInputTokens: 3, cacheReadInputTokens: 4, outputTokens: 5 } }, total_cost_usd: 0.0000125 };
    }
  };
}`
	if err := os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, bridgePath, sdkPath, "/bin/true", root)
	cmd.Stdin = strings.NewReader(`{"op":"start","prompt":"hello","model":"test-model","effort":"high"}` + "\n")
	output, _ := cmd.Output() // The fake Query closes without a normal stop receipt.
	var turn, usage, models bool
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		var frame struct {
			Kind    string          `json:"kind"`
			Input   int64           `json:"input_tokens_total"`
			Output  int64           `json:"output_tokens_total"`
			Cached  *int64          `json:"cached_input_tokens_total"`
			CostUSD json.RawMessage `json:"cost_usd_total"`
			Models  []struct {
				Model  string `json:"model"`
				Input  int64  `json:"input_tokens"`
				Output int64  `json:"output_tokens"`
				Cached int64  `json:"cached_input_tokens"`
			} `json:"models"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil {
			continue
		}
		if frame.Kind == "turn_started" {
			turn = true
		}
		if frame.Kind == "usage" && frame.Input == 17 && frame.Output == 5 {
			cost, ok := usdMicros(frame.CostUSD)
			usage = ok && cost == 13 && frame.Cached != nil && *frame.Cached == 4
			models = len(frame.Models) == 1 && frame.Models[0].Model == "model" && frame.Models[0].Input == 17 && frame.Models[0].Output == 5 && frame.Models[0].Cached == 4
		}
	}
	if !turn || !usage || !models {
		t.Fatalf("Claude bridge did not map result usage: turn=%t usage=%t models=%t output=%s", turn, usage, models, output)
	}
}

func TestClaudeBridgeDeniesNativeEdits(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "work")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{workspace, outside} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	bridge, err := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	bridgePath := filepath.Join(root, "bridge.mjs")
	if err := os.WriteFile(bridgePath, bridge, 0600); err != nil {
		t.Fatal(err)
	}
	sdk := fmt.Sprintf(`export function query({ options }) {
  return {
    streamInput: async () => {}, interrupt: async () => ({ still_queued: [] }), close: () => {},
    async *[Symbol.asyncIterator]() {
      if (options.permissionMode !== 'dontAsk' || options.additionalDirectories?.length !== 0) throw Error('permissions');
      const entry = options.hooks?.PreToolUse?.[0];
      if (entry?.matcher !== '.*' || entry.hooks?.length !== 1) throw Error('hook');
      const hook = entry.hooks[0], root = options.cwd, outside = %q;
      for (const name of ['Edit', 'Write']) {
        for (const file_path of [outside + '/file', root + '/../outside/file', root + '/escape/file']) {
          const result = await hook({ tool_name: name, tool_input: { file_path } });
          if (result.hookSpecificOutput?.permissionDecision !== 'deny') throw Error('outside edit');
        }
        const allowed = await hook({ tool_name: name, tool_input: { file_path: root + '/inside/file' } });
        if (allowed.hookSpecificOutput?.permissionDecision !== 'deny') throw Error('native edit must be disabled');
      }
      yield { type: 'system', subtype: 'init', session_id: 'fake-session', model: 'test-model', capabilities: ['interrupt_receipt_v1'] };
      yield { type: 'result', modelUsage: { model: { inputTokens: 1, outputTokens: 1 } } };
    }
  };
}`, outside)
	sdkPath := filepath.Join(root, "sdk.mjs")
	if err := os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, bridgePath, sdkPath, "/bin/true", workspace)
	cmd.Stdin = strings.NewReader(`{"op":"start","prompt":"hello"}` + "\n")
	output, _ := cmd.Output() // The fake Query closes without a normal stop receipt.
	if !strings.Contains(string(output), `"kind":"session_started"`) ||
		!strings.Contains(string(output), `"kind":"usage"`) {
		t.Fatalf("Claude bridge path gate did not pass: %s", output)
	}
}

// Review case: the Claude bridge reports 4 cached tokens in session usage;
// run telemetry must carry the same 4 as cached deltas, not 0.
func TestClaudeRunTelemetryCarriesCachedTokens(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness = Claude
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := fakeVendorPath(t, "claude")
	node, sdk := claudeAdapterDependencies(t, path) // AEON-342: node and an installed SDK package
	a := NewClaudeAdapter(node, sdk, path, map[string]string{"account": home})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan AdapterEvent, 32)
	p, err := a.Start(ctx, r, func(ev AdapterEvent) { events <- ev })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(ctx); _ = p.Wait() }()
	var input, cached int64
	for {
		select {
		case ev := <-events:
			input += ev.InputTokensDelta
			cached += ev.CachedInputTokensDelta
			if ev.SessionUsage != nil && ev.SessionUsage.CachedInputTokens != nil {
				if input != 15 || *ev.SessionUsage.CachedInputTokens != 4 {
					t.Fatalf("fixture: input=%d session cached=%d", input, *ev.SessionUsage.CachedInputTokens)
				}
				if cached != 4 {
					t.Fatalf("session cached=4 but run cached=%d", cached)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("usage not received")
		}
	}
}

func TestClaudeCachedTotal(t *testing.T) {
	seven := int64(7)
	if got, ok := claudeCachedTotal(&seven, []claudeModelUsage{{Cached: 1}}); !ok || got != 7 {
		t.Fatalf("bridge total ignored: %d %t", got, ok)
	}
	if got, ok := claudeCachedTotal(nil, []claudeModelUsage{{Cached: 1}, {Cached: 3}}); !ok || got != 4 {
		t.Fatalf("model sum: %d %t", got, ok)
	}
	if _, ok := claudeCachedTotal(nil, nil); ok {
		t.Fatal("no evidence became zero cached tokens")
	}
	if _, ok := claudeCachedTotal(nil, []claudeModelUsage{{Cached: math.MaxInt64}, {Cached: 1}}); ok {
		t.Fatal("overflow accepted")
	}
}

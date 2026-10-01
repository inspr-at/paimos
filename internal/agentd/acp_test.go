// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type acpHealthAPI struct{ *fakeAPI }

func (*acpHealthAPI) Queued(context.Context) ([]Run, error) { return nil, nil }

func TestACPVersionOnlyProbeNeverEstablishesReadiness(t *testing.T) {
	for _, name := range []string{Gemini, OpenCode} {
		t.Run(name, func(t *testing.T) {
			// Any non-version command fails: these probes must never call a model
			// or read a vendor identity/credential store to guess sign-in.
			path := fakeScript(t, versionAnswer+"exit 9\n")
			a := &ACPAdapter{Harness: name, Path: path, Homes: map[string]string{"local": privateHome(t)}}
			if a.Probe(t.Context(), "local") {
				t.Error("version-only probe returned true")
			}
			if got := probeAccount(t.Context(), a, "local"); got.OK || got.Failure != "sign_in_unverified" {
				t.Errorf("version-only account probe: %+v", got)
			}
			s, api, _ := testSupervisor(t)
			s.api = &acpHealthAPI{api} // Health-only poll; no queued run launches.
			s.accounts[0].Harness = name
			s.adapters = map[string]Adapter{name: a}
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, now := range []time.Time{time.Now(), time.Now().Add(2 * time.Minute)} {
				v := s.lifecycleAt("", now)
				d := v.AccountStatuses["account"]
				if v.Ready || d.State != "blocked" || d.Reason != "sign_in_unverified" || s.accountAvailable("account") || v.LoginRequired {
					t.Errorf("version-only lifecycle: %+v", v)
				}
			}
			a.Path = filepath.Join(t.TempDir(), "missing")
			if got := probeAccount(t.Context(), a, "local"); got.OK || got.Failure != ProbeUnavailable {
				t.Errorf("missing launcher: %+v", got)
			}
		})
	}
}

// This fixture executable speaks vendor ACP frames and makes no model calls.
func TestACPVendorFixture(t *testing.T) {
	fixture := os.Getenv("AEON_ACP_FIXTURE")
	if fixture == "" {
		return
	}
	name, scenario, _ := strings.Cut(fixture, ":")
	if os.Getenv("AEON_TEST_INJECTION") != "" {
		os.Exit(7)
	}
	if name == Gemini && os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH") == "" {
		os.Exit(8)
	}
	enc := json.NewEncoder(os.Stdout)
	read := bufio.NewScanner(os.Stdin)
	var active json.RawMessage
	model := "gemini-2.5-pro"
	if name == OpenCode {
		model = "ollama/qwen3-coder"
	}
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(9)
		}
	}
	update := func(session, kind string) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": map[string]any{"sessionUpdate": kind, "used": 999, "size": 1000, "cost": map[string]any{"amount": 0.00002, "currency": "USD"}}}})
	}
	complete := func(id json.RawMessage, reason string) {
		usage := map[string]any{"inputTokens": 15, "outputTokens": 4, "cachedReadTokens": 5, "thoughtTokens": 3, "totalTokens": 19}
		if name == OpenCode {
			usage["inputTokens"], usage["cachedWriteTokens"], usage["totalTokens"] = 8, 2, 22
		}
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": reason, "usage": usage, "_meta": map[string]any{"quota": map[string]any{"model_usage": []map[string]string{{"model": model}}}}}})
	}
	for read.Scan() {
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params struct {
				Session string `json:"sessionId"`
				ID      string `json:"configId"`
				Value   string `json:"value"`
				Servers []struct {
					Type    string `json:"type"`
					Headers []struct{ Name, Value string }
				} `json:"mcpServers"`
			} `json:"params"`
		}
		if json.Unmarshal(read.Bytes(), &f) != nil {
			os.Exit(10)
		}
		result := any(map[string]any{})
		switch f.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1}
		case "session/new":
			if scenario == "tools" && (len(f.Params.Servers) != 1 || f.Params.Servers[0].Type != "http" || len(f.Params.Servers[0].Headers) != 1 || f.Params.Servers[0].Headers[0].Name != "Authorization") {
				os.Exit(11)
			}
			current := model
			if scenario == "model-mismatch" {
				current = "other-model"
			}
			result = map[string]any{"sessionId": "ses-fixture", "models": map[string]string{"currentModelId": current}}
		case "session/set_config_option":
			value := model
			if scenario == "model-mismatch" {
				value = "other-model"
			}
			result = map[string]any{"configOptions": []map[string]string{{"id": "model", "currentValue": value}, {"id": "effort", "currentValue": "default"}}}
		case "session/prompt":
			if f.Params.Session != "ses-fixture" {
				os.Exit(12)
			}
			if scenario == "stream-failed" {
				fmt.Fprintln(os.Stdout, "{")
				continue
			}
			if scenario == "permission" {
				send(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "session/request_permission", "params": map[string]string{"sessionId": "ses-fixture"}})
				continue
			}
			update("foreign-session", "tool_call")
			update("ses-fixture", "agent_message_chunk")
			if scenario == "tool-turn" {
				update("ses-fixture", "tool_call")
			}
			update("ses-fixture", "usage_update")
			update("ses-fixture", "usage_update")
			if scenario == "cancel" {
				active = f.ID
				continue
			}
			complete(f.ID, "end_turn")
			continue
		case "session/cancel":
			complete(active, "cancelled")
			continue
		case "":
			if scenario == "permission" && !strings.Contains(string(f.Result), "cancelled") {
				os.Exit(13)
			}
			continue
		default:
			os.Exit(14)
		}
		send(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": result})
	}
	os.Exit(0)
}

func acpFixturePath(t *testing.T, name, scenario string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "vendor")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then printf '1.14.48\\n'; exit 0; fi\nAEON_ACP_FIXTURE=%s:%s exec %q -test.run=^TestACPVendorFixture$\n", name, scenario, exe)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func acpFixtureHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestACPHarnessQualification(t *testing.T) {
	for _, name := range []string{Gemini, OpenCode} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("AEON_TEST_INJECTION", "must-not-inherit")
			home := acpFixtureHome(t)
			adapter := &ACPAdapter{Harness: name, Path: acpFixturePath(t, name, "tools"), Homes: map[string]string{"local": home}, IdleTimeout: time.Minute}
			req := StartRequest{AccountKey: "local", Workspace: home, Profile: Profile{Model: "gemini-2.5-pro", Effort: "16384"}, Prompt: "fixture", InboxEnabled: true, Tools: &RunTools{URL: "http://127.0.0.1:1234/mcp", Token: "fixture"}}
			if name == OpenCode {
				req.Profile.Model = "ollama/qwen3-coder"
				req.Profile.Effort = "default"
			}
			var mu sync.Mutex
			var events []AdapterEvent
			idle := make(chan struct{}, 4)
			observe := func(ev AdapterEvent) {
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
				if ev.Activity == "idle" {
					idle <- struct{}{}
				}
			}
			if !adapter.Probe(t.Context(), "local") || adapter.Probe(t.Context(), "unknown") {
				t.Fatal("probe binding")
			}
			if adapter.CanCaptureCapacity("local") || len(adapter.CaptureCapacity(t.Context(), "local")) != 0 {
				t.Fatal("quota invented")
			}
			proc, err := adapter.Start(t.Context(), req, observe)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = proc.Stop(context.Background()); _ = proc.Wait() })
			waitIdle := func() {
				select {
				case <-idle:
				case <-time.After(3 * time.Second):
					t.Fatal("idle timeout")
				}
			}
			waitIdle()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := proc.Control(ctx, "inbox", "leased message"); err != nil {
				t.Fatal(err)
			}
			waitIdle()
			mu.Lock()
			defer mu.Unlock()
			var input, output, cached, thought, cost, turns int64
			for _, ev := range events {
				input += ev.InputTokensDelta
				output += ev.OutputTokensDelta
				cached += ev.CachedInputTokensDelta
				thought += ev.ReasoningTokensDelta
				cost += ev.CostMicrosDelta
				turns += ev.TurnCountDelta
			}
			if input != 30 || output != 14 || cached != 10 || thought != 6 || cost != 20 || turns != 2 {
				t.Fatalf("counts %d/%d cache %d thought %d cost %d turns %d", input, output, cached, thought, cost, turns)
			}
			if err := proc.Control(ctx, "unknown", ""); !errors.Is(err, ErrUnsupported) {
				t.Fatal("unknown control", err)
			}
		})
	}
}

func TestACPFailureAndVerificationFences(t *testing.T) {
	for _, name := range []string{Gemini, OpenCode} {
		for _, scenario := range []string{"model-mismatch", "permission", "stream-failed"} {
			t.Run(name+"/"+scenario, func(t *testing.T) {
				home := acpFixtureHome(t)
				a := &ACPAdapter{Harness: name, Path: acpFixturePath(t, name, scenario), Homes: map[string]string{"local": home}}
				r := StartRequest{AccountKey: "local", Workspace: home, Profile: Profile{Model: "gemini-2.5-pro", Effort: "16384"}, Prompt: "fixture"}
				if name == OpenCode {
					r.Profile.Model = "ollama/qwen3-coder"
					r.Profile.Effort = "default"
				}
				p, err := a.Start(t.Context(), r, func(AdapterEvent) {})
				if scenario == "model-mismatch" {
					if err == nil {
						t.Fatal("model mismatch launched prompt")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- p.Wait() }()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("failed turn reported success")
					}
				case <-time.After(4 * time.Second):
					_ = p.Stop(context.Background())
					t.Fatal("failed stream retained process")
				}
			})
		}
		a := &ACPAdapter{Harness: name, Path: "/must/not/run"}
		r := StartRequest{Run: Run{Purpose: VerificationPurpose}}
		if _, err := a.Start(t.Context(), r, func(AdapterEvent) {}); !errors.Is(err, ErrVerificationUnavailable) {
			t.Fatal("verification was not blocked before child launch", name, err)
		}
	}
}

func TestACPInterruptAndIncompleteOpenCodeTurn(t *testing.T) {
	for _, tc := range []struct{ name, scenario string }{{Gemini, "cancel"}, {OpenCode, "cancel"}, {OpenCode, "tool-turn"}} {
		t.Run(tc.name+"/"+tc.scenario, func(t *testing.T) {
			home := acpFixtureHome(t)
			a := &ACPAdapter{Harness: tc.name, Path: acpFixturePath(t, tc.name, tc.scenario), Homes: map[string]string{"local": home}}
			r := StartRequest{AccountKey: "local", Workspace: home, Profile: Profile{Model: "gemini-2.5-pro", Effort: "16384"}, Prompt: "fixture"}
			if tc.name == OpenCode {
				r.Profile.Model = "ollama/qwen3-coder"
				r.Profile.Effort = "default"
			}
			accepted := make(chan struct{}, 1)
			var mu sync.Mutex
			var input, cost int64
			p, err := a.Start(t.Context(), r, func(ev AdapterEvent) {
				mu.Lock()
				input += ev.InputTokensDelta
				cost += ev.CostMicrosDelta
				mu.Unlock()
				if ev.Kind == "usage" && ev.CostMicrosDelta > 0 {
					accepted <- struct{}{}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.scenario == "cancel" {
				select {
				case <-accepted:
				case <-time.After(3 * time.Second):
					t.Fatal("turn not accepted")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				if err := p.Control(ctx, "interrupt", ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.Wait(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if cost != 20 || tc.scenario == "tool-turn" && input != 0 {
				t.Fatal("incomplete throughput guessed", input, cost)
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

// Qualification is test-only and explicitly synthetic. This does not add an
// approved production executable or change agentverification.For.
func qualifyCodexFixture(t *testing.T, a *CodexAdapter) {
	t.Helper()
	raw, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	a.idleUsage = loadCodexCapacityPin([]capacityPin{*fixtureCapacityPin(Codex, a.Path, raw, "synthetic-fixture")})
	if a.idleUsage == nil {
		t.Fatal("synthetic fixture pin rejected")
	}
}

func TestCodexUnqualifiedIdleNeverExecutesStartupHook(t *testing.T) {
	home := privateCapacityHome(t)
	path := filepath.Join(home, "codex")
	sentinel := filepath.Join(home, "startup-sentinel")
	raw := []byte(fmt.Sprintf("#!/bin/sh\nprintf invoked > %q\nexit 89\n", sentinel))
	if err := os.WriteFile(path, raw, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(path, map[string]string{"local": home})
	a.SetExpectedEmails(map[string]string{"local": "fixture@example.test"})
	if got := a.CaptureCapacityResult(t.Context(), "local"); got.Result != "unsupported" || a.CanCaptureCapacity("local") {
		t.Fatal("production capability was invented")
	}
	qualifyCodexFixture(t, a)
	original := *a.idleUsage
	for _, boundary := range []string{"startup", "config", "termination", "binary", "node"} {
		t.Run(boundary, func(t *testing.T) {
			c := original
			switch boundary {
			case "startup":
				c.startupHooks = false
			case "config":
				c.inheritedConfig = false
			case "termination":
				c.termination = false
			case "binary":
				c.binarySHA256 = strings.Repeat("0", 64)
			case "node":
				c.nodeSHA256 = strings.Repeat("0", 64)
			}
			a.idleUsage = &c
			if got := a.CaptureCapacityResult(t.Context(), "local"); got.Result != "unsupported" {
				t.Fatal("unqualified idle launch", got.Result)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatal("unqualified startup hook executed")
			}
		})
	}
}

func TestFakeCodexIdleProcess(t *testing.T) {
	if os.Getenv("AEON_FAKE_IDLE_CHECK") != "1" {
		return
	}
	runFakeCodexIdleProcess()
}

func runFakeCodexIdleProcess() {
	home := os.Getenv("CODEX_HOME")
	if home == "" || os.Getenv("HOME") != home || os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("NODE_OPTIONS") != "" || os.Getenv("NODE_PATH") != "" {
		os.Exit(10)
	}
	trace, err := os.OpenFile(filepath.Join(home, "methods"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(11)
	}
	defer trace.Close()
	scanner := bufio.NewScanner(os.Stdin)
	expected := []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}
	step := 0
	for scanner.Scan() {
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &f) != nil || step >= len(expected) || f.Method != expected[step] {
			os.Exit(12)
		}
		_, _ = fmt.Fprintln(trace, f.Method)
		step++
		if f.Method == "initialized" {
			continue
		}
		var result any = map[string]any{}
		if f.Method == "account/read" {
			result = map[string]any{"account": map[string]string{"id": "synthetic-id", "type": "chatgpt", "email": "fixture@example.test"}}
		}
		if f.Method == "account/rateLimits/read" {
			result = map[string]any{"rateLimits": map[string]any{"primary": map[string]any{"usedPercent": 12, "windowDurationMins": 300, "resetsAt": time.Now().Add(time.Hour).Unix()}}}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": result})
	}
	os.Exit(0)
}

// Risk: an enrolled wrapper or Node intercepts the idle exchange, or loader
// overrides reach the separately pinned native process.
func TestCodexIdleUsesPinnedNativeAndOnlyFourMethods(t *testing.T) {
	home := privateCapacityHome(t)
	a := NewCodexAdapter(refusingCapacityExecutable(t, home, "wrapper"), map[string]string{"local": home})
	a.Nodes = map[string]harnesslaunch.Node{"local": {Path: refusingCapacityExecutable(t, home, "node"), Version: "fixture"}}
	a.SetExpectedEmails(map[string]string{"local": "fixture@example.test"})
	pin := nativeCapacityFixture(t)
	a.idleUsage = loadCodexCapacityPin([]capacityPin{pin})
	t.Setenv("OPENAI_API_KEY", "synthetic-parent-key")
	t.Setenv("NODE_OPTIONS", "synthetic-startup-hook")
	t.Setenv("NODE_PATH", "synthetic-parent-tools")
	got := a.CaptureCapacityResult(t.Context(), "local")
	if got.Result != "success" || got.CleanupUnconfirmed || len(got.Readings) != 1 {
		t.Fatal("qualified synthetic exchange failed", got.Result, got.CleanupUnconfirmed)
	}
	trace, err := os.ReadFile(filepath.Join(home, "methods"))
	if err != nil || string(trace) != "initialize\ninitialized\naccount/read\naccount/rateLimits/read\n" {
		t.Fatal("unexpected protocol or prompt/thread/MCP call")
	}
	assertNativeCapacityLaunch(t, home, pin.Path)
	a.Emails["local"] = "other@example.test"
	got = a.CaptureCapacityResult(t.Context(), "local")
	if got.Result != "identity_mismatch" || len(got.Readings) != 0 || got.CleanupUnconfirmed {
		t.Fatal("mismatched binding lost hard failure", got.Result)
	}
	trace, err = os.ReadFile(filepath.Join(home, "methods"))
	if err != nil || string(trace) != "initialize\ninitialized\naccount/read\naccount/rateLimits/read\ninitialize\ninitialized\naccount/read\n" {
		t.Fatal("quota was read before identity matched")
	}
}

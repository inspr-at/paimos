// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// Risk: a version label or protocol fixture silently enables a different
// executable, interpreter, startup policy or unconfirmed cleanup. These are
// synthetic files and an in-process exchange, never a live model CLI call.
func TestClaudeIdleQualificationRefusesUnknownDigestAndBoundaries(t *testing.T) {
	home := privateCapacityHome(t)
	bin := privateCapacityHome(t)
	raw := []byte("#!/bin/sh\nexit 91\n")
	path, node := filepath.Join(bin, "claude"), filepath.Join(bin, "node")
	for _, p := range []string{path, node} {
		if err := os.WriteFile(p, raw, 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := NewClaudeAdapter(refusingCapacityExecutable(t, home, "node-wrapper"), "", refusingCapacityExecutable(t, home, "claude-wrapper"), map[string]string{"one": home})
	calls := 0
	secret := "sk-ant-synthetic-not-for-errors-1499"
	c := claudeUsageCapability{binaryPath: path, binarySHA256: sha256Hex(raw), nodePath: node, nodeSHA256: sha256Hex(raw), version: "synthetic-fixture", startupHooks: true, inheritedConfig: true, termination: true,
		pin: fixtureCapacityPin(Claude, path, raw, "synthetic-fixture", capacityHelperPin{Name: "node", Path: node, SHA256: sha256Hex(raw)}),
		capture: func(ctx context.Context, executable, selected string, environment []string) (json.RawMessage, error) {
			calls++
			if selected != home || executable != path {
				t.Fatal("cross-account profile or wrong capacity executable")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded capture")
			}
			for _, entry := range environment {
				if strings.HasPrefix(entry, "NODE_OPTIONS=") || strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") || strings.HasPrefix(entry, "CLAUDE_CODE_OAUTH_TOKEN=") {
					t.Fatal("inherited credential or loader override")
				}
			}
			return json.RawMessage(`{"used":24,"access_token":"` + secret + `"}`), nil
		}, decode: func(raw json.RawMessage, now time.Time) []capacity.Reading {
			var v struct {
				Used float64 `json:"used"`
			}
			if json.Unmarshal(raw, &v) != nil {
				return nil
			}
			return []capacity.Reading{{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: v.Used, ResetsAt: now.Add(time.Hour)}}
		}}
	for _, boundary := range []string{"no-entry", "version", "executable", "interpreter", "startup", "config", "termination"} {
		t.Run(boundary, func(t *testing.T) {
			candidate := c
			a.usage = &candidate
			switch boundary {
			case "no-entry":
				a.usage = nil
			case "version":
				candidate.version = "different-version"
			case "executable":
				candidate.binarySHA256 = strings.Repeat("0", 64)
			case "interpreter":
				candidate.nodeSHA256 = strings.Repeat("0", 64)
			case "startup":
				candidate.startupHooks = false
			case "config":
				candidate.inheritedConfig = false
			case "termination":
				candidate.termination = false
			}
			if out := a.CaptureCapacityResult(t.Context(), "one"); out.Result != "unsupported" || len(out.Readings) != 0 || calls != 0 {
				t.Fatal("unqualified idle exchange was reached", out.Result)
			}
		})
	}
	a.usage = &c
	t.Setenv("ANTHROPIC_API_KEY", secret)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", secret)
	t.Setenv("NODE_OPTIONS", "synthetic-loader")
	out := a.CaptureCapacityResult(t.Context(), "one")
	if out.Result != "success" || len(out.Readings) != 1 || out.Readings[0].Source != "agentd" || out.Readings[0].UsedPercent != 24 {
		t.Fatal("synthetic qualified measurement failed")
	}
	serialized, err := json.Marshal(out)
	if err != nil || strings.Contains(string(serialized), secret) {
		t.Fatal("raw vendor fields escaped normalized capture")
	}
	assertNoCapacitySpawn(t, home)
	c.capture = func(context.Context, string, string, []string) (json.RawMessage, error) {
		return nil, errors.Join(ownedprocess.ErrCleanupUnconfirmed, errors.New(secret))
	}
	out = a.CaptureCapacityResult(t.Context(), "one")
	if out.Result != "launch_failed" || !out.CleanupUnconfirmed || len(out.Readings) != 0 {
		t.Fatal("unconfirmed cleanup released the capture fence")
	}
	serialized, _ = json.Marshal(out)
	if strings.Contains(string(serialized), secret) {
		t.Fatal("vendor error leaked credential bytes")
	}
}

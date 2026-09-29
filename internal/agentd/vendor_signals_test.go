// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/internal/capacity"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQuotaFingerprintStableSeparatedAndPrivate(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	want := QuotaFingerprint(key, Codex, "vendor-user-42")
	if len(want) != 64 || want != QuotaFingerprint(append([]byte(nil), key...), Codex, "vendor-user-42") {
		t.Fatal("same tenant login differs between daemons")
	}
	seen := map[string]bool{want: true}
	for i := 0; i < 1000; i++ {
		v := QuotaFingerprint(key, Codex, fmt.Sprintf("login-%d", i))
		if seen[v] {
			t.Fatal("fingerprint collision")
		}
		seen[v] = true
	}
	if want == QuotaFingerprint(bytes.Repeat([]byte{8}, 32), Codex, "vendor-user-42") || want == QuotaFingerprint(key, Claude, "vendor-user-42") {
		t.Fatal("cross-tenant/vendor linkage")
	}
	for _, v := range []string{"", "user@example.test", "/private/home", "Bearer token"} {
		if QuotaFingerprint(key, Codex, v) != "" {
			t.Fatal("invalid ID accepted")
		}
	}
	if QuotaFingerprint(nil, Codex, "42") != "" {
		t.Fatal("unkeyed fingerprint")
	}
}

func TestStatuslineRateLimitAndAccountFence(t *testing.T) {
	s, base, _ := testSupervisor(t)
	api := &capacityTestAPI{API: base}
	s.api = api
	s.accounts = []EnrolledAccount{{ID: "claude", Key: "a", Harness: Claude}, {ID: "codex", Key: "b", Harness: Codex}}
	now := time.Now().UTC()
	r := capacity.Reading{WindowKind: "5h", Bucket: "five_hour", WindowMinutes: 300, UsedPercent: 42, ResetsAt: now.Add(time.Hour), ReadAt: now, Source: "harness", Phase: "update"}
	req := StatuslineRequest{AccountID: "claude", Readings: []capacity.Reading{r}}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ReportStatusline(t.Context(), req, now); err != nil {
				t.Error("statusline refused")
			}
		}()
	}
	wg.Wait()
	if len(api.got) != 1 {
		t.Fatal("more than one report per minute")
	}
	// A new in-memory supervisor using the same private state is still limited.
	restarted := &Supervisor{api: api, state: s.state, tenantID: s.tenantID, principalID: s.principalID, daemonID: s.daemonID, accounts: s.accounts}
	if out, err := restarted.ReportStatusline(t.Context(), req, now.Add(59*time.Second)); err != nil || out.Reported {
		t.Fatal("restart/59s bypassed rate limit")
	}
	req.Readings[0].ReadAt = now.Add(time.Minute)
	if out, err := s.ReportStatusline(t.Context(), req, now.Add(time.Minute)); err != nil || !out.Reported {
		t.Fatal("60s reading missing")
	}
	for _, id := range []string{"foreign", "codex"} {
		req.AccountID = id
		if _, err := s.ReportStatusline(t.Context(), req, now.Add(time.Minute)); err == nil {
			t.Fatal("cross-account statusline")
		}
	}
	req.AccountID = "claude"
	req.Readings[0].Plan = "private-value"
	if _, err := s.ReportStatusline(t.Context(), req, now.Add(time.Minute)); err == nil {
		t.Fatal("extra data accepted")
	}
}

func TestClaudeStatuslineSettingsConsentAndPreservation(t *testing.T) {
	home := privateCapacityHome(t)
	path := filepath.Join(home, "settings.json")
	command := "aeon statusline --account-id fixture"
	if err := applyClaudeStatusline(home, command, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("settings created without consent")
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark","permissions":{"allow":["Read"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyClaudeStatusline(home, command, true); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["theme"] != "dark" || got["permissions"] == nil || got["statusLine"] == nil {
		t.Fatal("settings not preserved")
	}
	if err := applyClaudeStatusline(home, command, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "statusLine") {
		t.Fatal("owned statusline retained")
	}
	foreign := []byte(`{"statusLine":{"type":"command","command":"other-tool"}}`)
	_ = os.WriteFile(path, foreign, 0600)
	if applyClaudeStatusline(home, command, true) == nil {
		t.Fatal("overwrote user statusline")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, foreign) {
		t.Fatal("foreign settings changed")
	}
	// File fences: neither symbolic nor hard links can cause a settings write.
	for _, link := range []string{"symlink", "hardlink"} {
		t.Run(link, func(t *testing.T) {
			root := privateCapacityHome(t)
			target := filepath.Join(root, "settings.json")
			var err error
			if link == "symlink" {
				err = os.Symlink(path, target)
			} else {
				err = os.Link(path, target)
			}
			if err != nil {
				t.Fatal(err)
			}
			if applyClaudeStatusline(root, command, true) == nil {
				t.Fatal("linked settings accepted")
			}
		})
	}
}

func TestClaudeIdleCapabilityExactBinary(t *testing.T) {
	home := privateCapacityHome(t)
	path := filepath.Join(home, "fixture-claude")
	raw := []byte("#!/bin/sh\nexit 88\n")
	_ = os.WriteFile(path, raw, 0700)
	a := NewClaudeAdapter("/fixture/node", "/fixture/sdk", path, map[string]string{"a": home})
	if a.CanCaptureCapacity("a") || len(a.CaptureCapacity(t.Context(), "a")) != 0 {
		t.Fatal("unsupported Claude captured")
	}
	calls := 0
	a.usage = &claudeUsageCapability{binaryPath: path, binarySHA256: sha256Hex(raw), version: "fixture-1", capture: func(_ context.Context, _ string, environment []string) (json.RawMessage, error) {
		calls++
		if len(environment) != 5 {
			t.Fatal("child env expanded")
		}
		return []byte(`{}`), nil
	}, decode: func(_ json.RawMessage, now time.Time) []capacity.Reading {
		return []capacity.Reading{{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 12, ResetsAt: now.Add(time.Hour)}}
	}}
	if len(a.CaptureCapacity(t.Context(), "a")) != 1 || calls != 1 {
		t.Fatal("verified fixture did not capture")
	}
	_ = os.WriteFile(path, []byte("#!/bin/sh\nexit 89\n"), 0700)
	if a.CanCaptureCapacity("a") || len(a.CaptureCapacity(t.Context(), "a")) != 0 || calls != 1 {
		t.Fatal("changed binary launched")
	}
}

func TestCodexVendorLimitSettlesRunAndCapacity(t *testing.T) {
	s, base, _ := testSupervisor(t)
	api := &capacityTestAPI{API: base}
	s.api = api
	if err := s.StartRun(t.Context(), base.run); err != nil {
		t.Fatal(err)
	}
	e := s.runs[base.run.ID]
	parser := &codexProcess{wireProcess: &wireProcess{observe: func(ev AdapterEvent) { s.observe(e, ev) }}}
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	parser.notification(json.RawMessage(fmt.Sprintf(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":41,"windowDurationMins":300,"resetsAt":%d}},"ordinaryUsageAllowed":false}}`, reset.Unix())))
	awaitTelemetryMonitor(t, e)
	base.mu.Lock()
	last := base.reports[len(base.reports)-1]
	base.mu.Unlock()
	if last.Kind != "finished" || last.ErrorCode != "vendor_limit" || last.Status != "failed" || last.LimitResetsAt == nil || !last.LimitResetsAt.Equal(reset) {
		t.Fatal("vendor limit did not settle run")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.got) == 0 || api.got[0].UsedPercent != 100 || api.got[0].RunID != base.run.ID || api.got[0].Source != "harness" {
		t.Fatal("100% harness reading missing")
	}
}

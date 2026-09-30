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
	s.statuslineEnabled = map[string]bool{"claude": true}
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
	restarted := &Supervisor{api: api, state: s.state, tenantID: s.tenantID, principalID: s.principalID, daemonID: s.daemonID, accounts: s.accounts, statuslineEnabled: map[string]bool{"claude": true}}
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

func TestStatuslineReportRequiresConsent(t *testing.T) {
	s, base, _ := testSupervisor(t)
	s.api = &capacityTestAPI{API: base}
	s.accounts = []EnrolledAccount{{ID: "claude", Key: "a", Harness: Claude}}
	now := time.Now().UTC()
	req := StatuslineRequest{AccountID: "claude", Readings: []capacity.Reading{{WindowKind: "5h", Bucket: "five_hour", WindowMinutes: 300, UsedPercent: 42, ResetsAt: now.Add(time.Hour), ReadAt: now, Source: "harness", Phase: "update"}}}
	if _, err := s.ReportStatusline(t.Context(), req, now); err == nil {
		t.Fatal("missing consent reported")
	}
	s.statuslineEnabled = map[string]bool{"claude": false}
	if _, err := s.ReportStatusline(t.Context(), req, now); err == nil {
		t.Fatal("consent off reported")
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

func TestClaudeStatuslineFollowsBinaryPath(t *testing.T) {
	home := privateCapacityHome(t)
	account := "11111111-1111-4111-8111-111111111111"
	old := aeonStatuslineCommand("/nix/store/old-aeon/bin/aeon", home, account)
	next := aeonStatuslineCommand("/nix/store/new-aeon/bin/aeon", home, account)
	if err := applyClaudeStatusline(home, old, true); err != nil {
		t.Fatal(err)
	}
	if err := applyClaudeStatusline(home, next, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if err != nil || !strings.Contains(string(raw), "/nix/store/new-aeon/bin/aeon") || strings.Contains(string(raw), "/nix/store/old-aeon/bin/aeon") {
		t.Fatal(string(raw))
	}
	if err := applyClaudeStatusline(home, next, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustRead(t, filepath.Join(home, "settings.json")), "statusLine") {
		t.Fatal("path change left Aeon's status line")
	}
	foreign := []byte(`{"statusLine":{"type":"command","command":"other-tool"}}`)
	if err := os.WriteFile(filepath.Join(home, "settings.json"), foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if applyClaudeStatusline(home, next, false) == nil {
		t.Fatal("removed a status line Aeon did not install")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExactCapacityBinaryHashCache(t *testing.T) {
	home := privateCapacityHome(t)
	path := filepath.Join(home, "fixture-claude")
	raw := []byte("#!/bin/sh\nexit 88\n")
	if err := os.WriteFile(path, raw, 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256Hex(raw)
	before := capacityHashCount()
	if !exactCapacityBinary(path, digest) || !exactCapacityBinary(path, digest) {
		t.Fatal("fixture hash rejected")
	}
	if capacityHashCount()-before != 1 {
		t.Fatalf("hashed %d times", capacityHashCount()-before)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho bigger\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if exactCapacityBinary(path, digest) {
		t.Fatal("stale hash accepted")
	}
	if capacityHashCount()-before != 2 {
		t.Fatalf("size change hashed %d times", capacityHashCount()-before)
	}
}

func capacityHashCount() int {
	capacityBinaryCache.Lock()
	defer capacityBinaryCache.Unlock()
	return capacityBinaryCache.hashes
}

func TestExactCapacityBinaryRejectsRestoredMtime(t *testing.T) {
	for _, rename := range []bool{false, true} {
		t.Run(fmt.Sprintf("rename=%t", rename), func(t *testing.T) {
			path := filepath.Join(privateCapacityHome(t), "fixture-binary")
			original := []byte("#!/bin/sh\nexit 88\n")
			changed := []byte("#!/bin/sh\nexit 89\n")
			if err := os.WriteFile(path, original, 0700); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !exactCapacityBinary(path, sha256Hex(original)) {
				t.Fatal("original rejected")
			}
			before := capacityHashCount()
			// Allow the filesystem clock to advance before the in-place rewrite.
			time.Sleep(2 * time.Millisecond)
			target := path
			if rename {
				target += ".replacement"
			}
			if err := os.WriteFile(target, changed, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if rename {
				if err := os.Rename(target, path); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.Stat(path)
			if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
				t.Fatal("fixture did not preserve size and mtime")
			}
			if exactCapacityBinary(path, sha256Hex(original)) || !exactCapacityBinary(path, sha256Hex(changed)) {
				t.Fatal("replacement reused the approved hash")
			}
			if capacityHashCount()-before != 1 {
				t.Fatal("replacement was not hashed once and cached")
			}
		})
	}
}

type statuslineConsentTestAPI struct{ API }

func (statuslineConsentTestAPI) StatuslineConsent(context.Context, string) (StatuslineConsent, error) {
	return StatuslineConsent{Enabled: false}, nil
}

func TestStatuslineOffWithoutExecutable(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid-executable=%t", invalid), func(t *testing.T) {
			s, base, _ := testSupervisor(t)
			s.api = statuslineConsentTestAPI{base}
			home := privateCapacityHome(t)
			s.accounts = []EnrolledAccount{{ID: "claude", Key: "local", Harness: Claude}}
			s.adapters[Claude] = NewClaudeAdapter("/unused", "/unused", "/unused", map[string]string{"local": home})
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			if invalid {
				if err := os.WriteFile(filepath.Join(bin, "aeon"), []byte("invalid fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			old := aeonStatuslineCommand("/old install/Markus's tools/aeon", s.state.Path(), "claude")
			if err := applyClaudeStatusline(home, old, true); err != nil {
				t.Fatal(err)
			}
			s.statuslineEnabled = map[string]bool{"claude": true}
			s.syncStatuslines(t.Context(), time.Now().UTC())
			if s.statuslineEnabled["claude"] || strings.Contains(mustRead(t, filepath.Join(home, "settings.json")), "statusLine") {
				t.Fatal("off-switch retained the owned entry or consent")
			}
			foreign := aeonStatuslineCommand("/old install/aeon", s.state.Path(), "other-account")
			if err := applyClaudeStatusline(home, foreign, true); err != nil {
				t.Fatal(err)
			}
			before := mustRead(t, filepath.Join(home, "settings.json"))
			s.syncStatuslines(t.Context(), time.Now().UTC())
			if mustRead(t, filepath.Join(home, "settings.json")) != before {
				t.Fatal("off-switch changed another account's entry")
			}
		})
	}
}

func TestStatuslineQuotedExecutableOwnership(t *testing.T) {
	wanted := aeonStatuslineCommand("", "/private/state dir", "account")
	for _, binary := range []string{"/old install/aeon", "/Markus's tools/aeon", "/tools statusline --state-dir /aeon"} {
		if !claudeStatuslineOwned(aeonStatuslineCommand(binary, "/private/state dir", "account"), wanted) {
			t.Fatal("quoted executable not recognized")
		}
	}
	for _, foreign := range []string{
		"'other'; " + aeonStatuslineCommand("/aeon", "/private/state dir", "account"),
		aeonStatuslineCommand("/aeon", "/other state", "account"),
		aeonStatuslineCommand("/aeon", "/private/state dir", "other-account"),
		aeonStatuslineCommand("/aeon", "/private/state dir", "account") + " && other-tool",
	} {
		if claudeStatuslineOwned(foreign, wanted) {
			t.Fatal("foreign command treated as owned")
		}
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
	if last.Kind != "finished" || last.ErrorCode != "vendor_limit" || last.Status != "failed" || last.LimitResetsAt != nil {
		t.Fatal("vendor limit did not settle run")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.got) == 0 || api.got[0].UsedPercent != 41 || api.got[0].RunID != base.run.ID || api.got[0].Source != "harness" {
		t.Fatal("unnamed stop rewrote the vendor percentage")
	}
}

func TestCodexVendorLimitAtStartReturnsObservedProcess(t *testing.T) {
	for _, vendor := range []string{"codex_limited", "codex_limit_error"} {
		t.Run(vendor, func(t *testing.T) {
			req := adapterRequest(t)
			a := NewCodexAdapter(fakeVendorPath(t, vendor), map[string]string{"account": privateCapacityHome(t)})
			a.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
			var mu sync.Mutex
			var hits int
			proc, err := a.Start(t.Context(), req, func(e AdapterEvent) {
				mu.Lock()
				defer mu.Unlock()
				if e.ErrorCode == "vendor_limit" {
					hits++
				}
			})
			if err != nil || proc == nil {
				t.Fatal("vendor refusal lost its owned process")
			}
			_ = proc.Wait()
			mu.Lock()
			defer mu.Unlock()
			if hits == 0 || !proc.(interface{ ProcessExited() bool }).ProcessExited() {
				t.Fatal("limit did not prove owned exit")
			}
		})
	}
}

func TestCodexVendorLimitNotificationBinding(t *testing.T) {
	hits := 0
	p := &codexProcess{wireProcess: &wireProcess{threadID: "owned", turnID: "turn", observe: func(e AdapterEvent) {
		if e.VendorLimit != nil {
			hits++
		}
	}}, acknowledged: true}
	for _, thread := range []string{"foreign", "owned"} {
		p.notification(json.RawMessage(fmt.Sprintf(`{"method":"error","params":{"threadId":%q,"turnId":"turn","error":{"codexErrorInfo":"usageLimitExceeded"}}}`, thread)))
		if thread == "foreign" && hits != 0 {
			t.Fatal("foreign thread stopped run")
		}
	}
	if hits != 1 {
		t.Fatal("owned limit missing")
	}
}

func TestCodexRealMidrunLimitThroughAdapter(t *testing.T) {
	for _, vendor := range []string{"codex_midrun_limit", "codex_midrun_foreign_thread", "codex_midrun_foreign_turn"} {
		t.Run(vendor, func(t *testing.T) {
			a := NewCodexAdapter(fakeVendorPath(t, vendor), map[string]string{"account": privateCapacityHome(t)})
			a.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
			var mu sync.Mutex
			var hits []*capacity.LimitHit
			proc, err := a.Start(t.Context(), adapterRequest(t), func(e AdapterEvent) {
				mu.Lock()
				defer mu.Unlock()
				if e.VendorLimit != nil {
					hits = append(hits, e.VendorLimit)
				}
				wire, _ := json.Marshal(e)
				if strings.Contains(string(wire), "PRIVATE_LIMIT_FIXTURE") {
					t.Error("raw vendor error escaped")
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = proc.Stop(context.Background()) })
			if proc.(*codexProcess).vendorLimited.Load() {
				t.Fatal("fixture did not start a normal run")
			}
			if err := proc.Control(t.Context(), "steer", "fixture trigger"); err != nil {
				t.Fatal(err)
			}
			err = proc.Wait()
			limited := vendor == "codex_midrun_limit"
			wantHits := 0
			if limited {
				wantHits = 2 // The error notification and failed turn both carry the real error variant.
			}
			mu.Lock()
			defer mu.Unlock()
			if (err != nil) != limited || len(hits) != wantHits || !proc.(interface{ ProcessExited() bool }).ProcessExited() {
				t.Fatalf("mid-run binding/settlement failed: error=%v hits=%d", err, len(hits))
			}
			for _, hit := range hits {
				if hit.Window != "" || hit.ResetsAt != nil || len(hit.Readings) != 0 {
					t.Fatal("mid-run error invented window bounds")
				}
			}
		})
	}
}

func TestCodexNotificationScopesLimitsToCurrentModel(t *testing.T) {
	hits := 0
	p := &codexProcess{capacityModel: "model-a", wireProcess: &wireProcess{threadID: "owned", observe: func(e AdapterEvent) {
		if e.VendorLimit != nil {
			hits++
		}
	}}}
	raw := json.RawMessage(`{"method":"account/rateLimits/updated","params":{"rateLimitsByLimitId":{"model-a":{"rateLimitReachedType":null},"model-b":{"rateLimitReachedType":"rate_limit_reached"}}}}`)
	p.notification(raw)
	p.notification(json.RawMessage(`{"method":"thread/settings/updated","params":{"threadId":"foreign","threadSettings":{"model":"model-b"}}}`))
	p.notification(raw)
	if hits != 0 {
		t.Fatal("unrelated model stopped run")
	}
	p.notification(json.RawMessage(`{"method":"thread/settings/updated","params":{"threadId":"owned","threadSettings":{"model":"model-b"}}}`))
	p.notification(raw)
	if hits != 1 {
		t.Fatal("current model stop missing")
	}
}

func TestCodexVendorLimitWaitsForTurnAcknowledgement(t *testing.T) {
	for _, turn := range []string{"expected", "foreign"} {
		t.Run(turn, func(t *testing.T) {
			f := newCodexLifecycleFixture(t)
			f.emit(t, fmt.Sprintf(`{"method":"error","params":{"threadId":"synthetic-thread","turnId":%q,"error":{"codexErrorInfo":"usageLimitExceeded"}}}`, turn))
			if f.proc.vendorLimited.Load() {
				t.Fatal("limit preceded turn acknowledgement")
			}
			if err := f.ack(t, `"result":{"turn":{"id":"expected","status":"inProgress"}}`); err != nil {
				t.Fatal(err)
			}
			if f.proc.vendorLimited.Load() != (turn == "expected") {
				t.Fatal("turn binding failed")
			}
		})
	}
}

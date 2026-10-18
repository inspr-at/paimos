// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/capacity"
)

func privateCapacityHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	return home
}

// This is deliberately a synthetic schema, not a claim about Grok's fields.
func TestFakeBillingProcess(t *testing.T) {
	if os.Getenv("AEON_FAKE_BILLING") != "1" {
		return
	}
	home := os.Getenv("GROK_HOME")
	if home == "" || os.Getenv("HOME") != home || os.Getenv("GROK_AUTH_PATH") != "" || os.Getenv("XAI_API_KEY") != "" {
		os.Exit(3)
	}
	scanner := bufio.NewScanner(os.Stdin)
	count := 0
	for scanner.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			os.Exit(4)
		}
		count++
		if count == 1 && frame.Method != "initialize" || count == 2 && frame.Method != "x.ai/billing" || count > 2 {
			os.Exit(5)
		}
		f, err := os.OpenFile(filepath.Join(home, "calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(6)
		}
		_, _ = fmt.Fprintln(f, frame.Method)
		_ = f.Close()
		var result any = map[string]any{}
		if frame.Method == "x.ai/billing" {
			result = map[string]any{"fixture_identity": "fixture-principal", "fixture_used": 31, "ignored": "must-not-escape"}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": result})
	}
	os.Exit(0)
}

func billingFixture(t *testing.T, home string) *GrokAdapter {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateCapacityHome(t), "fake-grok")
	script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = agent ] && [ \"$2\" = stdio ] || exit 9\nAEON_FAKE_BILLING=1 exec %q -test.run=^TestFakeBillingProcess$\n", exe)
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := NewGrokAdapter(map[string]GrokBinding{"a": {BinaryPath: path, PrincipalSHA256: "fixture-principal"}})
	a.Homes = map[string]string{"a": home}
	a.billing = &grokBillingCapability{binaryPath: path, binarySHA256: sha256Hex([]byte(script)), version: "fixture-1", decode: func(raw json.RawMessage, now time.Time, identity string) []capacity.Reading {
		var v struct {
			Identity string   `json:"fixture_identity"`
			Used     *float64 `json:"fixture_used"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Identity != identity || v.Used == nil {
			return nil
		}
		return []capacity.Reading{{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: *v.Used, ResetsAt: now.Add(time.Hour)}}
	}}
	return a
}

func TestGrokBillingCapabilityFence(t *testing.T) {
	home := privateCapacityHome(t)
	a := billingFixture(t, home)
	supported := a.billing
	a.billing = nil
	if a.CapacitySupport("a") != "not available: Grok billing capability unverified" || len(a.CaptureCapacity(t.Context(), "a")) != 0 {
		t.Fatal("unverified billing enabled")
	}
	if _, err := os.Stat(filepath.Join(home, "calls")); !os.IsNotExist(err) {
		t.Fatal("unverified CLI launched")
	}
	a.billing = supported
	t.Setenv("GROK_AUTH_PATH", "parent-auth-must-not-inherit")
	t.Setenv("XAI_API_KEY", "fixture-parent-value")
	rs := a.CaptureCapacity(t.Context(), "a")
	if len(rs) != 1 || rs[0].Source != "agentd" || rs[0].UsedPercent != 31 || rs[0].ReadAt.IsZero() {
		t.Fatal("verified fixture not captured")
	}
	calls, err := os.ReadFile(filepath.Join(home, "calls"))
	if err != nil || string(calls) != "initialize\nx.ai/billing\n" {
		t.Fatal("unexpected protocol sequence")
	}
	b := a.Bindings["a"]
	b.PrincipalSHA256 = "different-account"
	a.Bindings["a"] = b
	if len(a.CaptureCapacity(t.Context(), "a")) != 0 {
		t.Fatal("cross-account quota")
	}
	b.BinaryPath = "/unverified-executable"
	a.Bindings["a"] = b
	if a.billingSupported("a") {
		t.Fatal("capability crossed executable binding")
	}
	if len(a.CaptureCapacity(t.Context(), "missing")) != 0 {
		t.Fatal("unknown account accepted")
	}
}

func TestGrokCaptureRejectsInvalidReadingAndCancellation(t *testing.T) {
	a := billingFixture(t, privateCapacityHome(t))
	a.billing.decode = func(json.RawMessage, time.Time, string) []capacity.Reading {
		return []capacity.Reading{{UsedPercent: 101}}
	}
	if len(a.CaptureCapacity(t.Context(), "a")) != 0 {
		t.Fatal("invalid reading accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if len(a.CaptureCapacity(ctx, "a")) != 0 {
		t.Fatal("canceled capture accepted")
	}
}

func TestCapacityAccountInventoryIsDisplayOnly(t *testing.T) {
	home := privateCapacityHome(t)
	codex := NewCodexAdapter("/unused", map[string]string{"private-a": home, "private-b": home})
	codex.SetExpectedEmails(map[string]string{"private-a": "one@example.test", "private-b": "two@example.test"})
	s, _, _ := testSupervisor(t)
	s.accounts = []EnrolledAccount{
		{ID: "2", Key: "private-b", Harness: Codex, Metadata: &AccountMetadata{Label: "Shared label", Plan: "Pro"}},
		{ID: "1", Key: "private-a", Harness: Codex, Metadata: &AccountMetadata{Label: "Shared label", Plan: "Pro"}},
		{ID: "3", Key: "private-cursor", Harness: Cursor},
		{ID: "4", Key: "private-grok", Harness: Grok},
	}
	s.adapters = map[string]Adapter{Codex: codex, Cursor: NewCursorAdapter("/unused", nil), Grok: NewGrokAdapter()}
	s.capacityLast = map[string]time.Time{"1": time.Now().UTC()}
	got := s.CapacityAccounts("")
	if len(got) != 4 || got[0].AccountID != "1" || got[1].AccountID != "2" || got[0].LastReadAt == nil || got[1].LastReadAt != nil || got[2].Fallback != "not available headless" {
		t.Fatal(got)
	}
	raw, _ := json.Marshal(got)
	for _, private := range []string{home, "private-", "one@example.test", "two@example.test", "binary_path", "auth_path"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private binding escaped")
		}
	}
	if only := s.CapacityAccounts("2"); len(only) != 1 || only[0].AccountID != "2" {
		t.Fatal("account filter ignored")
	}
	cursor := s.adapters[Cursor].(*CursorAdapter)
	if len(cursor.CaptureCapacity(t.Context(), "private-cursor")) != 0 {
		t.Fatal("headless Cursor fabricated quota")
	}
}

func TestCapacityInventoryExcludesRevokedAndKeepsUnboundAccountBlocked(t *testing.T) {
	s, _, _ := testSupervisor(t)
	s.accounts = []EnrolledAccount{
		{ID: "revoked", Key: "old", Harness: Claude},
		{ID: "unbound", Key: "missing", Harness: Claude, DependencyBlocked: true, PinReason: "binding_missing"},
		{ID: "current", Key: "current", Harness: Claude},
	}
	s.adapters[Claude] = &ClaudeAdapter{Homes: map[string]string{"current": privateCapacityHome(t)}}
	s.probedAccounts["current"] = true
	if err := PersistFence(s.state.Path(), s.daemonID, "revoked"); err != nil {
		t.Fatal(err)
	}
	got := s.CapacityAccounts("")
	if len(got) != 2 || got[0].AccountID != "current" || got[0].State != "ready" || got[1].AccountID != "unbound" || got[1].State != "blocked" || got[1].Reason != "binding_missing" {
		t.Fatalf("stale account advertised or current account blocked: %+v", got)
	}
	if detail := s.Lifecycle("").HarnessDetails[Claude]; detail.State != "ready" {
		t.Fatalf("old account blocked current Claude account: %+v", detail)
	}
	if got := s.CapacityAccounts("revoked"); len(got) != 0 {
		t.Fatal("revoked account survived filter", got)
	}
	if err := PersistFence(s.state.Path(), s.daemonID, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.CapacityAccounts(""); len(got) != 0 {
		t.Fatal("disconnected computer advertised live accounts", got)
	}
}

func TestCapacityInventoryExplainsPrivateProfile(t *testing.T) {
	s, _, _ := testSupervisor(t)
	home := privateCapacityHome(t)
	if err := os.Chmod(home, 0755); err != nil {
		t.Fatal(err)
	}
	s.accounts = []EnrolledAccount{{ID: "claude", Key: "local", Harness: Claude}}
	s.adapters[Claude] = &ClaudeAdapter{Homes: map[string]string{"local": home}}
	s.blockedAccounts["claude"] = true
	s.probeReasonDetails = map[string]string{"claude": agentsetup.ProbeProfilePrivate}
	got := s.CapacityAccounts("")
	if len(got) != 1 || got[0].State != "blocked" || got[0].ReasonDetail != agentsetup.ProbeProfilePrivate || got[0].Fallback != "not available: account profile must be private (mode 0700)" {
		t.Fatalf("profile permission failure mislabeled as missing binding: %+v", got)
	}
}

func TestCursorPerHomeProbeAndRun(t *testing.T) {
	aHome, bHome := privateCapacityHome(t), privateCapacityHome(t)
	// A fake status command changes identity with the config home. ACP is still
	// handled by the existing fake CLI, so both launch and probe share routing.
	fake := fakeVendorPath(t, "cursor")
	script, err := os.ReadFile(fake)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("#!/bin/sh\ncase \"$CURSOR_CONFIG_DIR\" in %q) identity=42;; %q) identity=43;; *) exit 8;; esac\n[ \"$HOME\" = \"$CURSOR_CONFIG_DIR\" ] || exit 9\n[ -z \"$CURSOR_API_KEY\" ] || exit 10\nif [ \"$1\" = status ]; then printf '{\"status\":\"authenticated\",\"isAuthenticated\":true,\"userInfo\":{\"userId\":\"%%s\"}}\\n' \"$identity\"; exit 0; fi\n", aHome, bHome)
	if err = os.WriteFile(fake, []byte(prefix+strings.TrimPrefix(string(script), "#!/bin/sh\n")), 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCursorAdapter(fake, map[string]string{"a": "42", "b": "43"})
	a.Homes = map[string]string{"a": aHome, "b": bHome}
	t.Setenv("CURSOR_API_KEY", "fixture-parent-value")
	if !a.Probe(t.Context(), "a") || !a.Probe(t.Context(), "b") || a.Probe(t.Context(), "unknown") {
		t.Fatal("per-account probe failed")
	}
	a.Identities["a"] = "43"
	if a.Probe(t.Context(), "a") {
		t.Fatal("wrong identity accepted")
	}
	a.Identities["a"] = "42"
	req := adapterRequest(t)
	req.AccountKey = "a"
	req.Profile.Harness = Cursor
	p, err := a.Start(t.Context(), req, func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err = p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUnavailableCaptureNeverTakesDispatchSlot(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.api = &capacityTestAPI{API: api}
	s.accounts = []EnrolledAccount{{ID: "cursor", Key: "cursor", Harness: Cursor}, {ID: "grok", Key: "grok", Harness: Grok}}
	s.adapters = map[string]Adapter{Cursor: NewCursorAdapter("/must-not-launch", nil), Grok: NewGrokAdapter()}
	s.probedAccounts = map[string]bool{"cursor": true, "grok": true}
	s.captureIdleCapacity(t.Context(), time.Now())
	if len(s.capacityAttempt) != 0 || s.capacityCapturing {
		t.Fatal("unsupported fallback blocked dispatch")
	}
}

func TestCodexFallbackEnumeratesThreeHomes(t *testing.T) {
	homes := map[string]string{"one": privateCapacityHome(t), "two": privateCapacityHome(t), "three": privateCapacityHome(t)}
	fake := fakeVendorPath(t, "codex_capacity")
	script, err := os.ReadFile(fake)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "#!/bin/sh\n[ -n \"$CODEX_HOME\" ] && [ \"$HOME\" = \"$CODEX_HOME\" ] || exit 7\n[ -z \"$OPENAI_API_KEY\" ] && [ -z \"$CODEX_SQLITE_HOME\" ] || exit 8\nprintf 'captured' > \"$CODEX_HOME/captured\"\n"
	if err = os.WriteFile(fake, []byte(prefix+strings.TrimPrefix(string(script), "#!/bin/sh\n")), 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodexAdapter(fake, homes)
	a.SetExpectedEmails(map[string]string{"one": "agent@example.test", "two": "agent@example.test", "three": "agent@example.test"})
	qualifyCodexFixture(t, a)
	t.Setenv("OPENAI_API_KEY", "fixture-parent-value")
	t.Setenv("CODEX_SQLITE_HOME", "/parent-must-not-inherit")
	for key, home := range homes {
		rs := a.CaptureCapacity(t.Context(), key)
		if len(rs) != 1 || rs[0].Source != "agentd" {
			t.Fatal("per-home capture missing", key)
		}
		if _, err := os.Stat(filepath.Join(home, "captured")); err != nil {
			t.Fatal("selected home was not used", key)
		}
	}
}

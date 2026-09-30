// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// recoveryFixture is shared with web/tests/agent-pairing.test.ts so the web
// labels and fix commands cannot drift from the daemon and server mapping.
// Regenerate with UPDATE_HARNESS_RECOVERY=1 after an intentional change.
const recoveryFixture = "testdata/harness_recovery.json"

type recoveryTable struct {
	Harnesses []string                          `json:"harnesses"`
	Reasons   []string                          `json:"reasons"`
	Fixes     map[string]map[string]*HarnessFix `json:"fixes"`
}

func TestRecoveryFixIsOneSharedVocabulary(t *testing.T) {
	table := recoveryTable{Harnesses: []string{"claude", "codex", "cursor", "grok", "pi"}, Reasons: append(append([]string(nil), HarnessReasons...), "future_reason"), Fixes: map[string]map[string]*HarnessFix{}}
	kinds := map[string]string{"dependency_invalid": FixAddHarness, PinMissing: FixAddHarness, PinPartial: FixAddHarness, PinDrifted: FixAddHarness, PinInvalid: FixAddHarness, PinUnsafe: FixAddHarness, "login_required": FixLogin, "harness_failed": FixRestart, "cli_unavailable": FixRestart, "profile_permissions": FixRestart}
	for _, harness := range table.Harnesses {
		table.Fixes[harness] = map[string]*HarnessFix{}
		for _, reason := range table.Reasons {
			fix := RecoveryFix(harness, reason)
			want := kinds[reason]
			if harness == "claude" && want == FixAddHarness {
				want = FixRepin
			}
			if fix.Kind != want || (want == "") != (fix.Command == "") {
				t.Fatalf("%s/%s: fix %+v, want kind %q", harness, reason, fix, want)
			}
			if fix.Kind != "" {
				table.Fixes[harness][reason] = &fix
			}
			// Every blocked report keeps its code; only known codes get a fix.
			detail, ok := HarnessReport(harness, "blocked", reason)
			if !ok || detail.Reason != reason || detail.Fix != fix {
				t.Fatalf("%s/%s: report %+v %v", harness, reason, detail, ok)
			}
		}
	}
	if fix := RecoveryFix("gemini", PinDrifted); fix != (HarnessFix{}) {
		t.Fatal("unknown harness received a command", fix)
	}
	for _, report := range [][3]string{{"claude", "future_state", "future_reason"}, {"codex", "checking", "starting"}, {"pi", "login_required", ""}} {
		if detail, ok := HarnessReport(report[0], report[1], report[2]); !ok || detail.State != report[1] {
			t.Fatalf("report %v dropped: %+v", report, detail)
		}
	}
	for _, report := range [][3]string{{"claude", "ready", PinDrifted}, {"claude", "blocked", "Local Diagnostic"}, {"claude", "Blocked", ""}, {"gemini", "blocked", PinDrifted}} {
		if _, ok := HarnessReport(report[0], report[1], report[2]); ok {
			t.Fatalf("inconsistent or malformed report accepted: %v", report)
		}
	}
	raw, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.FromSlash(recoveryFixture)
	if _, ok := HarnessReport("claude", "ready", ""); !ok {
		t.Fatal("ready report without a reason was rejected")
	}
	if os.Getenv("UPDATE_HARNESS_RECOVERY") == "1" {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("%s is stale; rerun with UPDATE_HARNESS_RECOVERY=1 and update the web mapping: %v", recoveryFixture, err)
	}
}

func TestPartialAttentionKeepsOnlyAProperSubset(t *testing.T) {
	enrolled := []string{"b", "a", "c"}
	got := PartialAttention("codex", enrolled, "ready", []AccountAttention{
		{AccountID: "c", Reason: PinDrifted},
		{AccountID: "a", Reason: ""},
		{AccountID: "missing", Reason: PinDrifted},
		{AccountID: "b", Reason: "not a code"},
		{AccountID: "a", Reason: "login_required"},
		{AccountID: "c", Reason: PinMissing},
	})
	want := []AccountAttention{{AccountID: "a", Reason: "login_required"}, {AccountID: "c", Reason: PinDrifted}}
	if got.Count != len(want) || got.Truncated || len(got.Accounts) != len(want) {
		t.Fatalf("attention %+v", got)
	}
	for i := range want {
		if got.Accounts[i] != want[i] {
			t.Fatalf("attention[%d] %+v", i, got)
		}
	}
	if len(PartialAttention("codex", []string{"a", "b"}, "ready", []AccountAttention{{AccountID: "a", Reason: PinDrifted}, {AccountID: "b", Reason: PinDrifted}}).Accounts) != 0 {
		t.Fatal("attention covering every enrollment was kept")
	}
	if len(PartialAttention("codex", []string{"a"}, "ready", []AccountAttention{{AccountID: "a", Reason: PinDrifted}}).Accounts) != 0 {
		t.Fatal("a single enrollment was treated as a partial block")
	}
	if len(PartialAttention("codex", enrolled, "blocked", []AccountAttention{{AccountID: "a", Reason: PinDrifted}}).Accounts) != 0 {
		t.Fatal("attention stuck to a blocked harness")
	}
	raw := []byte(`{"state":"ready","attention_accounts":[{"account_id":"a","reason":"pin_drifted"},"nope",{"account_id":"b"}],"fix":"legacy string"}`)
	var detail HarnessDetail
	if err := json.Unmarshal(raw, &detail); err != nil || detail.State != "ready" || detail.Reason != "" || detail.Fix.Command != "" || len(detail.Attention) != 2 || detail.Attention[0].Reason != PinDrifted || detail.Attention[1].Reason != "" || detail.AttentionCount != 0 || detail.AttentionTruncated {
		t.Fatalf("attention decode dropped the ready detail: %+v %v", detail, err)
	}
	if err := json.Unmarshal([]byte(`{"state":"ready","attention_accounts":"nope"}`), &detail); err != nil || detail.State != "ready" || detail.Attention != nil {
		t.Fatalf("malformed attention dropped the detail: %+v %v", detail, err)
	}
	if err := json.Unmarshal([]byte(`{"state":"ready","attention_count":"six","attention_truncated":"yes"}`), &detail); err != nil || detail.State != "ready" || detail.AttentionCount != 0 || detail.AttentionTruncated {
		t.Fatalf("malformed attention total dropped the detail: %+v %v", detail, err)
	}
}

func TestPartialAttentionKeepsSixOfSevenAndADeclaredTotal(t *testing.T) {
	enrolled := []string{"healthy", "b1", "b2", "b3", "b4", "b5", "b6"}
	var blocked []AccountAttention
	for _, id := range enrolled[1:] {
		blocked = append(blocked, AccountAttention{AccountID: id, Reason: PinDrifted})
	}
	got := PartialAttention("codex", enrolled, "ready", blocked)
	if got.Count != 6 || got.Truncated || len(got.Accounts) != 6 {
		t.Fatalf("six of seven: %+v", got)
	}
	for _, item := range got.Accounts {
		if item.AccountID == "healthy" || item.Reason != PinDrifted {
			t.Fatalf("blocked set %+v", got.Accounts)
		}
	}
	wide := []string{"healthy"}
	var many []AccountAttention
	for i := 0; i < AttentionAccountLimit+1; i++ {
		id := fmt.Sprintf("b%02d", i)
		wide = append(wide, id)
		many = append(many, AccountAttention{AccountID: id, Reason: "login_required"})
	}
	capped := PartialAttention("codex", wide, "ready", many)
	if capped.Count != AttentionAccountLimit+1 || !capped.Truncated || len(capped.Accounts) != AttentionAccountLimit {
		t.Fatalf("truncated block: %+v", capped)
	}
	short := PartialAttention("codex", wide, "ready", capped.Accounts)
	restored := short.WithDeclaredTotal(capped.Count, true, len(wide))
	if restored.Count != capped.Count || !restored.Truncated || len(restored.Accounts) != AttentionAccountLimit {
		t.Fatalf("declared total lost: %+v", restored)
	}
	if restored.WithDeclaredTotal(len(wide), true, len(wide)).Count != restored.Count {
		t.Fatal("declared total covered every enrollment")
	}
	raw, err := json.Marshal(HarnessDetail{State: "ready", Attention: restored.Accounts, AttentionCount: restored.Count, AttentionTruncated: true})
	if err != nil {
		t.Fatal(err)
	}
	var detail HarnessDetail
	if err := json.Unmarshal(raw, &detail); err != nil || detail.AttentionCount != restored.Count || !detail.AttentionTruncated || len(detail.Attention) != AttentionAccountLimit {
		t.Fatalf("count did not round-trip: %+v %s %v", detail, raw, err)
	}
}

func TestResolveAttentionKeepsLegacyUncertainty(t *testing.T) {
	enroll := func(n int) []string {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("a%02d", i)
		}
		return ids
	}
	blocked := func(ids []string, n int) []AccountAttention {
		items := make([]AccountAttention, n)
		for i := range items {
			items[i] = AccountAttention{AccountID: ids[i+1], Reason: PinDrifted}
		}
		return items
	}
	type want struct {
		count, list int
		truncated   bool
	}
	check := func(t *testing.T, name string, enrolled []string, items []AccountAttention, count int, flag bool, want want) {
		t.Helper()
		rawIn, err := json.Marshal(HarnessDetail{State: "ready", Attention: items, AttentionCount: count, AttentionTruncated: flag})
		if err != nil {
			t.Fatal(err)
		}
		var decoded HarnessDetail
		if err := json.Unmarshal(rawIn, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.AttentionCount != count || decoded.AttentionTruncated != flag {
			t.Fatalf("%s decode: %+v", name, decoded)
		}
		got := ResolveAttention("codex", "ready", enrolled, decoded.Attention, decoded.AttentionCount, decoded.AttentionTruncated)
		if got.Count != want.count || len(got.Accounts) != want.list || got.Truncated != want.truncated {
			t.Fatalf("%s: count=%d list=%d truncated=%v want %+v", name, got.Count, len(got.Accounts), got.Truncated, want)
		}
		for _, item := range got.Accounts {
			if item.AccountID == enrolled[0] {
				t.Fatalf("%s listed the healthy account", name)
			}
		}
		rawOut, err := json.Marshal(HarnessDetail{State: "ready", Attention: got.Accounts, AttentionCount: got.Count, AttentionTruncated: got.Truncated})
		if err != nil {
			t.Fatal(err)
		}
		if got.Truncated != bytes.Contains(rawOut, []byte(`"attention_truncated":true`)) {
			t.Fatalf("%s wire %s", name, rawOut)
		}
		var round HarnessDetail
		if err := json.Unmarshal(rawOut, &round); err != nil || round.AttentionCount != got.Count || round.AttentionTruncated != got.Truncated || len(round.Attention) != len(got.Accounts) {
			t.Fatalf("%s round-trip %+v %s %v", name, round, rawOut, err)
		}
	}
	seven := enroll(7)
	fiveNames := blocked(seven, 5)
	check(t, "legacy", seven, fiveNames, 0, false, want{5, 5, true})
	check(t, "count-only", seven, fiveNames, 6, false, want{6, 5, true})
	check(t, "flag-only", seven, fiveNames, 0, true, want{5, 5, true})
	for _, n := range []int{5, 6, 7, 32, 33} {
		ids := enroll(n)
		items := blocked(ids, n-1)
		check(t, fmt.Sprintf("total%d", n), ids, items, n-1, false, want{n - 1, n - 1, false})
	}
	ids34 := enroll(34)
	full34 := blocked(ids34, 33)
	check(t, "total34-full", ids34, full34, 33, false, want{33, AttentionAccountLimit, true})
	check(t, "total34-capped", ids34, full34[:AttentionAccountLimit], 33, false, want{33, AttentionAccountLimit, true})
	ids65 := enroll(65)
	full65 := blocked(ids65, 64)
	check(t, "total65-full", ids65, full65, 64, false, want{64, AttentionAccountLimit, true})
	check(t, "total65-capped", ids65, full65[:AttentionAccountLimit], 64, false, want{64, AttentionAccountLimit, true})
	local := PartialAttention("codex", enroll(6), "ready", blocked(enroll(6), 5))
	if local.Count != 5 || local.Truncated || len(local.Accounts) != 5 {
		t.Fatalf("current daemon list of five was treated as uncertain: %+v", local)
	}
	pair := enroll(2)
	one := PartialAttention("codex", pair, "ready", []AccountAttention{{AccountID: pair[1], Reason: PinDrifted}})
	if resolved := one.WithDeclaredTotal(0, false, len(pair)); resolved.Count != 1 || resolved.Truncated || len(resolved.Accounts) != 1 {
		t.Fatalf("short list without metadata became uncertain: %+v", resolved)
	}
}

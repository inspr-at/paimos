// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProbeDetailBoundaryAndCanonicalFix(t *testing.T) {
	for _, tc := range []struct {
		name, harness, state, reason, input, want string
		fix                                       HarnessFix
	}{
		{"default profile", "claude", "blocked", "probe_failed", ProbeClaudeDefaultPrivate, ProbeClaudeDefaultPrivate, HarnessFix{"permissions", `chmod 700 "$HOME/.claude"`}},
		{"daemon sign-out", "claude", "login_required", "login_required", ProbeSignedOut, ProbeSignedOut, RecoveryFix("claude", "login_required")},
		{"wrong sign-out reason", "claude", "blocked", "probe_failed", ProbeSignedOut, "", RecoveryFix("claude", "probe_failed")},
		{"command failure", "claude", "blocked", "probe_failed", ProbeCommandFailed, ProbeCommandFailed, RecoveryFix("claude", "probe_failed")},
		{"private text", "claude", "blocked", "probe_failed", "/private/account: diagnostic", "", RecoveryFix("claude", "probe_failed")},
		{"oversized", "claude", "blocked", "probe_failed", strings.Repeat("x", maxProbeDetail+1), "", RecoveryFix("claude", "probe_failed")},
		{"unknown reason", "claude", "blocked", "future_reason", ProbeCommandFailed, "", HarnessFix{}},
		{"unknown state", "claude", "future_state", "probe_failed", ProbeCommandFailed, "", HarnessFix{}},
		{"ready", "claude", "ready", "", ProbeCommandFailed, "", HarnessFix{}},
		{"draining", "claude", "draining", "", ProbeCommandFailed, "", HarnessFix{}},
		{"sign in", "claude", "login_required", "login_required", ProbeCommandFailed, "", RecoveryFix("claude", "login_required")},
		{"wrong harness", "codex", "blocked", "probe_failed", ProbeClaudeDefaultPrivate, "", RecoveryFix("codex", "probe_failed")},
		{"unknown harness", "future_harness", "blocked", "probe_failed", ProbeCommandFailed, "", HarnessFix{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := HarnessDetail{State: tc.state, Reason: tc.reason, ReasonDetail: "stale", Fix: HarnessFix{"restart", "untrusted command"}}
			got := d.WithProbeDetail(tc.harness, tc.input)
			if got.ReasonDetail != tc.want || got.Fix != tc.fix {
				t.Fatalf("detail = %+v, want detail %q fix %+v", got, tc.want, tc.fix)
			}
		})
	}
}

func TestProbeDetailDecodeIsAdvisoryAndAllowlisted(t *testing.T) {
	for _, value := range []any{ProbeOutputInvalid, "/private/account: diagnostic", strings.Repeat("x", 4096), 42, []string{"bad"}, map[string]string{"path": "private"}, nil} {
		raw, err := json.Marshal(map[string]any{"state": "blocked", "reason": "probe_failed", "reason_detail": value})
		if err != nil {
			t.Fatal(err)
		}
		var got HarnessDetail
		if err := json.Unmarshal(raw, &got); err != nil || got.State != "blocked" || got.Reason != "probe_failed" {
			t.Fatalf("advisory detail invalidated report: %+v, %v", got, err)
		}
		want := ""
		if detail, ok := value.(string); ok && detail == ProbeOutputInvalid {
			want = detail
		}
		if got.ReasonDetail != want {
			t.Fatalf("detail = %q, want %q", got.ReasonDetail, want)
		}
		// A malformed detail must also preserve the account's valid reason.
		raw, err = json.Marshal(map[string]any{"state": "ready", "reason_detail": ProbeOutputInvalid, "attention_accounts": []any{map[string]any{"account_id": "blocked", "reason": "probe_failed", "reason_detail": value, "fix": "untrusted command"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.ReasonDetail != "" || len(got.Attention) != 1 || got.Attention[0].Reason != "probe_failed" || got.Attention[0].ReasonDetail != want {
			t.Fatalf("attention diagnostic = %+v, %v", got, err)
		}
	}
	for _, state := range []string{"ready", "checking", "draining", "future_state"} {
		raw, _ := json.Marshal(map[string]string{"state": state, "reason": "probe_failed", "reason_detail": ProbeCommandFailed})
		var got HarnessDetail
		if err := json.Unmarshal(raw, &got); err != nil || got.ReasonDetail != "" {
			t.Fatalf("non-blocked diagnostic = %+v, %v", got, err)
		}
	}
}

func TestPartialAttentionSanitizesProbeDetails(t *testing.T) {
	got := PartialAttention("claude", []string{"healthy", "blocked", "unknown", "unsafe"}, "ready", []AccountAttention{
		{AccountID: "blocked", Reason: "probe_failed", ReasonDetail: ProbeOutputInvalid},
		{AccountID: "unknown", Reason: "future_reason", ReasonDetail: ProbeOutputInvalid},
		{AccountID: "unsafe", Reason: "probe_failed", ReasonDetail: "/private/profile"},
	})
	if got.Count != 3 || len(got.Accounts) != 3 || got.Accounts[0].ReasonDetail != ProbeOutputInvalid || got.Accounts[1].ReasonDetail != "" || got.Accounts[2].ReasonDetail != "" {
		t.Fatalf("unsafe or lost partial diagnostics: %+v", got)
	}
}

func TestReadinessUsesSafeCauseAndDerivedRepair(t *testing.T) {
	v := View{Enrollments: []Enrollment{{AccountID: "account", Harness: "claude", Label: "Work", State: "connected"}}}
	d := HarnessDetail{State: "blocked", Reason: "probe_failed", ReasonDetail: ProbeClaudeDefaultPrivate, Fix: HarnessFix{"restart", "untrusted command"}}
	stage, action := readinessAction(v, LocalStatus{AccountStatuses: map[string]HarnessDetail{"account": d}})
	if stage != "blocked" || !strings.Contains(action, "sign-in check failed: "+ProbeClaudeDefaultPrivate) || !strings.Contains(action, `chmod 700 "$HOME/.claude"`) || strings.Contains(action, "untrusted") {
		t.Fatal(stage, action)
	}
	d.ReasonDetail = "/private/account: diagnostic"
	_, action = readinessAction(v, LocalStatus{AccountStatuses: map[string]HarnessDetail{"account": d}})
	if strings.Contains(action, "/private/") || strings.Contains(action, "untrusted") || !strings.Contains(action, "aeon-agentd setup") {
		t.Fatal(action)
	}
}

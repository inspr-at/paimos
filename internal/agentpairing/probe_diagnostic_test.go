// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

// Decode the stored JSON without HarnessDetail.UnmarshalJSON: response-side
// sanitization must not hide unsafe diagnostics or commands in the database.
func storedProbeReport(t *testing.T, f *fixture, computer string) map[string]any {
	t.Helper()
	var raw []byte
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT harness_details FROM agent_pairing_computers WHERE id=$1`, computer).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "untrusted") || strings.Contains(string(raw), "/private/profile") {
		t.Fatalf("untrusted diagnostic persisted: %s", raw)
	}
	var reports map[string]map[string]any
	if err := json.Unmarshal(raw, &reports); err != nil {
		t.Fatal(err)
	}
	return reports["claude"]
}

func TestProbeDiagnosticPersistenceIsAllowlistedAndClears(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	for _, tc := range []struct {
		name, state, reason string
		input               any
		want, command       string
	}{
		{"safe cause", "blocked", "probe_failed", agentsetup.ProbeClaudeDefaultPrivate, agentsetup.ProbeClaudeDefaultPrivate, `chmod 700 "$HOME/.claude"`},
		{"raw error", "blocked", "probe_failed", "/private/profile: untrusted diagnostic", "", "aeon-agentd setup"},
		{"malformed extension", "blocked", "probe_failed", map[string]string{"path": "/private/profile"}, "", "aeon-agentd setup"},
		{"oversized extension", "blocked", "probe_failed", strings.Repeat("x", 4096), "", "aeon-agentd setup"},
		{"unknown reason", "blocked", "future_reason", agentsetup.ProbeOutputInvalid, "", ""},
		{"unknown state", "future_state", "probe_failed", agentsetup.ProbeOutputInvalid, "", ""},
		{"recovered", "ready", "", agentsetup.ProbeOutputInvalid, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			progress := map[string]any{"state": "connected", "harness_statuses": map[string]string{"claude": tc.state}, "harness_details": map[string]any{"claude": map[string]any{
				"state": tc.state, "reason": tc.reason, "reason_detail": tc.input, "fix": map[string]string{"kind": "restart", "command": "untrusted command"},
			}}}
			var report agentpairing.View
			decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": progress}, false, "", 200), &report)
			got := report.HarnessDetails["claude"]
			if got.State != tc.state || got.ReasonDetail != tc.want || got.Fix.Command != tc.command {
				t.Fatalf("reconcile report %+v", got)
			}
			stored := storedProbeReport(t, f, *v.ComputerID)
			wantDetail := any(nil)
			if tc.want != "" {
				wantDetail = tc.want
			}
			if stored["state"] != tc.state || stored["reason_detail"] != wantDetail {
				t.Fatalf("stored cause = %+v", stored)
			}
			if fix, _ := stored["fix"].(map[string]any); tc.command != "" && fix["command"] != tc.command || tc.command == "" && fix != nil {
				t.Fatalf("stored fix = %+v", stored)
			}
			var person agentpairing.View
			decodeResult(t, f.call("GET", "/api/agent-pairing/computers/"+*v.ComputerID, nil, true, "", 200), &person)
			if person.HarnessDetails["claude"].ReasonDetail != tc.want || person.HarnessDetails["claude"].Fix.Command != tc.command {
				t.Fatal("person response lost diagnostic")
			}
		})
	}
}

func TestProbeDiagnosticPersistsForBlockedSiblingOnly(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	q := &proposal{id: uuid(t, f.db), device: nonce(), runtime: p.runtime, lifecycle: p.lifecycle, request: map[string]any{}}
	for k, value := range p.request {
		q.request[k] = value
	}
	q.request["request_id"], q.request["device_hash"] = q.id, hash(q.device)
	q.request["existing_computer_id"], q.request["existing_lifecycle_secret"] = *v.ComputerID, p.lifecycle
	q.request["accounts"] = []map[string]string{{"account_key": "claude-add", "harness": "claude", "label": "Second chosen account", "model_profile_id": f.profiles["claude"]}}
	f.submit(q)
	f.approve(q, "connect_only")
	added := f.redeem(q)
	var ids []string
	for _, enrollment := range added.Enrollments {
		if enrollment.Harness == "claude" && enrollment.State == "connected" {
			ids = append(ids, enrollment.AccountID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("expected two enrolled accounts, got %d", len(ids))
	}
	progress := map[string]any{"state": "connected", "harness_statuses": map[string]string{"claude": "ready"}, "harness_details": map[string]any{"claude": map[string]any{
		"state": "ready", "reason_detail": agentsetup.ProbeOutputInvalid,
		"attention_accounts": []any{map[string]any{"account_id": ids[0], "reason": "probe_failed", "reason_detail": agentsetup.ProbeOutputInvalid, "fix": "untrusted command"}},
	}}}
	proof := map[string]any{"tenant_id": f.tenantID, "request_id": p.id, "lifecycle_secret": p.lifecycle, "progress": progress}
	var report agentpairing.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", proof, false, "", 200), &report)
	stored := storedProbeReport(t, f, *v.ComputerID)
	attention, _ := stored["attention_accounts"].([]any)
	if stored["state"] != "ready" || stored["reason_detail"] != nil || len(attention) != 1 {
		t.Fatalf("partial diagnostic lost: %+v", stored)
	}
	item, _ := attention[0].(map[string]any)
	if item["account_id"] != ids[0] || item["reason_detail"] != agentsetup.ProbeOutputInvalid || item["fix"] != nil {
		t.Fatalf("partial diagnostic unsafe: %+v", item)
	}
	// A successful later report must erase the blocked sibling's old cause.
	proof["progress"] = agentpairing.SetupProgress{State: "connected", HarnessStatuses: map[string]string{"claude": "ready"}, HarnessDetails: map[string]agentsetup.HarnessDetail{"claude": {State: "ready"}}}
	decodeResult(t, f.call("POST", "/api/agent-pairing/reconcile", proof, false, "", 200), &report)
	if stored = storedProbeReport(t, f, *v.ComputerID); stored["attention_accounts"] != nil || stored["reason_detail"] != nil {
		t.Fatalf("stale partial diagnostic persisted: %+v", stored)
	}
}

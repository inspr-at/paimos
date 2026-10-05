// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestHostCapacityOwnerRevisionRuntimeAndClaimFence(t *testing.T) {
	f := newFixture(t)
	p := f.propose("claude")
	f.approve(p, "one_per_harness")
	v := f.redeem(p)
	key := "aeon_" + v.RuntimePrefix + "_" + p.runtime
	if v.HostCapacity == nil || v.HostCapacity.Policy.Mode != "off" || v.HostCapacity.Policy.ConsiderActivity {
		t.Fatal("throttle and activity must default off")
	}
	policy := hostcapacity.Default()
	policy.Mode = "fixed"
	policy.MaximumLoad = 30
	policy.MaximumAgents = 12
	path := "/api/agent-pairing/computers/" + *v.ComputerID + "/capacity"
	f.call("PUT", path, map[string]any{"expected_revision": v.Revision + 1, "policy": policy}, true, "", 409)
	f.call("PUT", path, map[string]any{"expected_revision": v.Revision, "policy": policy}, false, key, 403)
	f.call("PUT", path, map[string]any{"expected_revision": v.Revision, "policy": policy}, true, "", 200)
	load := 40.0
	active := true
	signals := hostcapacity.Signals{Load: &load, Cores: 18, MemoryPressure: "normal", Power: "plugged_in", Thermal: "normal", InputActive: &active}
	var report hostcapacity.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 200), &report)
	if report.Reason != "host_load" || report.Signals.InputActive != nil || len(report.History) != 1 {
		t.Fatalf("wrong report: %+v", report)
	}
	// The server fence protects the actual claim even if a daemon ignores the UI.
	actor := tenant.Principal{ID: *v.PrincipalID, TenantID: f.tenantID, Kind: tenant.Agent}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), actor), f.db.App, f.tenantID, func(tx pgx.Tx) error {
		if err := agentpairing.LockMutation(t.Context(), tx); err != nil {
			return err
		}
		return agentpairing.RunFence(t.Context(), tx, v.Enrollments[0].AccountID, *v.Enrollments[0].VerificationRunID, true)
	})
	denied, ok := err.(*agentpairing.Error)
	if !ok || denied.Code != "host_capacity_wait" {
		t.Fatal("claim failed for the wrong reason", err)
	}
	var consumed bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT verification_claimed_at IS NOT NULL FROM agent_pairing_enrollments WHERE account_id=$1`, v.Enrollments[0].AccountID).Scan(&consumed); err != nil || consumed {
		t.Fatal("waiting consumed verification claim", err)
	}
	load = 20
	signals.Load = &load
	decodeResult(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 200), &report)
	if report.Reason != "" || len(report.History) != 1 {
		t.Fatal("falling load did not resume or history grew faster than one point/minute")
	}
	signals.Cores = 0
	retryErrorCode(t, f.call("POST", "/api/agent-pairing/self/capacity", signals, false, key, 400), "invalid_request")
	// Revoked management permission cannot reuse the already reviewed policy.
	if _, err := f.db.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person); err != nil {
		t.Fatal(err)
	}
	retryErrorCode(t, f.call("PUT", path, map[string]any{"expected_revision": v.Revision + 1, "policy": policy}, true, "", 403), "forbidden")
}
func TestSignOutEverywhereFencesCanonicalSetAndPreservesOtherAccounts(t *testing.T) {
	f := newFixture(t)
	p := f.propose("codex", "claude")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	// Different display labels and harnesses do not establish a shared account.
	target := v.Enrollments[0]
	other := v.Enrollments[1]
	secondProposal := f.propose(target.Harness)
	f.approve(secondProposal, "connect_only")
	second := f.redeem(secondProposal)
	shared := second.Enrollments[0]
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=$1,quota_pool_fingerprint=$1 WHERE id=ANY($2::uuid[])`, strings.Repeat("a", 64), []string{target.AccountID, shared.AccountID}); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"targets": []map[string]any{{"computer_id": v.ComputerID, "account_id": target.AccountID, "expected_revision": v.Revision + 1}}}
	retryErrorCode(t, f.call("POST", "/api/agent-pairing/accounts/sign-out", body, true, "", 409), "conflict")
	var connected int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_pairing_enrollments WHERE computer_id=$1 AND state='connected'`, v.ComputerID).Scan(&connected); err != nil || connected != 2 {
		t.Fatal("stale signout mutated sign-ins", err)
	}
	body["targets"] = []map[string]any{{"computer_id": v.ComputerID, "account_id": target.AccountID, "expected_revision": v.Revision}}
	retryErrorCode(t, f.call("POST", "/api/agent-pairing/accounts/sign-out", body, true, "", 409), "conflict")
	body["targets"] = []map[string]any{{"computer_id": v.ComputerID, "account_id": target.AccountID, "expected_revision": v.Revision}, {"computer_id": second.ComputerID, "account_id": shared.AccountID, "expected_revision": second.Revision}}
	f.call("POST", "/api/agent-pairing/accounts/sign-out", body, true, "", 200)
	var state, cleanup, sibling string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state,local_cleanup,(SELECT state FROM agent_pairing_enrollments WHERE account_id=$2) FROM agent_pairing_enrollments WHERE account_id=$1`, target.AccountID, other.AccountID).Scan(&state, &cleanup, &sibling); err != nil || state != "revoked" || cleanup != "pending" || sibling != "connected" {
		t.Fatal("signout changed sibling or invented cleanup", err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state,local_cleanup FROM agent_pairing_enrollments WHERE account_id=$1`, shared.AccountID).Scan(&state, &cleanup); err != nil || state != "revoked" || cleanup != "pending" {
		t.Fatal("complete canonical set was not signed out", err)
	}
}

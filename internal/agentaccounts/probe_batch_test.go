// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"net/http"
	"testing"
	"time"
)

// Risk: one rejected observation must not refresh health or acknowledge its
// siblings; exact-key revocation must still stop writes inside each transaction.
func TestProbeBatchIndependentObservationsAndRevocation(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "probe-batch", now)
	var second Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "second", "harness": "codex", "daemon_id": "daemon-a", "label": "Second"}), 201, &second)
	var third Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "third", "harness": "codex", "daemon_id": "daemon-a", "label": "Third"}), 201, &third)
	entry := func(id string, at time.Time, ok bool, failure string) map[string]any {
		return map[string]any{"account_id": id, "observed_at": at, "probe": map[string]any{"daemon_id": "daemon-a", "daemon_generation": "g1", "available": ok, "failure": failure}}
	}
	body := map[string]any{"items": []any{entry(f.account.ID, now, true, ""), entry(second.ID, now.Add(-10*time.Second), false, "auth_failed"), entry(third.ID, now.Add(-ProbeFreshness-time.Second), true, "")}}
	var out struct {
		Items []struct {
			AccountID string `json:"account_id"`
			Status    int    `json:"status"`
		} `json:"items"`
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/probes", encoded(t, body), 200, &out)
	if len(out.Items) != 3 || out.Items[0].Status != 200 || out.Items[1].Status != 200 || out.Items[2].Status != 409 {
		t.Fatalf("wrong per-observation status: %+v", out)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_at=$2 AND last_probe_ok=false AND last_probe_failure='auth_failed'`, second.ID, now.Add(-10*time.Second)) != 1 {
		t.Fatal("individual failed observation lost its time or result")
	}
	if scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_at IS NULL`, third.ID) != 1 {
		t.Fatal("stale observation refreshed account")
	}
	// Superseded healthy and newer unhealthy observations remain distinct.
	body = map[string]any{"items": []any{entry(second.ID, now.Add(-20*time.Second), true, "")}}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/probes", encoded(t, body), 200, &out)
	if out.Items[0].Status != 409 || scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_ok=false`, second.ID) != 1 {
		t.Fatal("older successful observation erased sign-out")
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=$1 WHERE principal_id=$2`, now, f.runner.ID); err != nil {
		t.Fatal(err)
	}
	body = map[string]any{"items": []any{entry(third.ID, now, true, "")}}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/probes", encoded(t, body), 200, &out)
	if out.Items[0].Status != http.StatusForbidden || scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND last_probe_at IS NULL`, third.ID) != 1 {
		t.Fatal("revoked exact key wrote health")
	}
	// Current project/account grants cannot be bypassed with the same principal.
	f.token = issueKey(t, f.runner, []string{"account.probe"})
	if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.runner.ID); err != nil {
		t.Fatal(err)
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/probes", encoded(t, body), 200, &out)
	if out.Items[0].Status != http.StatusForbidden {
		t.Fatal("revoked role wrote health")
	}
}

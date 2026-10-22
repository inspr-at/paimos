// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/workorders"
)

// Existing lifecycle tests model a current daemon. Acquire actual server
// credentials rather than bypassing the new boundary or weakening assertions.
// Legacy transport tests opt out and send every proof explicitly.
func (f *fixture) requestContentProof(method, path string, body []byte, key string) string {
	parts := strings.Split(strings.Trim(strings.SplitN(path, "?", 2)[0], "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	var runID string
	switch {
	case len(parts) == 4 && parts[1] == "projects" && parts[3] == "harness-sessions" && method == "POST":
		var in struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(body, &in)
		runID = in.RunID
	case parts[1] == "runs" && len(parts) >= 4 && (parts[3] == "claim" || parts[3] == "handoff"):
		var in struct {
			Refusal string `json:"verification_unavailable"`
		}
		_ = json.Unmarshal(body, &in)
		if in.Refusal != "" {
			return ""
		}
		runID = parts[2]
	case (parts[1] == "nodes" || parts[1] == "work-orders") && workorders.UUID(parts[2]):
		prefix := strings.SplitN(key, "_", 3)
		if len(prefix) != 3 {
			return ""
		}
		_ = f.db.Admin.QueryRow(f.t.Context(), `SELECT r.id::text FROM agent_runs r JOIN agent_keys k ON k.tenant_id=r.tenant_id AND k.principal_id=r.agent_principal_id WHERE k.prefix=$1 AND r.work_order_id=$2 ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, prefix[1], parts[2]).Scan(&runID)
	case len(parts) >= 5 && parts[1] == "projects" && parts[3] == "harness-sessions" && workorders.UUID(parts[4]):
		// Safety routes intentionally exercise the capability-free path.
		if len(parts) >= 6 && (parts[5] == "yield" || parts[5] == "stop" || parts[5] == "confirm-exit" || strings.HasSuffix(path, "/complete")) {
			return ""
		}
		_ = f.db.Admin.QueryRow(f.t.Context(), `SELECT run_id::text FROM harness_sessions WHERE tenant_id=$1 AND id=$2`, f.tenantID, parts[4]).Scan(&runID)
	}
	return f.prepareContentProof(runID, key)
}

func (f *fixture) prepareContentProof(runID, key string) string {
	if !workorders.UUID(runID) {
		return ""
	}
	f.contentMu.Lock()
	defer f.contentMu.Unlock()
	cacheKey := key + "\x00" + runID
	if proof := f.contentProofs[cacheKey]; proof != "" {
		return proof
	}
	var daemon, generation string
	if err := f.db.Admin.QueryRow(f.t.Context(), `SELECT c.daemon_id,a.last_daemon_generation FROM agent_runs r
 JOIN agent_pairing_computers c ON c.tenant_id=r.tenant_id AND c.principal_id=r.agent_principal_id
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id
 JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
 JOIN model_profiles m ON m.id=r.model_profile_id AND m.harness=a.harness
 WHERE r.tenant_id=$1 AND r.id=$2 AND a.last_daemon_generation IS NOT NULL
 AND (r.requested_account_id IS NULL OR r.requested_account_id=a.id) ORDER BY a.id LIMIT 1`, f.tenantID, runID).Scan(&daemon, &generation); err != nil {
		return ""
	}
	r := f.request("POST", "/api/runs/"+runID+"/capability", map[string]string{"daemon_id": daemon, "daemon_generation": generation}, false, key)
	r.Header.Set(db.RunCapabilityPolicyHeader, db.RunCapabilityPolicy)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 200 {
		return ""
	} // The original negative request must still prove its own refusal.
	var out struct {
		Capability string `json:"capability"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		return ""
	}
	if f.contentProofs == nil {
		f.contentProofs = map[string]string{}
	}
	f.contentProofs[cacheKey] = out.Capability
	return out.Capability
}

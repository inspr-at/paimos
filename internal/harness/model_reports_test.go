// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/jackc/pgx/v5"
)

func TestModelReportsRequireWorkerLeaseAndStayInHarness(t *testing.T) {
	f := fixture(t)
	lease := "synthetic-model-reports-lease-000000000000000"
	session := decode(t, f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "model-report-session-reference-000001", "worker_lease": lease, "management_mode": "unmanaged", "role": "worker", "model": "gpt-6.1-sol", "reasoning_effort": "high"}, ""))["id"].(string)
	path := "/api/projects/" + f.project + "/harness-sessions/" + session + "/model-reports"
	reports := []modelregistry.Observation{{ReportID: modelregistry.EvidenceID("one-failure"), Harness: "codex", Model: "gpt-6.1-sol", Effort: "high", Status: "invalid"}}
	expect(t, f.call(f.agent, "POST", path, reports, "wrong-lease-0000000000000000000000000000"), 403)
	expect(t, f.call(f.person, "POST", path, reports, lease), 403)
	expect(t, f.call(f.agent, "POST", path, reports, lease), 200)
	expect(t, f.call(f.agent, "POST", path, reports, lease), 200)
	reports[0].ReportID = modelregistry.EvidenceID("second-failure")
	reports[0].Harness = "grok"
	expect(t, f.call(f.agent, "POST", path, reports, lease), 400)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var failures int
		if err := tx.QueryRow(t.Context(), `SELECT failures FROM model_observations WHERE harness='codex' AND model='gpt-6.1-sol'`).Scan(&failures); err != nil {
			return err
		}
		if failures != 1 {
			t.Fatalf("replay or invalid harness counted: %d", failures)
		}
		return nil
	})
}

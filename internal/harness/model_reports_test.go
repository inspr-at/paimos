// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/jackc/pgx/v5"
)

func TestObservedProfileCannotBeRequestedUntilPersonAcceptance(t *testing.T) {
	f := fixture(t)
	modelregistry.New(f.db.App).Mount(f.mux)
	lease := "observed-profile-request-lease-0000000000"
	base := "/api/projects/" + f.project + "/harness-sessions"
	session := decode(t, f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "observed-profile-session-reference", "worker_lease": lease, "management_mode": "unmanaged", "role": "worker"}, ""))["id"].(string)
	var account string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'wildcard','codex','fixture',$2,'Wildcard account') RETURNING id::text`, f.person.TenantID, f.agent.ID).Scan(&account)
	})
	reports := []modelregistry.Observation{{ReportID: modelregistry.EvidenceID("observed-request"), Harness: "codex", Model: "gpt-next", Effort: "high", Status: "advertised"}}
	expect(t, f.call(f.person, "POST", "/api/models/reports", reports, ""), 200)
	var profile string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE model='gpt-next'`).Scan(&profile)
	})
	request := map[string]any{"request_id": uid(), "expected_generation": session, "kind": "model_request", "account_id": account, "model_profile_id": profile}
	path := base + "/" + session + "/requests"
	expect(t, f.call(f.person, "POST", path, request, ""), 400)
	accept := map[string]any{"harness": "codex", "model": "gpt-next", "effort": "high"}
	expect(t, f.call(f.agent, "POST", "/api/models/proposals/accept", accept, ""), 403)
	expect(t, f.call(f.person, "POST", "/api/models/proposals/accept", accept, ""), 200)
	expect(t, f.call(f.person, "POST", path, request, ""), 201)
}

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
	reports[0].Harness = "codex"
	reports[0].Model = "gpt-6-astra"
	expect(t, f.call(f.agent, "POST", path, reports, lease), 400)
	reports[0].Model = "gpt-6.1-sol"
	reports[0].Effort = "xhigh"
	expect(t, f.call(f.agent, "POST", path, reports, lease), 400)
	// The same principal's other generation does not authorize this lease.
	decode(t, f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "model-report-session-reference-000002", "worker_lease": lease + "2", "management_mode": "unmanaged", "role": "worker", "model": "gpt-6-astra", "reasoning_effort": "high"}, ""))
	reports[0].Model = "gpt-6-astra"
	reports[0].Effort = "high"
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

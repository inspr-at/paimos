// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/jackc/pgx/v5"
)

func enrollReportingHarness(t *testing.T, f *harnessFixture, name string) string {
	t.Helper()
	var account string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,$2,$2,'fixture',$3,'Reporting account') RETURNING id::text`, f.person.TenantID, name, f.agent.ID).Scan(&account)
	})
	return account
}

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
	accepted := f.call(f.person, "POST", "/api/models/proposals/accept", accept, "")
	expect(t, accepted, 200)
	request["model_profile_id"] = decode(t, accepted)["id"]
	expect(t, f.call(f.person, "POST", path, request, ""), 201)
}

func TestModelReportsRequireWorkerLeaseAndStayInHarness(t *testing.T) {
	f := fixture(t)
	enrollReportingHarness(t, f, "codex")
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
	expect(t, f.call(f.agent, "POST", path, reports, lease), 403)
	reports[0].Model = "gpt-6.1-sol"
	reports[0].Effort = "xhigh"
	expect(t, f.call(f.agent, "POST", path, reports, lease), 403)
	// The same principal's other generation does not authorize this lease.
	decode(t, f.call(f.person, "POST", "/api/projects/"+f.project+"/harness-sessions", map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "model-report-session-reference-000002", "worker_lease": lease + "2", "management_mode": "unmanaged", "role": "worker", "model": "gpt-6-astra", "reasoning_effort": "high"}, ""))
	reports[0].Model = "gpt-6-astra"
	reports[0].Effort = "high"
	expect(t, f.call(f.agent, "POST", path, reports, lease), 403)
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

func TestGrokEnrollmentCannotAuthorizeCodexSessionEvidence(t *testing.T) {
	f := fixture(t)
	enrollReportingHarness(t, f, "grok")
	lease := "unenrolled-codex-report-lease-0000000000"
	base := "/api/projects/" + f.project + "/harness-sessions"
	registration := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": uid(), "worker_lease": lease, "management_mode": "unmanaged", "role": "worker", "model": "gpt-6.1-sol", "reasoning_effort": "high"}
	// A grok-only worker can register metadata, but cannot make it evidence.
	w := f.call(f.agent, "POST", base, registration, "")
	expect(t, w, 201)
	session := decode(t, w)["id"].(string)
	path := base + "/" + session
	for _, status := range []string{"invalid", "working", "advertised"} {
		reports := []modelregistry.Observation{{ReportID: uid(), Harness: "codex", Model: "gpt-6.1-sol", Effort: "high", Status: status}}
		expect(t, f.call(f.agent, "POST", path+"/model-reports", reports, lease), 403)
		expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "model_reports": reports}, lease), 403)
		registration["harness_session_ref"] = uid()
		registration["model_reports"] = reports
		expect(t, f.call(f.agent, "POST", base, registration, ""), 403)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var observations, receipts, sessions int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_observations),(SELECT count(*) FROM model_report_receipts),(SELECT count(*) FROM harness_sessions)`).Scan(&observations, &receipts, &sessions); err != nil {
			return err
		}
		if observations != 0 || receipts != 0 || sessions != 1 {
			t.Fatalf("unenrolled evidence persisted: %d observations, %d receipts, %d sessions", observations, receipts, sessions)
		}
		return nil
	})
}

func TestUsageHealthRequiresLiveEnrolledExactModel(t *testing.T) {
	for _, scenario := range []string{"other model", "no enrollment", "unavailable account", "stopped session", "exact model"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture(t)
			modelregistry.New(f.db.App).Mount(f.mux)
			const model = "gpt-6.1-sol"
			const effort = "high"
			lease := "usage-model-health-lease-000000000000000"
			base := "/api/projects/" + f.project + "/harness-sessions"
			w := f.call(f.agent, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": uid(), "worker_lease": lease, "management_mode": "unmanaged", "role": "worker", "model": "gpt-6-astra", "reasoning_effort": effort}, "")
			expect(t, w, 201)
			session := decode(t, w)["id"].(string)
			var account string
			if scenario != "no enrollment" {
				account = enrollReportingHarness(t, f, "codex")
			}
			var until time.Time
			f.tx(t, f.person, func(tx pgx.Tx) error {
				if scenario != "other model" {
					if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET model=$2 WHERE id=$1`, session, model); err != nil {
						return err
					}
				}
				if scenario == "unavailable account" {
					if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET state='unavailable' WHERE id=$1`, account); err != nil {
						return err
					}
				}
				if scenario == "stopped session" {
					if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=now() WHERE id=$1`, session); err != nil {
						return err
					}
				}
				return tx.QueryRow(t.Context(), `INSERT INTO model_observations(tenant_id,harness,model,effort,failures,last_failing_at,suppressed_until,source) VALUES($1,'codex',$2,$3,2,now(),now()+interval '24 hours','agent') RETURNING suppressed_until`, f.person.TenantID, model, effort).Scan(&until)
			})
			report := usagePayload()
			report["model"] = model
			out, replayed := usageResult(t, f.call(f.agent, "POST", base+"/"+session+"/usage", report, lease))
			if replayed || out.Model != model || out.OutputTokens == nil || *out.OutputTokens == 0 {
				t.Fatal("health authorization lost accounting data")
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var failures, receipts int
				var suppressed *time.Time
				if err := tx.QueryRow(t.Context(), `SELECT failures,suppressed_until,(SELECT count(*) FROM model_report_receipts) FROM model_observations WHERE harness='codex' AND model=$1 AND effort=$2`, model, effort).Scan(&failures, &suppressed, &receipts); err != nil {
					return err
				}
				if scenario == "exact model" {
					if failures != 0 || suppressed != nil || receipts != 1 {
						t.Fatal("authorized exact-model usage did not clear suppression")
					}
				} else if failures != 2 || suppressed == nil || !suppressed.Equal(until) || receipts != 0 {
					t.Fatal("ineligible usage changed model suppression")
				}
				return nil
			})
		})
	}
}

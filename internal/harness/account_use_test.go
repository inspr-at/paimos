// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/jackc/pgx/v5"
)

// Risks 21/22: a denied session model request is accepted, or a matching label
// is counted as compliance. Unmanaged telemetry stays recorded in every case.
func TestUnmanagedAccountContextRequestsAndAttribution(t *testing.T) {
	f := fixture(t)
	path, id, lease := usageSession(t, f, "unmanaged")
	var allowed, denied, profile string
	f.tx(t, f.person, func(tx pgx.Tx) error {
		for _, out := range []*string{&allowed, &denied} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,billing_mode) VALUES($1,gen_random_uuid()::text,'codex','test',$2,'Same label','api') RETURNING id::text`, f.person.TenantID, f.agent.ID).Scan(out); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'context-request','1','codex','openai','test-model','high','standard') RETURNING id::text`, f.person.TenantID).Scan(&profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by) SELECT $1,$2,id,'migration',$3 FROM work_contexts WHERE kind='default' ON CONFLICT DO NOTHING`, f.person.TenantID, allowed, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM account_use_cells WHERE account_id=$1`, denied)
		return err
	})
	request := map[string]any{"request_id": uid(), "expected_generation": id, "kind": "model_request", "account_id": denied, "model_profile_id": profile}
	w := f.call(f.person, "POST", path+"/requests", request, "")
	expect(t, w, 409)
	if decode(t, w)["code"] != accountuse.NotAllowed {
		t.Fatal("wrong refusal", w.Body.String())
	}
	request["account_id"] = allowed
	request["request_id"] = uid()
	expect(t, f.call(f.person, "POST", path+"/requests", request, ""), 201)
	for _, tc := range []struct{ name, account, want string }{
		{"allowed", allowed, "allowed"}, {"denied", denied, "outside_matrix"}, {"missing", "", "unattributed"}, {"ambiguous label", "", "unattributed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := usagePayload()
			report["model"] = "test-" + uid()
			if tc.account != "" {
				report["account_id"] = tc.account
			}
			if tc.name == "ambiguous label" {
				report["account_label"] = "Same label"
			}
			out, replayed := usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
			if replayed || out.AccountUse != tc.want || out.InputTokens == nil || *out.InputTokens != 100 {
				t.Fatal("attribution or totals", out)
			}
			retry, replayed := usageResult(t, f.call(f.agent, "POST", path+"/usage", report, lease))
			if !replayed || retry.AccountUse != tc.want || retry.SessionID != out.SessionID || retry.Model != out.Model || retry.Sequence != out.Sequence {
				t.Fatal("replay lost attribution", retry)
			}
		})
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		var violations, unknown int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='account_use.outside_matrix'),count(*) FILTER(WHERE type='account_use.unattributed') FROM events`).Scan(&violations, &unknown); err != nil {
			return err
		}
		if violations != 1 || unknown != 2 {
			t.Fatal("missing attribution events", violations, unknown)
		}
		return nil
	})
}

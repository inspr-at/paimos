// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
)

func TestRunCreationStampsStarterAndRejectsDisallowedAccount(t *testing.T) {
	for _, kind := range []string{"person", "creator-key", "operator-key"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			o := f.order(t, nil)
			requester := f.person
			want := f.person.ID
			if strings.Contains(kind, "key") {
				requester = f.agent
				if kind == "creator-key" {
					requester.KeyCreatorID = f.person.ID
				} else {
					want = ""
				}
			}
			eu := "eu"
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "default", Residency: &eu})
				return err
			})
			var run agentruns.Run
			f.call(t, requester, "POST", "/api/work-orders/"+o.NodeID+"/runs", map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile}, 201, &run)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var starter *string
				var residency string
				if err := tx.QueryRow(t.Context(), `SELECT prefs_person_id::text,residency FROM agent_runs WHERE id=$1`, run.ID).Scan(&starter, &residency); err != nil {
					return err
				}
				if residency != "eu" || (want == "" && starter != nil) || (want != "" && (starter == nil || *starter != want)) {
					t.Fatal("wrong run stamp", kind, residency, starter)
				}
				return nil
			})
			account := uuid()
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label)
   VALUES($1,$2,'residency-account','codex','daemon-test',$3,'Any provider')`, f.person.TenantID, account, f.agent.ID)
				return err
			})
			body := `{"agent_principal_id":"` + f.agent.ID + `","model_profile_id":"` + f.profile + `","requested_account_id":"` + account + `"}`
			response := f.request(f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", body, "")
			if response.Code != 409 || !strings.Contains(response.Body.String(), `"code":"residency_unmet"`) {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
}

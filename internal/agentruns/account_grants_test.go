// SPDX-License-Identifier: AGPL-3.0-only

package agentruns_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestAccountModelGrantsEnforcedAtCreateAndClaim(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	run := f.run(t, o)
	ids := f.reserve(t, run)
	var account string
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT account_id::text FROM agent_runs WHERE id=$1`, run.ID).Scan(&account); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET allowed_model_profile_ids='{}' WHERE id=$1`, account)
		return err
	})
	body := map[string]any{"agent_principal_id": f.agent.ID, "model_profile_id": f.profile, "requested_account_id": account}
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", body, 409, nil)
	// Existing reservations do not override a grant revoked before claim.
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 409, nil)
	if f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE status='starting'`) != 0 {
		t.Fatal("revoked grant started a run")
	}
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET allowed_model_profile_ids=ARRAY[$2::uuid] WHERE id=$1`, account, f.profile)
		return err
	})
	f.call(t, f.person, "POST", "/api/work-orders/"+o.NodeID+"/runs", body, 201, nil)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, nil)
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/jackc/pgx/v5"
)

// AEON-402: a person cancels a queued run from /agents. Its holds go back to
// the ledger, including a shared login's ledger, and a started run is untouched.
func TestCancelQueuedRunIsPersonOnlyAndReleasesHolds(t *testing.T) {
	f := setup(t)
	run := f.run(t, f.order(t, nil))
	f.reserve(t, run)
	sibling := uuid()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,quota_fingerprint) VALUES($1,$2::uuid,$2::text,'codex','other-daemon',$3,'Sibling',repeat('ab',32))`, f.agent.TenantID, sibling, f.other.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=repeat('ab',32) WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET account_id=$2 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID, sibling)
		return err
	})
	path := "/api/runs/" + run.ID + "/cancel"
	f.call(t, f.agent, "POST", path, nil, 403, nil)
	f.call(t, f.foreign, "POST", path, nil, 404, nil)
	var out agentruns.Run
	f.call(t, f.person, "POST", path, nil, 200, &out)
	if out.Status != "cancelled" || out.EndedAt == nil || out.StartedAt != nil {
		t.Fatalf("cancelled run: %+v", out)
	}
	if n := f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run.ID); n != 0 {
		t.Fatal("reservation still held after cancel")
	}
	if n := f.count(t, f.person, `SELECT reserved FROM account_allowance_windows WHERE account_id=$1`, sibling); n != 0 {
		t.Fatalf("shared ledger keeps %d reserved units", n)
	}
	f.call(t, f.person, "POST", path, nil, 200, nil)
	if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.cancelled'`) != 1 {
		t.Fatal("cancel event missing or a repeat duplicated it")
	}

	claimed := f.claim(t, f.run(t, f.order(t, nil)))
	f.call(t, f.person, "POST", "/api/runs/"+claimed.ID+"/cancel", nil, 409, nil)
	if n := f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND status='cancelled'`, claimed.ID); n != 0 {
		t.Fatal("a started run was cancelled")
	}
}

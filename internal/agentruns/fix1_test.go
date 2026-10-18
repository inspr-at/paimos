// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/jackc/pgx/v5"
)

func TestClaimReleasesObsoleteSiblingLedgerAfterMigration(t *testing.T) {
	f := setup(t)
	run := f.run(t, f.order(t, nil))
	ids := f.reserve(t, run)
	other := f.run(t, f.order(t, nil))
	f.reserve(t, other)
	sibling := uuid()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,quota_fingerprint,quota_pool_fingerprint) VALUES($1,$2::uuid,$2::text,'codex','sibling-daemon',$3,'Sibling',repeat('ab',32),repeat('ab',32))`, f.agent.TenantID, sibling, f.other.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=repeat('ab',32),quota_pool_fingerprint=repeat('ab',32) WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET account_id=$2 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID, sibling); err != nil {
			return err
		}
		// Migration 1054 clears legacy consent; the daemon retains these exact IDs.
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_pool_fingerprint=''`)
		return err
	})
	// A forged reservation set must not release someone else's authentic hold.
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody([]string{uuid()}), 409, nil)
	if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run.ID) != 1 {
		t.Fatal("forged claim released a hold")
	}
	body, err := json.Marshal(claimBody(ids))
	if err != nil {
		t.Fatal(err)
	}
	w := f.request(f.agent, "POST", "/api/runs/"+run.ID+"/claim", string(body), f.token)
	if w.Code != 409 {
		t.Fatalf("obsolete claim=%d", w.Code)
	}
	if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run.ID) != 0 || f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND account_id IS NOT NULL`, run.ID) != 0 || f.count(t, f.person, `SELECT coalesce(sum(reserved),0) FROM account_allowance_windows WHERE account_id=$1`, sibling) != 0 {
		t.Fatal("claim stranded the pre-migration sibling hold or slot")
	}
	if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, other.ID) != 1 {
		t.Fatal("obsolete claim released another run's hold")
	}
	// Reroute to a fresh ledger, then claim without including released history.
	newIDs := f.reserve(t, run)
	roundTheClock(t, f, run.ID)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(newIDs), 200, nil)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(newIDs), 200, nil)
}

func TestClaimReleasesHoldAfterAccountLeavesRunGroup(t *testing.T) {
	f := setup(t)
	run := f.run(t, f.order(t, nil))
	ids := f.reserve(t, run)
	roundTheClock(t, f, run.ID)
	group := uuid()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_groups(tenant_id,id,harness,name) VALUES($1,$2,'codex','Changed group')`, f.agent.TenantID, group); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_run_targets(tenant_id,run_id,group_id) VALUES($1,$2,$3)`, f.agent.TenantID, run.ID, group)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 409, nil)
	if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run.ID) != 0 || f.count(t, f.person, `SELECT count(*) FROM agent_runs WHERE id=$1 AND account_id IS NOT NULL`, run.ID) != 0 {
		t.Fatal("claim kept a hold outside its run group")
	}
}

func TestConsecutiveVendorHandoffsPreserveRunGroupTarget(t *testing.T) {
	f := setup(t)
	v := f.claim(t, f.run(t, f.order(t, nil)))
	roundTheClock(t, f, v.ID)
	group := uuid()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_groups(tenant_id,id,harness,name) VALUES($1,$2,'codex','Run target')`, f.agent.TenantID, group); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET group_id=$2 WHERE id=$1`, v.AccountID, group); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_run_targets(tenant_id,run_id,group_id) VALUES($1,$2,$3)`, f.agent.TenantID, v.ID, group)
		return err
	})
	for handoff := 1; handoff <= 2; handoff++ {
		f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/telemetry", agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "failed", ErrorCode: "vendor_limit"}, 200, nil)
		f.tx(t, f.agent, func(tx pgx.Tx) error {
			if err := agentaccounts.Settle(t.Context(), tx, f.agent, v.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET at=now()-interval '2 hours' WHERE run_id=$1`, v.ID); err != nil {
				return err
			}
			// The admission wait is durable; advance its deadline explicitly.
			if _, err := tx.Exec(t.Context(), `UPDATE account_readiness_facts SET next_attempt_at=now()-interval '1 second' WHERE resource_id IN (SELECT resource_id FROM account_readiness_memberships WHERE account_id=$1) AND stop_kind='unnamed'`, v.AccountID); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET vendor_retry_same_account=true,vendor_retry_at=now()-interval '1 second' WHERE id=$1`, v.ID)
			return err
		})
		var queued []agentruns.Run
		f.call(t, f.agent, "GET", "/api/runs/queued", nil, 200, &queued)
		if len(queued) != 1 {
			t.Fatalf("handoff %d: no follow-up", handoff)
		}
		if f.count(t, f.person, `SELECT count(*) FROM account_run_targets WHERE run_id=$1 AND group_id=$2`, queued[0].ID, group) != 1 {
			t.Errorf("handoff %d lost the run-level group target", handoff)
		}
		if handoff == 1 {
			// Use the real target account and new reservation to continue the retry.
			ids := []string{uuid()}
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2,daemon_id='daemon-test',daemon_generation='generation-1',status='starting',started_at=now() WHERE id=$1`, queued[0].ID, v.AccountID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET reserved=reserved+100 WHERE account_id=$1`, v.AccountID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO account_reservations(tenant_id,id,run_id,window_id,reserved_units) SELECT tenant_id,$1,$2,id,100 FROM account_allowance_windows WHERE account_id=$3`, ids[0], queued[0].ID, v.AccountID)
				return err
			})
			queued[0].AccountID = v.AccountID
			v = queued[0]
		}
	}
}

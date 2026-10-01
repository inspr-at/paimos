// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

// roundTheClock gives the run's account an owner schedule that is always in
// hours, sprinting, with Keep for you off. These claims are about reading
// authority and freshness. The default 08–22 band waits after 22:00 UTC, and a
// window that crosses midnight otherwise keeps only today's slice — less than
// the 100 units this run already holds. Sprint pins the whole remainder.
func roundTheClock(t *testing.T, f *fixture, runID string) {
	t.Helper()
	s := capacity.DefaultSchedule()
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	s.Reserve = capacity.ReserveOff
	s.Override = "sprint"
	raw, _ := json.Marshal(s)
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH a AS (UPDATE agent_accounts SET capacity_owner=$2 WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1) RETURNING tenant_id,id)
 INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) SELECT tenant_id,$2,'account',id::text,id,$3 FROM a`, runID, f.person.ID, raw)
		return err
	})
}

func TestCapacityClaimRechecksReadingAuthorityAndFreshness(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	run := f.run(t, o)
	ids := f.reserve(t, run)
	roundTheClock(t, f, run.ID)
	update := func(allowed bool, age string, used int) {
		t.Helper()
		f.tx(t, f.agent, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET unit='percent',allowance=100,used=$4,capacity_kind='5h',capacity_read_at=clock_timestamp()-$3::interval,capacity_allowed=$2 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID, allowed, age, used)
			return err
		})
	}
	update(true, "11 minutes", 0)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 409, nil)
	update(false, "0 minutes", 0)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 409, nil)
	update(true, "0 minutes", 1)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 409, nil)
	update(true, "0 minutes", 0)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, nil)
}

func TestCapacityClaimAllowsOnlyItsRecordedRefresh(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	run := f.run(t, o)
	ids := f.reserve(t, run)
	roundTheClock(t, f, run.ID)
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET unit='percent',allowance=100,used=0,reserved=1,capacity_kind='5h',capacity_read_at=clock_timestamp()-interval '11 minutes',capacity_allowed=true,capacity_refresh_run=$1 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE account_reservations SET reserved_units=1 WHERE run_id=$1`, run.ID)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, nil)
}

func TestClaimSharedQuotaWindowKeepsDoorOwnership(t *testing.T) {
	f := setup(t)
	run := f.run(t, f.order(t, nil))
	ids := f.reserve(t, run)
	sibling := uuid()
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,quota_fingerprint,quota_pool_fingerprint) VALUES($1,$2::uuid,$2::text,'codex','other-daemon',$3,'Sibling',repeat('ab',32),repeat('ab',32))`, f.agent.TenantID, sibling, f.other.ID); err != nil {
			return err
		}
		// The ledger may be on a different daemon, but only the run's own daemon
		// may claim. An unrelated quota is never accepted even with the exact IDs.
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET account_id=$2 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID, sibling)
		return err
	})
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody([]string{uuid()}), 409, nil)
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=repeat('ab',32),quota_pool_fingerprint=repeat('ab',32) WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID)
		return err
	})
	wrong := claimBody(ids)
	wrong["daemon_id"] = "other-daemon"
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", wrong, 403, nil)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, nil)
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, nil)
}

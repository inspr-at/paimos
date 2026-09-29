// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

func (f *fixture) pinCapacitySchedule(t *testing.T, runID string) {
	t.Helper()
	// These tests exercise reading authority and reservation freshness, not
	// working hours. Sprint keeps the full remaining quota available at night
	// and on weekends without weakening either of those checks.
	schedule := capacity.DefaultSchedule()
	schedule.Override = "sprint"
	raw, err := json.Marshal(schedule)
	if err != nil {
		t.Fatal(err)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET capacity_owner=$2 WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, runID, f.person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,schedule) VALUES($1,$2,'user','',$3)`, f.person.TenantID, f.person.ID, raw)
		return err
	})
}

func TestCapacityClaimRechecksReadingAuthorityAndFreshness(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	run := f.run(t, o)
	ids := f.reserve(t, run)
	f.pinCapacitySchedule(t, run.ID)
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
	f.pinCapacitySchedule(t, run.ID)
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

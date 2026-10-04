// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Keep both queued reservations on the same measured ledger. Move persisted
// measurement timestamps, never sleep, and settle through the telemetry endpoint.
func measuredClaimQueue(t *testing.T, age string) (*fixture, []agentruns.Run, []string, string) {
	t.Helper()
	f := setup(t)
	f.mux = http.NewServeMux()
	workorders.New(f.d.App).Mount(f.mux)
	agentruns.New(f.d.App, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, run agentruns.Run, _ agentruns.Telemetry) error {
		return agentaccounts.Settle(ctx, tx, p, run.ID)
	}).Mount(f.mux)
	runs := []agentruns.Run{f.run(t, f.order(t, nil)), f.run(t, f.order(t, nil))}
	ids := append(f.reserve(t, runs[0]), uuid())
	var resource string
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT m.resource_id::text FROM account_readiness_memberships m JOIN agent_runs r ON r.account_id=m.account_id WHERE r.id=$1 ORDER BY m.resource_id LIMIT 1`, runs[0].ID).Scan(&resource); err != nil {
			return err
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`UPDATE agent_runs SET account_id=(SELECT account_id FROM agent_runs WHERE id=$1) WHERE id=$2`, []any{runs[0].ID, runs[1].ID}},
			{`UPDATE agent_accounts SET max_parallel_runs=3 WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, []any{runs[0].ID}},
			{`UPDATE account_reservations SET reserved_units=1 WHERE run_id=$1`, []any{runs[0].ID}},
			{`INSERT INTO account_reservations(tenant_id,id,run_id,window_id,reserved_units) SELECT tenant_id,$2,$3,window_id,1 FROM account_reservations WHERE run_id=$1`, []any{runs[0].ID, ids[1], runs[1].ID}},
			{`UPDATE account_allowance_windows SET unit='percent',allowance=100,used=90,reserved=2,capacity_kind='5h',capacity_source='harness',capacity_allowed=true,
 capacity_read_at=clock_timestamp()-CASE $2 WHEN 'stale' THEN interval '11 minutes' ELSE interval '2 minutes' END,
 ends_at=CASE $2 WHEN 'expired' THEN clock_timestamp()-interval '1 minute' ELSE ends_at END,capacity_retired=($2='retired')
 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, []any{runs[0].ID, age}},
		} {
			if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	return f, runs, ids, resource
}

func assertMeasuredClaim(t *testing.T, f *fixture, run agentruns.Run, id string) {
	t.Helper()
	var claimed agentruns.Run
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody([]string{id}), 200, &claimed)
	if claimed.Status != "starting" || f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE id=$1 AND run_id=$2 AND state='active' AND reserved_units=1`, id, run.ID) != 1 {
		t.Fatal("claim did not start with its exact measured reservation")
	}
	f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody([]string{id}), 200, nil)
}

func TestObsoleteMeasuredClaimsWithoutRecoveryWait(t *testing.T) {
	for _, age := range []string{"stale", "expired", "retired"} {
		t.Run(age, func(t *testing.T) {
			f, runs, ids, _ := measuredClaimQueue(t, age)
			if f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE wait_id IS NOT NULL OR recovery_run_id IS NOT NULL`) != 0 {
				t.Fatal("ordinary aging fixture unexpectedly has a recovery wait")
			}
			// Current Hold still wins over obsolete measurement data.
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=jsonb_set(schedule,'{override}','"hold"') WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, runs[0].ID)
				return err
			})
			claimFailure(t, f, runs[0].ID, ids[:1], "reserved capacity is not eligible")
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=jsonb_set(schedule,'{override}','"sprint"') WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, runs[0].ID)
				return err
			})
			claimFailure(t, f, runs[0].ID, ids[1:], "reservation set mismatch")
			// Decision 1C permits both runs; aging alone cannot invent a serial
			// recovery wait or require a permit which can never be acquired.
			for i, run := range runs {
				assertMeasuredClaim(t, f, run, ids[i])
			}
			if f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE wait_id IS NOT NULL OR recovery_run_id IS NOT NULL`) != 0 ||
				f.count(t, f.person, `SELECT reserved FROM account_allowance_windows WHERE id=(SELECT window_id FROM account_reservations WHERE id=$1)`, ids[0]) != 2 {
				t.Fatal("ordinary claims changed the holds or fabricated recovery authority")
			}
		})
	}
}

func TestObsoleteMeasuredClaimsAfterSuccessfulRecovery(t *testing.T) {
	for _, kind := range []string{"unnamed", "money_402"} {
		for _, age := range []string{"stale", "expired", "retired"} {
			t.Run(kind+"/"+age, func(t *testing.T) {
				f, runs, ids, resource := measuredClaimQueue(t, age)
				f.tx(t, f.agent, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO account_readiness_facts(tenant_id,resource_id,window_key,reported_by_account_id,binding_revision,source,observed_at,stop_kind,denial_reason,credit_state,wait_id,next_attempt_at)
 SELECT a.tenant_id,$2,'vendor',a.id,a.link_revision,'harness',clock_timestamp()-interval '1 hour',$3,'vendor_denied',CASE $3 WHEN 'money_402' THEN 'exhausted' ELSE 'unknown' END,gen_random_uuid(),clock_timestamp()-interval '1 second'
 FROM agent_runs r JOIN agent_accounts a ON a.id=r.account_id WHERE r.id=$1`, runs[0].ID, resource, kind)
					return err
				})
				assertMeasuredClaim(t, f, runs[0], ids[0])
				if f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND stop_kind=$2 AND wait_id IS NOT NULL AND recovery_run_id=$3`, resource, kind, runs[0].ID) != 1 {
					t.Fatal("first queued claim did not acquire the recovery wait")
				}
				claimFailure(t, f, runs[1].ID, ids[1:], "reserved capacity is not eligible")
				var completed agentruns.Run
				f.call(t, f.agent, "POST", "/api/runs/"+runs[0].ID+"/telemetry", agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "completed", Output: 5}, 200, &completed)
				if completed.Status != "completed" || f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND stop_kind='none' AND wait_id IS NULL AND recovery_run_id IS NULL AND next_attempt_at IS NULL AND backoff_step=0`, resource) != 1 {
					t.Fatal("evidenced inference did not clear the stop and recovery permit")
				}
				if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE id=$1 AND state='settled'`, ids[0]) != 1 ||
					f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE id=$1 AND state='active' AND reserved_units=1`, ids[1]) != 1 {
					t.Fatal("recovery settlement did not preserve the second queued hold")
				}
				assertMeasuredClaim(t, f, runs[1], ids[1])
				f.call(t, f.agent, "POST", "/api/runs/"+runs[1].ID+"/telemetry", agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "completed", Output: 5}, 200, nil)
				if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE id=ANY($1::uuid[]) AND state='settled'`, ids) != 2 ||
					f.count(t, f.person, `SELECT reserved FROM account_allowance_windows WHERE id=(SELECT window_id FROM account_reservations WHERE id=$1)`, ids[0]) != 0 {
					t.Fatal("completed claims left stranded reservations")
				}
			})
		}
	}
}

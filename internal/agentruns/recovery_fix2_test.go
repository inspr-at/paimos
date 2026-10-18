// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/jackc/pgx/v5"
)

func TestQueuedRecoveryClaimsKeepTheirExactReservation(t *testing.T) {
	for _, age := range []string{"current", "expired"} {
		t.Run(age, func(t *testing.T) {
			f := setup(t)
			run := f.run(t, f.order(t, nil))
			ids := f.reserve(t, run)
			sibling := f.run(t, f.order(t, nil))
			siblingReservation := uuid()
			var resource string
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT m.resource_id::text FROM account_readiness_memberships m JOIN agent_runs r ON r.account_id=m.account_id WHERE r.id=$1 ORDER BY m.resource_id LIMIT 1`, run.ID).Scan(&resource); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=(SELECT account_id FROM agent_runs WHERE id=$1) WHERE id=$2`, run.ID, sibling.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET max_parallel_runs=3 WHERE id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET unit='percent',allowance=1,reserved=1,capacity_bucket='unknown:'||$1::text,
 capacity_kind='blind',capacity_source='estimate',capacity_allowed=true,capacity_read_at=clock_timestamp()-interval '2 minutes',capacity_refresh_run=$1::uuid,
 starts_at=clock_timestamp()-interval '2 hours',ends_at=clock_timestamp()+CASE $2 WHEN 'expired' THEN interval '-1 minute' ELSE interval '5 minutes' END
 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID, age); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE account_reservations SET reserved_units=1 WHERE run_id=$1`, run.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `WITH w AS (INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,reserved,capacity_kind,capacity_bucket,capacity_read_at,capacity_source,capacity_allowed,capacity_refresh_run)
 SELECT w.tenant_id,w.account_id,w.starts_at,w.ends_at,'percent',1,1,'blind','unknown:'||$3::text,w.capacity_read_at,'estimate',true,$3::uuid
 FROM account_allowance_windows w JOIN account_reservations r ON r.window_id=w.id WHERE r.run_id=$1 RETURNING tenant_id,id)
 INSERT INTO account_reservations(tenant_id,id,run_id,window_id,reserved_units)
 SELECT tenant_id,$2::uuid,$3::uuid,id,1 FROM w`, run.ID, siblingReservation, sibling.ID); err != nil {
					return err
				}
				// Advance the persisted wait deadline, never sleep for expiry.
				_, err := tx.Exec(t.Context(), `INSERT INTO account_readiness_facts(tenant_id,resource_id,window_key,reported_by_account_id,binding_revision,source,observed_at,stop_kind,denial_reason,credit_state,wait_id,next_attempt_at)
 SELECT $1,$2,'vendor',a.id,a.link_revision,'harness',clock_timestamp()-interval '1 hour','unnamed','vendor_denied','unknown',gen_random_uuid(),clock_timestamp()-interval '1 second'
 FROM agent_runs r JOIN agent_accounts a ON a.id=r.account_id WHERE r.id=$3`, f.agent.TenantID, resource, run.ID)
				return err
			})
			if age == "expired" {
				// Expiry may defer to current admission only for the run's own
				// provisional grant, and Hold must still prevent promotion.
				f.tx(t, f.agent, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_refresh_run=$2 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID, sibling.ID)
					return err
				})
				f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 409, nil)
				f.tx(t, f.agent, func(tx pgx.Tx) error {
					if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_refresh_run=$1 WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=jsonb_set(schedule,'{override}','"hold"') WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID)
					return err
				})
				body, err := json.Marshal(claimBody(ids))
				if err != nil {
					t.Fatal(err)
				}
				response := f.request(f.agent, "POST", "/api/runs/"+run.ID+"/claim", string(body), f.token)
				var failure struct{ Error string }
				if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
					t.Fatal(err)
				}
				if response.Code != 409 || failure.Error != "reserved capacity is not eligible" {
					t.Fatalf("expired provisional grant bypassed Hold or failed before current admission: %d %s", response.Code, response.Body.String())
				}
				if f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id IS NOT NULL`, resource) != 0 {
					t.Fatal("failed claim consumed the recovery permit")
				}
				f.tx(t, f.agent, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=jsonb_set(schedule,'{override}','"sprint"') WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID)
					return err
				})
			}
			var claimed agentruns.Run
			f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, &claimed)
			if claimed.Status != "starting" || f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id=$2`, resource, run.ID) != 1 {
				t.Fatal("real claim did not promote the queued run")
			}
			f.call(t, f.agent, "POST", "/api/runs/"+sibling.ID+"/claim", claimBody([]string{siblingReservation}), 409, nil)
			if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE id=ANY($1::uuid[]) AND state='active'`, []string{ids[0], siblingReservation}) != 2 {
				t.Fatal("recovery claim replaced a reservation")
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/jackc/pgx/v5"
)

func claimFailure(t *testing.T, f *fixture, run string, ids []string, want string) {
	t.Helper()
	body, err := json.Marshal(claimBody(ids))
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(f.agent, "POST", "/api/runs/"+run+"/claim", string(body), f.token)
	var failure struct{ Error string }
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if response.Code != 409 || failure.Error != want {
		t.Fatalf("claim failed for the wrong reason: %d %s", response.Code, response.Body.String())
	}
}

func TestQueuedMeasuredReservationRecoversThroughClaim(t *testing.T) {
	for _, age := range []string{"stale", "expired", "retired"} {
		t.Run(age, func(t *testing.T) {
			f := setup(t)
			run := f.run(t, f.order(t, nil))
			ids := f.reserve(t, run)
			var resource string
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT m.resource_id::text FROM account_readiness_memberships m JOIN agent_runs r ON r.account_id=m.account_id WHERE r.id=$1 ORDER BY m.resource_id LIMIT 1`, run.ID).Scan(&resource); err != nil {
					return err
				}
				// This measured hold precedes the denial and later measurement expiry.
				if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET unit='percent',allowance=100,used=90,reserved=1,capacity_kind='5h',capacity_source='harness',capacity_allowed=true,capacity_read_at=clock_timestamp() WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE account_reservations SET reserved_units=1 WHERE run_id=$1`, run.ID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO account_readiness_facts(tenant_id,resource_id,window_key,reported_by_account_id,binding_revision,source,observed_at,stop_kind,denial_reason,credit_state,wait_id,next_attempt_at)
 SELECT $1,$2,'vendor',a.id,a.link_revision,'harness',clock_timestamp(),'unnamed','vendor_denied','unknown',gen_random_uuid(),clock_timestamp()+interval '1 hour' FROM agent_runs r JOIN agent_accounts a ON a.id=r.account_id WHERE r.id=$3`, f.agent.TenantID, resource, run.ID)
				return err
			})
			claimFailure(t, f, run.ID, ids, "reserved capacity is not eligible")
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET capacity_read_at=clock_timestamp()-interval '11 minutes',ends_at=CASE $2 WHEN 'expired' THEN clock_timestamp()-interval '1 minute' ELSE ends_at END,capacity_retired=($2='retired') WHERE id=(SELECT window_id FROM account_reservations WHERE run_id=$1)`, run.ID, age); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE account_readiness_facts SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE resource_id=$1 AND window_key='vendor'`, resource)
				return err
			})
			// Current Hold must still veto promotion and leave its permit untouched.
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=jsonb_set(schedule,'{override}','"hold"') WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID)
				return err
			})
			claimFailure(t, f, run.ID, ids, "reserved capacity is not eligible")
			if f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id IS NOT NULL`, resource) != 0 {
				t.Fatal("failed stale claim consumed recovery")
			}
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=jsonb_set(schedule,'{override}','"sprint"') WHERE account_id=(SELECT account_id FROM agent_runs WHERE id=$1)`, run.ID)
				return err
			})
			var claimed agentruns.Run
			f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, &claimed)
			if claimed.Status != "starting" || f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id=$2`, resource, run.ID) != 1 ||
				f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE id=$1 AND run_id=$2 AND state='active' AND reserved_units=1`, ids[0], run.ID) != 1 {
				t.Fatal("actual stale measured claim lost its permit or exact hold")
			}
		})
	}
}

func TestPooledSiblingLocalRecoveryThroughClaim(t *testing.T) {
	for _, change := range []string{"unchanged", "withdrawn", "revision"} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			agentaccounts.New(f.d.App).Mount(f.mux)
			f.token = f.key(t, f.agent, []string{"work_orders.read", "work_orders.write", "run.read", "run.create", "run.claim", "run.telemetry", "account.manage", "account.probe"})
			run := f.run(t, f.order(t, nil))
			ids := f.reserve(t, run)
			var account, resource string
			sibling := uuid()
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT account_id::text FROM agent_runs WHERE id=$1`, run.ID).Scan(&account); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,quota_fingerprint,quota_pool_fingerprint) VALUES($1,$2::uuid,$2::text,'codex','sibling-daemon',$3,'Sibling',repeat('ab',32),repeat('ab',32))`, f.agent.TenantID, sibling, f.agent.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=repeat('ab',32),quota_pool_fingerprint=repeat('ab',32) WHERE id=$1`, account); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT m.resource_id::text FROM account_readiness_memberships m JOIN account_readiness_resources r ON r.id=m.resource_id WHERE m.account_id=$1 AND r.identity_kind='account'`, sibling).Scan(&resource); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO account_readiness_facts(tenant_id,resource_id,window_key,reported_by_account_id,binding_revision,source,observed_at,stop_kind,denial_reason,credit_state,wait_id,next_attempt_at)
 SELECT tenant_id,$2,'vendor',id,link_revision,'harness',clock_timestamp()-interval '1 hour','unnamed','vendor_denied','unknown',gen_random_uuid(),clock_timestamp()-interval '1 second' FROM agent_accounts WHERE id=$1`, sibling, resource)
				return err
			})
			// Actual routing acquires recovery from the sibling's local resource.
			f.call(t, f.agent, "POST", "/api/agent-accounts/route", map[string]any{"run_id": run.ID, "daemon_id": "daemon-test", "account_ids": []string{account}, "estimates": map[string]int64{"cost_micros": 100}}, 200, nil)
			if f.count(t, f.person, `SELECT count(*) FROM account_readiness_memberships WHERE account_id=$1 AND resource_id=$2`, account, resource) != 0 ||
				f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id=$2`, resource, run.ID) != 1 {
				t.Fatal("fixture did not reserve a sibling-local recovery permit")
			}
			if change != "unchanged" {
				f.tx(t, f.agent, func(tx pgx.Tx) error {
					query := `UPDATE agent_accounts SET quota_pool_fingerprint='' WHERE id=$1`
					if change == "revision" {
						query = `UPDATE agent_accounts SET link_revision=link_revision+1 WHERE id=$1`
					}
					_, err := tx.Exec(t.Context(), query, sibling)
					return err
				})
				f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 409, nil)
				if f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, run.ID) != 0 ||
					f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id IS NOT NULL`, resource) != 0 {
					t.Fatal("obsolete sibling authority kept a hold or recovery permit")
				}
				return
			}
			var claimed agentruns.Run
			f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, &claimed)
			f.call(t, f.agent, "POST", "/api/runs/"+run.ID+"/claim", claimBody(ids), 200, nil)
			if claimed.Status != "starting" || f.count(t, f.person, `SELECT count(*) FROM account_reservations WHERE id=$1 AND state='active'`, ids[0]) != 1 ||
				f.count(t, f.person, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id=$2`, resource, run.ID) != 1 {
				t.Fatal("unchanged sibling pool did not retain the exact recovery ledger")
			}
		})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

func TestVendorHandoffWaitRetryAndFences(t *testing.T) {
	for _, tc := range []struct {
		name         string
		minutes      int
		pin, blocked bool
		wantRetry    bool
	}{{"long", 70, false, false, true}, {"short", 12, false, false, false}, {"boundary", 20, false, false, false}, {"pinned", 70, true, false, false}, {"pool-dry", 70, false, true, false}, {"residency-tightened", 70, false, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			o := f.order(t, nil)
			v := f.claim(t, f.run(t, o))
			spare := uuid()
			s := capacity.DefaultSchedule()
			s.Reserve = capacity.ReserveOff
			for i := range s.Week {
				s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
			}
			raw, _ := json.Marshal(s)
			f.tx(t, f.agent, func(tx pgx.Tx) error {
				// All clocks are relative to the same transaction; schedules never depend
				// on the hour or weekday the suite happens to execute.
				if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET label='Main',capacity_owner=$2 WHERE id=$1`, v.AccountID, f.person.ID); err != nil {
					return err
				}
				if tc.pin {
					if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET requested_account_id=account_id WHERE id=$1`, v.ID); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,state) VALUES($1,$2::uuid,$2::uuid::text,'codex','daemon-test',$3,'Spare',now(),true,'generation-1',$4,$5)`, f.agent.TenantID, spare, f.agent.ID, f.person.ID, map[bool]string{true: "unavailable", false: "available"}[tc.blocked]); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',100,'unrestricted')`, f.agent.TenantID, spare); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3::uuid,$4),($1,$2,'account',$5::uuid::text,$5::uuid,$4)
 ON CONFLICT (tenant_id,principal_id,scope,scope_key) DO UPDATE SET schedule=EXCLUDED.schedule`, f.person.TenantID, f.person.ID, spare, raw, v.AccountID); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,window_minutes,used_percent,resets_at,read_at,source,ordinary_usage_allowed,run_id,phase) VALUES($1,$2,'5h',300,100,now()+$3::int*interval '1 minute',now(),'harness',false,$4,'end')`, f.agent.TenantID, v.AccountID, tc.minutes, v.ID)
				return err
			})
			// A mid-turn error cannot create a second running process.
			f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/telemetry", agentruns.Telemetry{Sequence: 1, Kind: "status", Status: "waiting", ErrorCode: "vendor_limit"}, 200, nil)
			var queued []agentruns.Run
			f.call(t, f.agent, "GET", "/api/runs/queued", nil, 200, &queued)
			if len(queued) != 0 {
				t.Fatal("handoff happened mid-turn")
			}
			finished := agentruns.Telemetry{Sequence: 2, Kind: "finished", Status: "failed", ErrorCode: "vendor_limit"}
			f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/telemetry", finished, 200, nil)
			if tc.name == "residency-tightened" {
				f.tx(t, f.person, func(tx pgx.Tx) error {
					eu := "eu"
					_, err := modelprefs.SaveScope(t.Context(), tx, f.person, modelprefs.Scope{Level: "person", PersonID: &f.person.ID, Residency: &eu})
					return err
				})
			}
			// Duplicate telemetry and duplicate polls must never duplicate a retry/log.
			f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/telemetry", finished, 200, nil)
			for i := 0; i < 2; i++ {
				f.call(t, f.agent, "GET", "/api/runs/queued", nil, 200, &queued)
			}
			if tc.wantRetry {
				if len(queued) != 1 || queued[0].RetryOfRunID == nil || *queued[0].RetryOfRunID != v.ID || queued[0].OrderID != o.NodeID || queued[0].CapacityOverride != "" {
					t.Fatalf("wrong retry %+v", queued)
				}
				if len(v.Trace) == 0 || string(queued[0].Trace) != string(v.Trace) {
					t.Fatal("vendor retry lost the creation preference trace")
				}
				var target string
				f.tx(t, f.agent, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT retry_account_id::text FROM agent_runs WHERE id=$1`, queued[0].ID).Scan(&target)
				})
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var starter *string
					if err := tx.QueryRow(t.Context(), `SELECT prefs_person_id::text FROM agent_runs WHERE id=$1`, queued[0].ID).Scan(&starter); err != nil {
						return err
					}
					if starter == nil || *starter != f.person.ID {
						t.Fatal("retry lost starter")
					}
					return nil
				})
				if target != spare {
					t.Fatal("did not select Spare")
				}
				if f.count(t, f.person, `SELECT count(*) FROM events WHERE type='run.capacity_handoff' AND after->>'note' LIKE 'Moved from Main to Spare%'`) != 1 {
					t.Fatal("handoff log missing or duplicated")
				}
			} else {
				if len(queued) != 0 {
					t.Fatal("retry bypassed wait or pin")
				}
				var stopped agentruns.Run
				f.call(t, f.person, "GET", "/api/runs/"+v.ID, nil, 200, &stopped)
				if stopped.Wait == nil || stopped.Wait.Code != "vendor" || stopped.Wait.Until == nil || stopped.Wait.RunNowAllowed {
					t.Fatal("vendor wait missing")
				}
			}
		})
	}
}

func TestShortVendorWaitContinuesSameAccountAfterReset(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	v := f.claim(t, f.run(t, o))
	roundTheClock(t, f, v.ID)
	f.call(t, f.agent, "POST", "/api/runs/"+v.ID+"/telemetry", agentruns.Telemetry{Sequence: 1, Kind: "finished", Status: "failed", ErrorCode: "vendor_limit"}, 200, nil)
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		if err := agentaccounts.Settle(t.Context(), tx, f.agent, v.ID); err != nil {
			return err
		}
		// Advance the frozen reset and backoff instead of sleeping.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET vendor_retry_same_account=true,vendor_retry_at=now()-interval '1 second' WHERE id=$1`, v.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET at=now()-interval '2 hours' WHERE run_id=$1`, v.ID); err != nil {
			return err
		}
		// Advance the persisted retry clock too; telemetry edits cannot reset it.
		_, err := tx.Exec(t.Context(), `UPDATE account_readiness_facts SET next_attempt_at=now()-interval '1 second' WHERE resource_id IN (SELECT resource_id FROM account_readiness_memberships WHERE account_id=$1) AND stop_kind='unnamed'`, v.AccountID)
		return err
	})
	var queued []agentruns.Run
	f.call(t, f.agent, "GET", "/api/runs/queued", nil, 200, &queued)
	if len(queued) != 1 {
		t.Fatalf("same-account continuation missing %+v", queued)
	}
	var target string
	f.tx(t, f.agent, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT retry_account_id::text FROM agent_runs WHERE id=$1`, queued[0].ID).Scan(&target)
	})
	if target != *v.AccountID {
		t.Fatal("short wait switched accounts")
	}
}

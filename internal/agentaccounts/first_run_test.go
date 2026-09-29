// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestFirstReadingGrantIsSingleUsePerGeneration(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		t.Run(harness, func(t *testing.T) {
			reset(t)
			person := makePrincipal(t, "first-"+harness, "person", "Ada", []string{"admin"})
			runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
			profile := codexProfile(t, person)
			seed := func(fn func(pgx.Tx) error) {
				t.Helper()
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, fn); err != nil {
					t.Fatal(err)
				}
			}
			if harness == "claude" {
				seed(func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'first-claude','1','claude','anthropic','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
				})
			}
			token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
			mod := accountsMod()
			var a Account
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "first", "harness": harness, "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
			probe := func(g string) {
				callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", encoded(t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": g, "available": true}), 200, nil)
			}
			probe("g1")
			s := capacity.DefaultSchedule()
			for i := range s.Week {
				s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
			}
			callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
			first := insertRun(t, person, runner, profile)
			route := mustRoute(t, mod, runner, token, first, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			if len(route.Reservations) != 1 {
				t.Fatal(route)
			}
			second := insertRun(t, person, runner, profile)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			// Repeated polling replays the same reservation, never another grant.
			again := mustRoute(t, mod, runner, token, first, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			if again.Reservations[0].ReservationID != route.Reservations[0].ReservationID {
				t.Fatal("grant replay changed")
			}
			// A new probe generation does not create a second concurrent run.
			probe("g2")
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			probe("g1")
			seed(func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, first, "", "") })
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			probe("g1")
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			probe("g2")
			mustRoute(t, mod, runner, token, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			// A real harness reading replaces bootstrap admission. It is stored as
			// harness evidence and enables the normal second parallel slot.
			now := time.Now().UTC().Add(-time.Second)
			reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 10, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness", RunID: second, Phase: "start"}
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
			third := insertRun(t, person, runner, profile)
			mustRoute(t, mod, runner, token, third, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			if scalar(t, person, `SELECT count(*) FROM account_capacity_readings WHERE account_id=$1 AND source='harness'`, a.ID) != 1 {
				t.Fatal("real reading missing")
			}
		})
	}
}

func TestBlindDayPolicyAndDurableDailyLimit(t *testing.T) {
	for _, harness := range []string{"grok", "cursor", "pi"} {
		t.Run(harness, func(t *testing.T) {
			reset(t)
			person := makePrincipal(t, "blind-"+harness, "person", "Ada", []string{"admin"})
			runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
			profile := codexProfile(t, person)
			seed := func(fn func(pgx.Tx) error) {
				t.Helper()
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, fn); err != nil {
					t.Fatal(err)
				}
			}
			seed(func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'1',$2,'openai','test','high','strong') RETURNING id::text`, person.TenantID, harness).Scan(&profile)
			})
			token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
			mod := accountsMod()
			var a Account
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "blind", "harness": harness, "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
			s := capacity.DefaultSchedule()
			for i := range s.Week {
				s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
			}
			callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
			for i := 0; i < 3; i++ {
				run := insertRun(t, person, runner, profile)
				mustRoute(t, mod, runner, token, run, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
				concurrent := insertRun(t, person, runner, profile)
				callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, concurrent, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
				seed(func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, run, "", "") })
			}
			run := insertRun(t, person, runner, profile)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			// Outside the person's hours Q3 removes the daily cap, including off days.
			s.Week = capacity.Preset(5)
			s.Timezone = "UTC"
			seed(func(tx pgx.Tx) error {
				now := time.Date(2026, 9, 29, 23, 10, 0, 0, time.UTC)
				a, err := getAccount(t.Context(), tx, a.ID)
				if err != nil {
					return err
				}
				a.LastProbeAt = &now
				raw := encoded(t, s)
				if _, err = tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=$2::jsonb WHERE account_id=$1`, a.ID, raw); err != nil {
					return err
				}
				_, wait, err := admission(t.Context(), tx, a, a.Windows, now, 0, runRow{Purpose: "managed"}, false)
				if err == nil && wait != nil {
					t.Fatalf("night blocked: %+v", wait)
				}
				if err != nil {
					return err
				}
				// A content-free vendor stop survives night, the next day and a
				// person-requested schedule override. No reset is invented.
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2 WHERE id=$1`, run, a.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, run, now); err != nil {
					return err
				}
				for _, at := range []time.Time{now, now.Add(24 * time.Hour)} {
					a.LastProbeAt = &at
					_, w, err := admission(t.Context(), tx, a, nil, at, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
					if err != nil {
						return err
					}
					if w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until != nil {
						t.Fatalf("vendor stop lost: %+v", w)
					}
				}
				// Even a manual cap plus an optimistic estimate cannot erase the
				// stop. Explicit vendor recovery is required, later than that stop.
				manual := []Window{{AccountID: a.ID, StartsAt: now, EndsAt: now.Add(2 * time.Hour), Unit: "requests", Allowance: 100, PaceModel: "unrestricted"}}
				for _, source := range []string{"estimate", "harness"} {
					if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,window_minutes,used_percent,resets_at,read_at,source,ordinary_usage_allowed) VALUES($1,$2,'5h',300,5,$3,$4,$5,true)`, person.TenantID, a.ID, now.Add(time.Hour), now.Add(time.Minute), source); err != nil {
						return err
					}
					afterReading := now.Add(2 * time.Minute)
					a.LastProbeAt = &afterReading
					_, w, err := admission(t.Context(), tx, a, manual, afterReading, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
					if err != nil {
						return err
					}
					if source == "estimate" && (w == nil || w.Code != "vendor") {
						t.Fatalf("estimate erased stop: %+v", w)
					}
					if source == "harness" && w != nil {
						t.Fatalf("explicit recovery did not clear stop: %+v", w)
					}
				}
				return nil
			})
		})
	}
}

func TestAdmissionScheduleClockAndRunNowFences(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "clock", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"clock","harness":"codex","daemon_id":"daemon-a","label":"Main"}`, 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule("Europe/Vienna")
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	loc, _ := time.LoadLocation(s.Timezone)
	now := time.Date(2026, 9, 29, 23, 10, 0, 0, loc)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		a, err := getAccount(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		a.LastProbeAt = &now
		_, wait, err := admission(t.Context(), tx, a, nil, now, 0, runRow{Purpose: "managed"}, false)
		if err != nil {
			return err
		}
		if wait == nil || wait.Code != "schedule" || !wait.RunNowAllowed || wait.Until == nil || wait.Until.In(loc).Hour() != 8 {
			t.Fatalf("23:10: %+v", wait)
		}
		for _, tc := range []struct {
			name  string
			state string
			slots int
			probe bool
			want  string
		}{
			{"now", "available", 0, true, ""}, {"capacity", "available", 1, true, "capacity"}, {"drain", "draining", 0, true, "state"}, {"offline", "available", 0, false, "offline"},
		} {
			b := a
			b.State = tc.state
			if !tc.probe {
				b.LastProbeAt = timePtr(now.Add(-time.Hour))
			}
			_, w, err := admission(t.Context(), tx, b, nil, now, tc.slots, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
			if err != nil {
				return err
			}
			got := ""
			if w != nil {
				got = w.Code
			}
			if got != tc.want {
				t.Fatalf("%s: %s != %s", tc.name, got, tc.want)
			}
		}
		// A deliberate hold is not a schedule exception.
		s.Override = "hold"
		if _, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=$2::jsonb WHERE account_id=$1`, a.ID, encoded(t, s)); err != nil {
			return err
		}
		_, w, err := admission(t.Context(), tx, a, nil, now, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
		if err != nil {
			return err
		}
		if w == nil || w.Code != "hold" || w.RunNowAllowed {
			t.Fatalf("hold lost: %+v", w)
		}
		// A sign-in failure has an actionable reason rather than generic offline.
		no := false
		a.LastProbeOK = &no
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_failure='auth_failed' WHERE id=$1`, a.ID); err != nil {
			return err
		}
		_, w, err = admission(t.Context(), tx, a, nil, now, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
		if err != nil {
			return err
		}
		if w == nil || w.Code != "sign_in" || w.RunNowAllowed {
			t.Fatalf("sign-in lost: %+v", w)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

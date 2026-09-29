// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
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
					_, err := tx.Exec(t.Context(), `UPDATE model_profiles SET harness='claude' WHERE id=$1`, profile)
					return err
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
			s.Override = "sprint"
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
				_, err := tx.Exec(t.Context(), `UPDATE model_profiles SET harness=$2 WHERE id=$1`, profile, harness)
				return err
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
				return err
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
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

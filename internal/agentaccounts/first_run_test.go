// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
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
				// A content-free vendor stop waits out a bounded backoff. Run now
				// does not skip it, and the backoff is not a vendor window.
				if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2 WHERE id=$1`, run, a.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, run, now); err != nil {
					return err
				}
				a.LastProbeAt = &now
				_, w, err := admission(t.Context(), tx, a, nil, now, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
				if err != nil {
					return err
				}
				if w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(now.Add(vendorStopBackoff)) {
					t.Fatalf("vendor backoff: %+v", w)
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
					if source == "estimate" && (w == nil || w.Code != "vendor" || w.RunNowAllowed) {
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

func TestVendorDenialRecoversOnceAfterReset(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			reset(t)
			person := makePrincipal(t, "recover-"+harness, "person", "Ada", []string{"admin"})
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
					return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'recover-claude','1','claude','anthropic','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
				})
			}
			token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
			mod := accountsMod()
			var a Account
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "recover", "harness": harness, "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
			probe := func(g string) {
				callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", encoded(t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": g, "available": true}), 200, nil)
			}
			probe("g1")
			s := capacity.DefaultSchedule()
			for i := range s.Week {
				s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
			}
			callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
			now := time.Now().UTC().Truncate(time.Microsecond)
			readAt := now.Add(-2 * time.Hour)
			fiveReset := now.Add(-90 * time.Minute)
			weeklyReset := now.Add(-30 * time.Minute)
			no := false
			yes := true
			deny := func(kind string, minutes int, reset time.Time, at time.Time, allowed bool) capacity.Reading {
				flag := &no
				if allowed {
					flag = &yes
				}
				return capacity.Reading{WindowKind: kind, WindowMinutes: minutes, UsedPercent: 100, ReadAt: at, ResetsAt: reset, Source: "harness", OrdinaryUsageAllowed: flag}
			}
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
				deny("5h", 300, fiveReset, readAt, false),
				deny("weekly", 10080, weeklyReset, readAt, false),
			}}), 204, nil)
			// An estimate is not vendor recovery.
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
				{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 100, ReadAt: readAt.Add(time.Minute), ResetsAt: weeklyReset, Source: "estimate", OrdinaryUsageAllowed: &yes},
			}}), 204, nil)
			between := fiveReset.Add(time.Minute)
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
				loaded, err := getAccount(t.Context(), tx, a.ID)
				if err != nil {
					return err
				}
				loaded.LastProbeAt = &between
				_, w, err := admission(t.Context(), tx, loaded, loaded.Windows, between, 0, runRow{Purpose: "managed"}, false)
				if err != nil {
					return err
				}
				if w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(weeklyReset) {
					t.Fatalf("latest denying reset: %+v", w)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			first := insertRun(t, person, runner, profile)
			mustRoute(t, mod, runner, token, first, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			second := insertRun(t, person, runner, profile)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			probe("g2")
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			seed(func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, first, "", "") })
			probe("g1")
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			probe("g2")
			mustRoute(t, mod, runner, token, second, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			seed(func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, second, "", "") })
			probe("g2")
			allowAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
			allowReset := allowAt.Add(time.Hour)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
				{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 10, ReadAt: allowAt, ResetsAt: allowReset, Source: "harness", OrdinaryUsageAllowed: &yes},
				{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 10, ReadAt: allowAt, ResetsAt: allowReset, Source: "harness", OrdinaryUsageAllowed: &yes},
			}}), 204, nil)
			third := insertRun(t, person, runner, profile)
			fourth := insertRun(t, person, runner, profile)
			mustRoute(t, mod, runner, token, third, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			mustRoute(t, mod, runner, token, fourth, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
			seed(func(tx pgx.Tx) error {
				if err := Release(t.Context(), tx, runner, third, "", ""); err != nil {
					return err
				}
				return Release(t.Context(), tx, runner, fourth, "", "")
			})
			probe("g2")
			again := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
			future := again.Add(2 * time.Hour)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
				deny("5h", 300, future, again, false),
			}}), 204, nil)
			blocked := insertRun(t, person, runner, profile)
			callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, blocked, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
				loaded, err := getAccount(t.Context(), tx, a.ID)
				if err != nil {
					return err
				}
				clock := time.Now().UTC()
				loaded.LastProbeAt = &clock
				_, w, err := admission(t.Context(), tx, loaded, loaded.Windows, clock, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
				if err != nil {
					return err
				}
				if w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(future) {
					t.Fatalf("fresh denial: %+v", w)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBlindNightRunsDoNotCountTowardDailyLimit(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "blind-night", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"night","harness":"grok","daemon_id":"daemon-a","label":"Main","max_parallel_runs":2}`, 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule()
	s.Timezone = "UTC"
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	night := time.Date(2026, 9, 29, 0, 30, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		insertBlindAttempt(t, person, a.ID, insertRun(t, person, runner, profile), night.Add(time.Duration(i)*time.Minute))
	}
	admit := func(at time.Time, slots int) *CapacityWait {
		t.Helper()
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			loaded.LastProbeAt = &at
			_, wait, err = admission(t.Context(), tx, loaded, nil, at, slots, runRow{Purpose: "managed"}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return wait
	}
	morning := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	if w := admit(morning, 0); w != nil {
		t.Fatalf("night runs consumed the daytime allowance: %+v", w)
	}
	for _, hour := range []int{10, 11, 12} {
		insertBlindAttempt(t, person, a.ID, insertRun(t, person, runner, profile), time.Date(2026, 9, 29, hour, 0, 0, 0, time.UTC))
	}
	afternoon := time.Date(2026, 9, 29, 13, 0, 0, 0, time.UTC)
	if w := admit(afternoon, 0); w == nil || w.Code != "allowance" || w.RunNowAllowed {
		t.Fatalf("three daytime runs: %+v", w)
	}
}

func TestBlindVendorBackoffAdmitsOneRecovery(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "blind-recover", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"recover","harness":"cursor","daemon_id":"daemon-a","label":"Main","max_parallel_runs":2}`, 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule()
	s.Timezone = "UTC"
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	for _, hour := range []int{10, 11, 12} {
		insertBlindAttempt(t, person, a.ID, insertRun(t, person, runner, profile), time.Date(2026, 9, 29, hour, 0, 0, 0, time.UTC))
	}
	stop := time.Date(2026, 9, 29, 12, 30, 0, 0, time.UTC)
	cause := insertRun(t, person, runner, profile)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2 WHERE id=$1`, cause, a.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, cause, stop)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	admit := func(at time.Time, slots int) *CapacityWait {
		t.Helper()
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			loaded.LastProbeAt = &at
			_, wait, err = admission(t.Context(), tx, loaded, nil, at, slots, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return wait
	}
	inside := stop.Add(30 * time.Minute)
	if w := admit(inside, 0); w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(stop.Add(vendorStopBackoff)) {
		t.Fatalf("backoff: %+v", w)
	}
	// The three daytime attempts would block a normal run. The recovery probe
	// is not one of them, and a second slot still has to wait for that reading.
	due := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	if w := admit(due, 0); w != nil {
		t.Fatalf("recovery after backoff: %+v", w)
	}
	if w := admit(due, 1); w == nil || w.Code != "reading" {
		t.Fatalf("second recovery slot: %+v", w)
	}
}

func TestBlindCompletionClearsVendorStop(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "blind-clear", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	seed(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'clear-pi','1','pi','openai','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
	})
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"clear","harness":"pi","daemon_id":"daemon-a","label":"Main","max_parallel_runs":2}`, 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule()
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	stop := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	cause := insertRun(t, person, runner, profile)
	seed(func(tx pgx.Tx) error {
		// The causing run is already finished and started before the stop, so
		// it does not occupy a slot and does not count as later recovery.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2, status='cancelled' WHERE id=$1`, cause, a.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, cause, stop)
		return err
	})
	recovered := insertRun(t, person, runner, profile)
	mustRoute(t, mod, runner, token, recovered, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	seed(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed', started_at=$2 WHERE id=$1`, recovered, stop.Add(time.Minute)); err != nil {
			return err
		}
		return Release(t.Context(), tx, runner, recovered, "", "")
	})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		loaded, err := getAccount(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		loaded.LastProbeAt = &now
		_, w, err := admission(t.Context(), tx, loaded, loaded.Windows, now, 0, runRow{Purpose: "managed"}, false)
		if err != nil {
			return err
		}
		if w != nil && w.Code == "vendor" {
			t.Fatalf("completion left the vendor stop: %+v", w)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHardLimitWaitPrecedesSchedule(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "hard-limit", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"hard","harness":"codex","daemon_id":"daemon-a","label":"Main"}`, 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule("Europe/Vienna")
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	loc, _ := time.LoadLocation(s.Timezone)
	now := time.Date(2026, 9, 29, 23, 10, 0, 0, loc)
	read := now.Add(-time.Minute)
	five := Window{ID: "five", AccountID: a.ID, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(4 * time.Hour), Unit: "percent", Allowance: 100, PaceModel: "unrestricted", capacityReadAt: &read, capacityAllowed: true, capacityKind: "5h", capacityBucket: "codex"}
	weekly := five
	weekly.ID = "week"
	weekly.StartsAt = now.Add(-48 * time.Hour)
	weekly.EndsAt = now.Add(48 * time.Hour)
	weekly.Used = 100
	weekly.capacityKind = "weekly"
	admit := func(windows []Window) *CapacityWait {
		t.Helper()
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			loaded.LastProbeAt = &now
			_, wait, err = admission(t.Context(), tx, loaded, windows, now, 0, runRow{Purpose: "managed"}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return wait
	}
	for _, windows := range [][]Window{{five, weekly}, {weekly, five}} {
		w := admit(windows)
		if w == nil || w.Code != "allowance" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(weekly.EndsAt) {
			t.Fatalf("hard limit: %+v", w)
		}
	}
	w := admit([]Window{five})
	if w == nil || w.Code != "schedule" || !w.RunNowAllowed || w.Until == nil || w.Until.In(loc).Hour() != 8 {
		t.Fatalf("schedule-only window: %+v", w)
	}
}

func TestNamedClaudeDenialDoesNotWaitForTheOtherWindow(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "named-window", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'named-claude','1','claude','anthropic','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
	}); err != nil {
		t.Fatal(err)
	}
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "named", "harness": "claude", "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", encoded(t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": "g1", "available": true}), 200, nil)
	s := capacity.DefaultSchedule()
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	now := time.Now().UTC().Truncate(time.Microsecond)
	readAt := now.Add(-20 * time.Minute)
	fiveReset := now.Add(2 * time.Hour)
	// The weekly window has already been open for an hour, and resets in just under seven days.
	weeklyReset := now.Add(7*24*time.Hour - time.Hour)
	no, yes := false, true
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
		{WindowKind: "5h", Bucket: "five_hour", WindowMinutes: 300, UsedPercent: 90, ReadAt: readAt, ResetsAt: fiveReset, Source: "harness", OrdinaryUsageAllowed: &no},
		{WindowKind: "weekly", Bucket: "seven_day", WindowMinutes: 10080, UsedPercent: 10, ReadAt: readAt, ResetsAt: weeklyReset, Source: "harness", OrdinaryUsageAllowed: &yes},
	}}), 204, nil)
	stop := readAt.Add(-time.Minute)
	cause := insertRun(t, person, runner, profile)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2, status='cancelled' WHERE id=$1`, cause, a.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, cause, stop)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, person, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_kind='weekly' AND capacity_allowed AND NOT capacity_retired`, a.ID); n != 1 {
		t.Fatal("weekly window fenced by the 5h denial", n)
	}
	admit := func(at time.Time, override string) ([]Window, *CapacityWait) {
		t.Helper()
		var windows []Window
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			loaded.LastProbeAt = &at
			windows, wait, err = admission(t.Context(), tx, loaded, loaded.Windows, at, 0, runRow{Purpose: "managed", CapacityOverride: override}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return windows, wait
	}
	if _, w := admit(now, "now"); w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(fiveReset) {
		t.Fatalf("named denial: %+v", w)
	}
	freshRead := now.Add(-5 * time.Minute)
	freshReset := now.Add(-time.Minute)
	// A weekly window that resets within the hour keeps today's share available, so pacing does not turn the peer into a schedule wait.
	weeklySoon := freshRead.Add(time.Hour)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
		{WindowKind: "5h", Bucket: "five_hour", WindowMinutes: 300, UsedPercent: 100, ReadAt: freshRead, ResetsAt: freshReset, Source: "harness", OrdinaryUsageAllowed: &no},
		{WindowKind: "weekly", Bucket: "seven_day", WindowMinutes: 10080, UsedPercent: 12, ReadAt: freshRead, ResetsAt: weeklySoon, Source: "harness", OrdinaryUsageAllowed: &yes},
	}}), 204, nil)
	windows, w := admit(now, "")
	if w != nil || len(windows) != 1 || windows[0].capacityKind != "weekly" {
		t.Fatalf("weekly after the 5h reset: %+v windows=%+v", w, windows)
	}
	routed := insertRun(t, person, runner, profile)
	mustRoute(t, mod, runner, token, routed, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	if n := scalar(t, person, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_bucket LIKE 'recover:%'`, a.ID); n != 0 {
		t.Fatal("open weekly started a recovery", n)
	}
}

func TestRecoveryWithoutReadingResumesFirstRun(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "reread", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'reread-claude','1','claude','anthropic','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
	}); err != nil {
		t.Fatal(err)
	}
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "reread", "harness": "claude", "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
	probe := func(g string) {
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", encoded(t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": g, "available": true}), 200, nil)
	}
	probe("g1")
	s := capacity.DefaultSchedule()
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	now := time.Now().UTC().Truncate(time.Microsecond)
	readAt := now.Add(-2 * time.Hour)
	no := false
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
		{WindowKind: "5h", Bucket: "five_hour", WindowMinutes: 300, UsedPercent: 100, ReadAt: readAt, ResetsAt: readAt.Add(time.Hour), Source: "harness", OrdinaryUsageAllowed: &no},
	}}), 204, nil)
	recovery := insertRun(t, person, runner, profile)
	mustRoute(t, mod, runner, token, recovery, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	started := readAt.Add(time.Minute)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed', started_at=$2 WHERE id=$1`, recovery, started); err != nil {
			return err
		}
		return Release(t.Context(), tx, runner, recovery, "", "")
	}); err != nil {
		t.Fatal(err)
	}
	var listed []accountCapacity
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/capacity", "", 200, &listed)
	var card *accountCapacity
	for i := range listed {
		if listed[i].AccountID == a.ID {
			card = &listed[i]
		}
	}
	if card == nil || !card.AwaitingReading || len(card.Windows) != 0 || card.LimitingReset != nil {
		t.Fatalf("card after recovery without a reading: %+v", card)
	}
	admit := func(slots int) ([]Window, *CapacityWait) {
		t.Helper()
		var windows []Window
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			clock := time.Now().UTC()
			loaded.LastProbeAt = &clock
			windows, wait, err = admission(t.Context(), tx, loaded, loaded.Windows, clock, slots, runRow{Purpose: "managed"}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return windows, wait
	}
	if _, w := admit(1); w == nil || w.Code != "reading" || w.ReadAt != nil {
		t.Fatalf("second slot before the reread grant: %+v", w)
	}
	if n := scalar(t, person, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_bucket LIKE 'reread:%'`, a.ID); n != 0 {
		t.Fatal("slot wait persisted a reread", n)
	}
	windows, w := admit(0)
	if w != nil || len(windows) != 1 || !strings.HasPrefix(windows[0].capacityBucket, "reread:g1:") {
		t.Fatalf("reread grant: %+v windows=%+v", w, windows)
	}
	next := insertRun(t, person, runner, profile)
	mustRoute(t, mod, runner, token, next, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	if _, w := admit(0); w == nil || w.Code != "reading" || w.ReadAt != nil {
		t.Fatalf("spent reread: %+v", w)
	}
	again := insertRun(t, person, runner, profile)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, again, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
	if n := scalar(t, person, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_bucket LIKE 'reread:%'`, a.ID); n != 1 {
		t.Fatal("reread duplicated", n)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		return Release(t.Context(), tx, runner, next, "", "")
	}); err != nil {
		t.Fatal(err)
	}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, again, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
	probe("g2")
	mustRoute(t, mod, runner, token, again, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	if n := scalar(t, person, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_bucket LIKE 'reread:g2:%'`, a.ID); n != 1 {
		t.Fatal("new generation did not reread", n)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed', started_at=$2 WHERE id=$1`, again, time.Now().UTC().Add(-time.Minute)); err != nil {
			return err
		}
		return Release(t.Context(), tx, runner, again, "", "")
	}); err != nil {
		t.Fatal(err)
	}
	yes := true
	allowAt := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
		{WindowKind: "weekly", Bucket: "seven_day", WindowMinutes: 10080, UsedPercent: 10, ReadAt: allowAt, ResetsAt: allowAt.Add(48 * time.Hour), Source: "harness", OrdinaryUsageAllowed: &yes},
	}}), 204, nil)
	listed = nil
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/capacity", "", 200, &listed)
	card = nil
	for i := range listed {
		if listed[i].AccountID == a.ID {
			card = &listed[i]
		}
	}
	if card == nil || card.AwaitingReading || len(card.Windows) == 0 {
		t.Fatalf("reading after reread: %+v", card)
	}
	// Sprint keeps the whole remainder. A round-the-clock week still gives a
	// multi-day window only the hours left today, which is below two
	// reservations late in the day.
	s.Override = "sprint"
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	measured := insertRun(t, person, runner, profile)
	other := insertRun(t, person, runner, profile)
	mustRoute(t, mod, runner, token, measured, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	mustRoute(t, mod, runner, token, other, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
}

func TestClaudeUnnamedStopUsesVendorBackoff(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "unnamed-stop", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'unnamed-claude','1','claude','anthropic','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
	}); err != nil {
		t.Fatal(err)
	}
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "unnamed", "harness": "claude", "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", encoded(t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": "g1", "available": true}), 200, nil)
	s := capacity.DefaultSchedule()
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	stop := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Microsecond)
	cause := insertRun(t, person, runner, profile)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2, status='cancelled' WHERE id=$1`, cause, a.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, cause, stop)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	admit := func() *CapacityWait {
		t.Helper()
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			clock := time.Now().UTC()
			loaded.LastProbeAt = &clock
			_, wait, err = admission(t.Context(), tx, loaded, loaded.Windows, clock, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return wait
	}
	if w := admit(); w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(stop.Add(vendorStopBackoff)) {
		t.Fatalf("unnamed stop: %+v", w)
	}
	cleared := insertRun(t, person, runner, profile)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2, status='completed', started_at=$3 WHERE id=$1`, cleared, a.ID, stop.Add(time.Minute))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := admit(); w != nil && w.Code == "vendor" {
		t.Fatalf("completion left the unnamed stop: %+v", w)
	}
}

func TestUnnamedStopAfterClearedNamedDenialKeepsBackoff(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "cleared-named", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'cleared-named','1','claude','anthropic','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
	}); err != nil {
		t.Fatal(err)
	}
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "cleared", "harness": "claude", "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
	probe := func(g string) {
		t.Helper()
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", encoded(t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": g, "available": true}), 200, nil)
	}
	probe("g1")
	s := capacity.DefaultSchedule()
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	now := time.Now().UTC().Truncate(time.Microsecond)
	readAt := now.Add(-3 * time.Hour)
	no := false
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{
		{WindowKind: "5h", Bucket: "five_hour", WindowMinutes: 300, UsedPercent: 100, ReadAt: readAt, ResetsAt: now.Add(-2 * time.Hour), Source: "harness", OrdinaryUsageAllowed: &no},
	}}), 204, nil)
	recovery := insertRun(t, person, runner, profile)
	mustRoute(t, mod, runner, token, recovery, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed', started_at=$2 WHERE id=$1`, recovery, readAt.Add(time.Minute)); err != nil {
			return err
		}
		return Release(t.Context(), tx, runner, recovery, "", "")
	}); err != nil {
		t.Fatal(err)
	}
	admit := func(at time.Time) ([]Window, *CapacityWait) {
		t.Helper()
		var windows []Window
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			loaded.LastProbeAt = &at
			windows, wait, err = admission(t.Context(), tx, loaded, loaded.Windows, at, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return windows, wait
	}
	windows, w := admit(now)
	if w != nil || len(windows) != 1 || !strings.HasPrefix(windows[0].capacityBucket, "reread:g1:") {
		t.Fatalf("cleared named denial should reread: %+v windows=%+v", w, windows)
	}
	stop := now.Add(-30 * time.Minute)
	cause := insertRun(t, person, runner, profile)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2, status='cancelled' WHERE id=$1`, cause, a.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, cause, stop)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refuse := func(at time.Time) {
		t.Helper()
		_, w := admit(at)
		if w == nil || w.Code != "vendor" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(stop.Add(vendorStopBackoff)) {
			t.Fatalf("unnamed stop at %s: %+v", at.Format(time.RFC3339), w)
		}
	}
	refuse(now)
	refuse(stop.Add(vendorStopBackoff - time.Millisecond))
	blocked := insertRun(t, person, runner, profile)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, blocked, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
	probe("g2")
	refuse(time.Now().UTC())
	restarted := insertRun(t, person, runner, profile)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, restarted, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
	if n := scalar(t, person, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_bucket LIKE 'reread:%'`, a.ID); n != 0 {
		t.Fatal("unnamed stop minted a reread", n)
	}
	opened, w := admit(stop.Add(vendorStopBackoff))
	if w != nil && w.Code == "vendor" {
		t.Fatalf("backoff lasted past an hour: %+v", w)
	}
	if w != nil || len(opened) != 1 || !strings.HasPrefix(opened[0].capacityBucket, "recover:g2:") {
		t.Fatalf("hour ended on the unnamed stop: %+v windows=%+v", w, opened)
	}
}

func TestBlindSpentRecoveryDoesNotSayReading(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "blind-spent", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	profile := codexProfile(t, person)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'spent-cursor','1','cursor','openai','test','high','strong') RETURNING id::text`, person.TenantID).Scan(&profile)
	}); err != nil {
		t.Fatal(err)
	}
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "spent", "harness": "cursor", "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": 2}), 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", encoded(t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": "g1", "available": true}), 200, nil)
	s := capacity.DefaultSchedule()
	s.Timezone = "UTC"
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	stop := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	cause := insertRun(t, person, runner, profile)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2, status='cancelled' WHERE id=$1`, cause, a.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, person.TenantID, cause, stop)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	recovery := insertRun(t, person, runner, profile)
	mustRoute(t, mod, runner, token, recovery, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		return Release(t.Context(), tx, runner, recovery, "", "")
	}); err != nil {
		t.Fatal(err)
	}
	admit := func(at time.Time) *CapacityWait {
		t.Helper()
		var wait *CapacityWait
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			loaded, err := getAccount(t.Context(), tx, a.ID)
			if err != nil {
				return err
			}
			loaded.LastProbeAt = &at
			_, wait, err = admission(t.Context(), tx, loaded, loaded.Windows, at, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return wait
	}
	day := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	if w := admit(day); w == nil || w.Code != "reading" || w.ReadAt != nil || w.Timezone != "UTC" || w.RunNowAllowed {
		t.Fatalf("daytime spent grant: %+v", w)
	}
	night := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	next := s.NextStart(night.Add(time.Second), false)
	if w := admit(night); w == nil || w.Code != "schedule" || w.RunNowAllowed || w.Until == nil || next == nil || !w.Until.Equal(*next) {
		t.Fatalf("night spent grant: %+v next=%v", w, next)
	}
	held := s
	held.Override = "hold"
	until := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	held.OverrideUntil = &until
	if w := blindAfterRecovery(held, day); w == nil || w.Code != "hold" || w.RunNowAllowed || w.Until == nil || !w.Until.Equal(until) {
		t.Fatalf("hold after spent grant: %+v", w)
	}
}

func insertBlindAttempt(t *testing.T, person tenant.Principal, accountID, runID string, start time.Time) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2 WHERE id=$1`, runID, accountID); err != nil {
			return err
		}
		var windowID string
		err := tx.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(
 tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,burst_ratio,capacity_kind,capacity_read_at,capacity_allowed,capacity_source)
 VALUES($1,$2,$3,$4,'percent',1,'unrestricted',0,'blind',$3,true,'estimate') RETURNING id::text`,
			person.TenantID, accountID, start, start.Add(5*time.Minute)).Scan(&windowID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units) VALUES($1,$2,$3,1)`, person.TenantID, runID, windowID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

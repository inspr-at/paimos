// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestLimitPeriodDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		month time.Month
		day   int
		hours time.Duration
	}{{time.March, 29, 23}, {time.October, 25, 25}} {
		now := time.Date(2026, tt.month, tt.day, 12, 0, 0, 0, loc)
		start, end := limitPeriod("day", loc.String(), now)
		if start.Hour() != 0 || end.Hour() != 0 || end.Sub(start) != tt.hours*time.Hour || end.Day() != tt.day+1 {
			t.Fatalf("day %v: %v to %v", now, start, end)
		}
		for _, period := range []string{"week", "month"} {
			start, end = limitPeriod(period, loc.String(), now)
			if start.Hour() != 0 || end.Hour() != 0 || now.Before(start) || !now.Before(end) {
				t.Fatalf("%s: %v to %v", period, start, end)
			}
		}
	}
}

func TestPercentLimitSurvivesVendorResetAndRefresh(t *testing.T) {
	f := limitWorld(t, "limit-reset", 3)
	callStatus(t, accountsMod(), &f.admin, "", "PUT", "/api/agent-accounts/"+f.account.ID+"/limit", `{"amount":20,"unit":"percent","period":"day"}`, 200, nil)
	loc, _ := time.LoadLocation("Europe/Vienna")
	now := time.Date(2026, 9, 29, 15, 0, 0, 0, loc)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		// 20% before the 13:00 reset, another 20% after: the day consumed 40%.
		for _, v := range []struct{ hour, minute, used, reset int }{{8, 1, 0, 13}, {12, 55, 20, 13}, {13, 1, 0, 18}, {14, 59, 20, 18}} {
			_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'5h',300,$3,$4,$5,'harness')`, f.admin.TenantID, f.account.ID, v.used, time.Date(2026, 9, 29, v.reset, 0, 0, 0, loc), time.Date(2026, 9, 29, v.hour, v.minute, 0, 0, loc))
			if err != nil {
				return err
			}
		}
		s := capacity.DefaultSchedule("Europe/Vienna")
		a, err := getAccount(t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		for _, at := range []time.Time{time.Date(2026, 9, 29, 13, 2, 0, 0, loc), now} {
			for _, kind := range []string{"5h", "refresh"} {
				w := Window{AccountID: a.ID, Unit: "percent", Allowance: 100, StartsAt: at.Add(-time.Hour), EndsAt: at.Add(time.Hour), capacityKind: kind, capacityReadAt: &at, capacityAllowed: true}
				wait, err := limitWait(t.Context(), tx, a, []Window{w}, at, runRow{Purpose: "managed", CapacityOverride: "now"}, false, s)
				if err != nil {
					return err
				}
				if wait == nil || wait.Code != "allowance" || wait.RunNowAllowed {
					t.Fatalf("%s at %v bypassed daily cap: %+v", kind, at, wait)
				}
			}
		}
		start, _ := limitPeriod("day", s.Timezone, now)
		used, err := percentUsed(t.Context(), tx, a.ID, start, now)
		if err != nil {
			return err
		}
		if used != 40 {
			t.Fatalf("reset refunded usage: %v", used)
		}
		tomorrow := now.AddDate(0, 0, 1)
		start, _ = limitPeriod("day", s.Timezone, tomorrow)
		used, err = percentUsed(t.Context(), tx, a.ID, start, tomorrow)
		if err != nil {
			return err
		}
		if used != 0 {
			t.Fatalf("calendar period did not reset: %v", used)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLimitConcurrentReservationsAndClaim(t *testing.T) {
	for _, unit := range []string{"percent", "runs", "requests", "tokens", "cost_micros"} {
		t.Run(unit, func(t *testing.T) {
			f := limitWorld(t, "limit-concurrent-"+unit, 3)
			now := time.Now().UTC().Add(-time.Second)
			amount := int64(1)
			if unit == "tokens" {
				amount = 100_000
			}
			if unit == "cost_micros" {
				amount = 1_000_000
				seedPricedRun(t, f, amount)
			}
			f.report(t, 10, now, now.Add(48*time.Hour))
			callStatus(t, accountsMod(), &f.admin, "", "PUT", "/api/agent-accounts/"+f.account.ID+"/limit", fmt.Sprintf(`{"amount":%d,"unit":%q,"period":"day"}`, amount, unit), 200, nil)
			runs := []string{insertRun(t, f.admin, f.runner, f.profile), insertRun(t, f.admin, f.runner, f.profile), insertRun(t, f.admin, f.runner, f.profile)}
			bodies := make([]string, len(runs))
			for i, id := range runs {
				bodies[i] = routeBody(t, id, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
			}
			start := make(chan struct{})
			statuses := make([]int, len(runs))
			var wg sync.WaitGroup
			for i := range runs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					statuses[i], _ = call(t, accountsMod(), &f.runner, f.token, "POST", "/api/agent-accounts/route", bodies[i])
				}(i)
			}
			close(start)
			wg.Wait()
			admitted := ""
			count := 0
			for i, status := range statuses {
				if status == 200 {
					count++
					admitted = runs[i]
				} else if status != 409 {
					t.Fatalf("unexpected status: %v", statuses)
				}
			}
			if count != 1 {
				t.Fatalf("want one admission with three slots: %v", statuses)
			}
			// Rechecking at claim includes the saved incoming estimate exactly once.
			err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error { return ValidateReservedCapacity(t.Context(), tx, admitted, f.account.ID) })
			if err != nil {
				t.Fatal(err)
			}
			if got := f.capacity(t).Limit; got == nil || got.Used != float64(amount) {
				t.Fatalf("outstanding hold missing: %+v", got)
			}
			// A cancelled, never-started reservation returns its allowance.
			err = db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error { return Release(t.Context(), tx, f.runner, admitted, "", "") })
			if err != nil {
				t.Fatal(err)
			}
			f.route(t, 200)
		})
	}
}

func seedPricedRun(t *testing.T, f limitFixture, cost int64) string {
	t.Helper()
	id := insertRun(t, f.admin, f.runner, f.profile)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2,status='completed',started_at=clock_timestamp()-interval '40 days',ended_at=clock_timestamp()-interval '39 days' WHERE id=$1`, id, f.account.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,cost_micros_delta,at) VALUES($1,$2,1,'usage',$3,clock_timestamp()-interval '40 days')`, f.admin.TenantID, id, cost)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCostLimitRequiresPricedRunAccounting(t *testing.T) {
	f := limitWorld(t, "limit-priced", 3)
	path := "/api/agent-accounts/" + f.account.ID + "/limit"
	status, raw := call(t, accountsMod(), &f.admin, "", "PUT", path, `{"amount":1000000,"unit":"cost_micros","period":"day"}`)
	if status != 422 || !strings.Contains(string(raw), "unsupported_limit_unit") {
		t.Fatalf("unsupported cost: %d %s", status, raw)
	}
	if f.capacity(t).CostLimitSupported {
		t.Fatal("unpriced account offers dollars")
	}
	seedPricedRun(t, f, 1_000_000)
	callStatus(t, accountsMod(), &f.admin, "", "PUT", path, `{"amount":1000000,"unit":"cost_micros","period":"day"}`, 200, nil)
	if !f.capacity(t).CostLimitSupported {
		t.Fatal("priced account has no dollar support")
	}
	now := time.Now().Add(-time.Second)
	f.report(t, 10, now, now.Add(48*time.Hour))
	run, _ := f.route(t, 200)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		// A run spanning midnight finishes without cost telemetry. Its final
		// period must not treat it as free either.
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='completed',started_at=clock_timestamp()-interval '1 day',ended_at=clock_timestamp() WHERE id=$1`, run)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.route(t, 409)
}

func TestRepeatPreservesOriginalAndExistingCaps(t *testing.T) {
	f := limitWorld(t, "repeat-preserve", 3)
	mod := accountsMod()
	now := time.Now().UTC().Truncate(time.Second)
	f.report(t, 10, now.Add(-time.Minute), now.Add(48*time.Hour))
	var old Window
	callStatus(t, mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows", windowBody(now.Add(-time.Hour), now.Add(48*time.Hour), "requests", 1, "unrestricted"), 201, &old)
	run, _ := f.route(t, 200)
	path := "/api/agent-accounts/" + f.account.ID + "/windows/" + old.ID + "/repeat"
	callStatus(t, mod, &f.admin, "", "POST", path, "", 200, nil)
	f.route(t, 409)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error { return ValidateReservedCapacity(t.Context(), tx, run, f.account.ID) })
	if err != nil {
		t.Fatalf("repeat stranded its existing reservation: %v", err)
	}
	// A second old window cannot silently replace the first repeating cap.
	var other Window
	callStatus(t, mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows", windowBody(now.Add(49*time.Hour), now.Add(72*time.Hour), "requests", 100, "unrestricted"), 201, &other)
	callStatus(t, mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows/"+other.ID+"/repeat", "", 409, nil)
	if f.capacity(t).Limit.Amount != 1 {
		t.Fatal("repeat weakened existing rule")
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_allowance_windows WHERE id=$1 AND removed_at IS NULL`, other.ID); n != 1 {
		t.Fatal("rejected repeat changed the window")
	}
}

func TestLearnedLimitEstimateAndOutstandingUsage(t *testing.T) {
	f := limitWorld(t, "limit-learned", 3)
	old := seedPricedRun(t, f, 100)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE run_telemetry SET turn_count_delta=5 WHERE run_id=$1`, old)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Second)
	f.report(t, 10, now, now.Add(48*time.Hour))
	path := "/api/agent-accounts/" + f.account.ID + "/limit"
	callStatus(t, accountsMod(), &f.admin, "", "PUT", path, `{"amount":9,"unit":"requests","period":"day"}`, 200, nil)
	run, _ := f.route(t, 200) // Caller asks for one, history reserves five.
	f.route(t, 409)
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running',started_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, run); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,turn_count_delta) VALUES($1,$2,1,'usage',2)`, f.admin.TenantID, run)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := f.capacity(t); c.Limit.Used != 5 {
		t.Fatalf("recorded 2 plus remaining 3 should be 5: %+v", c.Limit)
	}
	f.route(t, 409) // A claimed run keeps the rest of its estimate.
	callStatus(t, accountsMod(), &f.admin, "", "PUT", path, `{"amount":10,"unit":"requests","period":"day"}`, 200, nil)
	f.route(t, 200)
}

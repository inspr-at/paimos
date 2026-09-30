// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type limitFixture struct {
	admin, runner tenant.Principal
	token         string
	profile       string
	account       Account
	mod           httpapi.Module
}

// fixedClockModule injects the same instant into every account request, including
// probes, reading validation, projections and routing. No global clock is changed.
type fixedClockModule struct {
	httpapi.Module
	at time.Time
}

func (m fixedClockModule) Mount(mux *http.ServeMux) {
	inner := http.NewServeMux()
	m.Module.Mount(inner)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clockKey{}, m.at)
		inner.ServeHTTP(w, r.WithContext(ctx))
	}))
}

// limitWorld is one Codex account with a fresh probe and an always-on
// account schedule without Keep for you.
func limitWorld(t *testing.T, slug string, parallel int) limitFixture {
	t.Helper()
	return limitWorldAt(t, slug, parallel, time.Time{})
}

func limitWorldAt(t *testing.T, slug string, parallel int, at time.Time) limitFixture {
	t.Helper()
	reset(t)
	f := limitFixture{admin: makePrincipal(t, slug, "person", "Ada", []string{"admin"})}
	f.runner = addPrincipal(t, f.admin.TenantID, "agent", "runner", nil)
	f.token = issueKey(t, f.runner, []string{"account.manage", "account.probe"})
	f.profile = codexProfile(t, f.admin)
	mod := accountsMod()
	if !at.IsZero() {
		mod = fixedClockModule{Module: mod, at: at}
	}
	f.mod = mod
	callStatus(t, mod, &f.runner, f.token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "main", "harness": "codex", "daemon_id": "daemon-a", "label": "Main", "max_parallel_runs": parallel}), 201, &f.account)
	callStatus(t, mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule("Europe/Vienna")
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	s.Reserve = capacity.ReserveOff
	callStatus(t, mod, &f.admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: f.account.ID, Schedule: &s}), 204, nil)
	return f
}

func (f limitFixture) report(t *testing.T, used float64, readAt, resets time.Time) {
	t.Helper()
	r := capacity.Reading{WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: used, ResetsAt: resets, ReadAt: readAt, Source: "harness"}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
}

func (f limitFixture) route(t *testing.T, status int) (string, RouteResult) {
	t.Helper()
	run := insertRun(t, f.admin, f.runner, f.profile)
	var out RouteResult
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}), status, &out)
	return run, out
}

func (f limitFixture) capacity(t *testing.T) accountCapacity {
	t.Helper()
	var out []accountCapacity
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &out)
	if len(out) != 1 {
		t.Fatalf("capacity: %+v", out)
	}
	return out[0]
}

func (f limitFixture) setByYou(t *testing.T) []Window {
	t.Helper()
	var accounts []Account
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts", "", 200, &accounts)
	out := []Window{}
	for _, w := range accounts[0].Windows {
		if w.SetByYou {
			out = append(out, w)
		}
	}
	return out
}

// AEON-384 acceptance: an old manual window caps on top of the derived plan,
// readings keep updating next to it, and nothing about it is lost; Remove
// keeps the row.
func TestOldManualWindowCapsOnTopOfReadings(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{
		time.Date(2026, 9, 29, 9, 0, 0, 0, loc),
		time.Date(2026, 9, 29, 18, 0, 0, 0, loc),
		time.Date(2026, 9, 29, 23, 30, 0, 0, loc),
		time.Date(2026, 9, 30, 23, 30, 0, 0, loc),
	} {
		t.Run(at.Format("2006-01-02_15:04"), func(t *testing.T) {
			testOldManualWindowCapsOnTopOfReadings(t, at)
		})
	}
}

func testOldManualWindowCapsOnTopOfReadings(t *testing.T, at time.Time) {
	t.Helper()
	f := limitWorldAt(t, "manual-caps", 4, at)
	mod := f.mod
	now := at.Add(-time.Second)
	resets := now.Add(48 * time.Hour)
	// The window as pairing or the old Settings form left it: two requests,
	// with a burst the UI no longer shows.
	var old Window
	callStatus(t, mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows", `{"starts_at":"`+now.Add(-time.Hour).Format(time.RFC3339)+`","ends_at":"`+now.Add(30*24*time.Hour).Format(time.RFC3339)+`","unit":"requests","allowance":2,"pace_model":"unrestricted","burst_ratio":0.25}`, 201, &old)
	if !old.SetByYou {
		t.Fatal("a window set by hand is not marked set by you")
	}
	f.report(t, 10, now.Add(-time.Minute), resets)

	// Both bind: every route reserves on the manual window and the reading.
	firstRun, first := f.route(t, 200)
	units := map[string]bool{}
	for _, r := range first.Reservations {
		units[r.Unit] = true
	}
	if len(first.Reservations) != 2 || !units["requests"] || !units["percent"] {
		t.Fatalf("the reading no longer applies next to the manual window: %+v", first.Reservations)
	}
	// Readings keep updating: a vendor at 99% stops routing even though the
	// manual window still has room (it used to replace the readings).
	f.report(t, 99.5, now, resets)
	if c := f.capacity(t); len(c.Windows) != 1 || c.Windows[0].Reading.UsedPercent != 99.5 {
		t.Fatalf("the reading did not update next to the manual window: %+v", c.Windows)
	}
	f.route(t, 409)
	f.report(t, 20, now.Add(100*time.Millisecond), resets)
	if c := f.capacity(t); len(c.Windows) != 1 || c.Windows[0].UsageTodayKnown || c.Windows[0].Pacing.UsedTodayPercent != 10 || c.Windows[0].Pacing.AvailableNowPercent <= 2 {
		t.Fatalf("late readings must keep a daily share and their observed usage: %+v", c.Windows)
	}
	secondRun, _ := f.route(t, 200)
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, at)
	for _, run := range []string{firstRun, secondRun} {
		if err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
			return ValidateReservedCapacity(ctx, tx, run, f.account.ID)
		}); err != nil {
			t.Fatalf("claim after the fresh reading: %v", err)
		}
	}
	// The manual window caps the plan: two requests are held, the readings
	// have plenty left, and the third run waits.
	f.route(t, 409)
	var wait *CapacityWait
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		a, err := getAccount(t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		a.LastProbeAt = &at
		_, wait, err = admission(t.Context(), tx, a, a.Windows, at, 2, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Code != "allowance" || wait.RunNowAllowed || wait.Until == nil || !wait.Until.Equal(old.EndsAt) {
		t.Fatalf("a full manual window must wait for its end, Run now included: %+v", wait)
	}
	// Nothing is lost: every field of the old window is still there.
	kept := f.setByYou(t)
	if len(kept) != 1 || kept[0].ID != old.ID || kept[0].Allowance != 2 || kept[0].Reserved != 2 || kept[0].PaceModel != "unrestricted" || kept[0].BurstRatio != 0.25 || !kept[0].EndsAt.Equal(old.EndsAt) {
		t.Fatalf("old window changed: %+v", kept)
	}

	// Remove stops the cap and keeps the row with its ledger.
	callStatus(t, mod, &f.runner, f.token, "DELETE", "/api/agent-accounts/"+f.account.ID+"/windows/"+old.ID, "", 403, nil)
	callStatus(t, mod, &f.admin, "", "DELETE", "/api/agent-accounts/"+f.account.ID+"/windows/"+old.ID, "", 204, nil)
	callStatus(t, mod, &f.admin, "", "DELETE", "/api/agent-accounts/"+f.account.ID+"/windows/"+old.ID, "", 404, nil)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_allowance_windows WHERE id=$1 AND removed_at IS NOT NULL AND allowance=2 AND reserved=2 AND burst_ratio=0.25`, old.ID); n != 1 {
		t.Fatal("Remove lost the window's row")
	}
	if len(f.setByYou(t)) != 0 {
		t.Fatal("a removed window is still listed")
	}
	f.route(t, 200)
	// A vendor window cannot be removed as if it were set by hand.
	var derived string
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM account_allowance_windows WHERE account_id=$1 AND capacity_kind IS NOT NULL LIMIT 1`, f.account.ID).Scan(&derived)
	})
	if err != nil {
		t.Fatal(err)
	}
	callStatus(t, mod, &f.admin, "", "DELETE", "/api/agent-accounts/"+f.account.ID+"/windows/"+derived, "", 404, nil)
}

// AEON-464: late observations receive a daily share, but later consumption
// still spends it. Repeated projections must not replace the usage baseline.
func TestLateReadingDailyShareStillCapsObservedUsage(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 30, 23, 30, 0, 0, loc)
	resets := time.Date(2026, 10, 3, 0, 0, 0, 0, loc)
	f := limitWorldAt(t, "late-daily-share", 4, at)
	f.report(t, 10, at.Add(-30*time.Minute), resets)
	f.report(t, 20, at.Add(-2*time.Minute), resets)
	for i := 0; i < 2; i++ {
		c := f.capacity(t)
		p := c.Windows[0].Pacing
		// Three full scheduled days until reset: today's share is 90/3,
		// of which the observed 10 points have already been consumed.
		if c.Windows[0].UsageTodayKnown || p.BudgetPercent != 30 || p.UsedTodayPercent != 10 || p.AvailableNowPercent != 20 {
			t.Fatalf("daily share with an unknown boundary reading: %+v", c.Windows[0])
		}
	}
	f.route(t, 200)
	f.report(t, 60, at.Add(-time.Minute), resets)
	c := f.capacity(t)
	if p := c.Windows[0].Pacing; p.BudgetPercent != 30 || p.UsedTodayPercent != 50 || p.AvailableNowPercent != 0 {
		t.Fatalf("later usage must spend the original daily share: %+v", p)
	}
	f.route(t, 409)
	// At midnight the old period's observations no longer supply a baseline.
	// Its usage stays unknown, and the new day's share starts at the boundary.
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		s := capacity.DefaultSchedule("Europe/Vienna")
		for i := range s.Week {
			s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
		}
		s.Reserve = capacity.ReserveOff
		midnight := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
		v := capacity.Reading{WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: 60, ReadAt: midnight, ResetsAt: resets, Source: "harness"}
		p, known, err := readingPacing(t.Context(), tx, f.account.ID, v, midnight, s)
		if err != nil {
			return err
		}
		if known || p.UsedTodayPercent != 0 || p.BudgetPercent != 20 || p.AvailableNowPercent != 20 {
			t.Fatalf("new month/day inherited the previous period's usage: known=%v pacing=%+v", known, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// AEON-384 acceptance: the Advanced sentence round-trips, and it caps.
func TestAdvancedSentenceRoundTripsAndCaps(t *testing.T) {
	f := limitWorld(t, "advanced", 4)
	mod := accountsMod()
	path := "/api/agent-accounts/" + f.account.ID + "/limit"
	var rule LimitRule
	callStatus(t, mod, &f.admin, "", "PUT", path, `{"amount":20,"unit":"percent","period":"day"}`, 200, &rule)
	if rule.Amount != 20 || rule.Unit != "percent" || rule.Period != "day" || rule.AccountID != f.account.ID {
		t.Fatalf("rule: %+v", rule)
	}
	got := f.capacity(t).Limit
	if got == nil || got.ID != rule.ID || got.Amount != 20 || got.Unit != "percent" || got.Period != "day" || !got.PeriodEnd.After(time.Now()) {
		t.Fatalf("the sentence did not round-trip: %+v", got)
	}
	var again LimitRule
	callStatus(t, mod, &f.admin, "", "PUT", path, `{"amount":20,"unit":"percent","period":"day"}`, 200, &again)
	if again.ID != rule.ID {
		t.Fatal("an identical sentence was stored twice")
	}
	for _, bad := range []string{`{"amount":0,"unit":"percent","period":"day"}`, `{"amount":101,"unit":"percent","period":"day"}`, `{"amount":5,"unit":"micros","period":"day"}`, `{"amount":5,"unit":"runs","period":"year"}`, `{"amount":5,"unit":"runs","period":"day","pace_model":"steady"}`} {
		callStatus(t, mod, &f.admin, "", "PUT", path, bad, 400, nil)
	}
	callStatus(t, mod, &f.runner, f.token, "PUT", path, `{"amount":5,"unit":"runs","period":"day"}`, 403, nil)

	// Percent counts each vendor window's rise today (Vienna), your own use
	// included, on a fixed clock: from the last reading before midnight.
	loc, _ := time.LoadLocation("Europe/Vienna")
	noon := time.Date(2026, 9, 29, 14, 0, 0, 0, loc)
	resets := noon.Add(48 * time.Hour)
	admit := func(used int64) ([]Window, *CapacityWait) {
		t.Helper()
		var ws []Window
		var wait *CapacityWait
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
			for _, r := range []struct {
				at   time.Time
				used float64
			}{{noon.Add(-14*time.Hour - 10*time.Minute), 10}, {noon.Add(-5 * time.Hour), 18}, {noon.Add(-time.Minute).Add(time.Duration(used) * time.Millisecond), float64(used)}} {
				if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly',10080,$3,$4,$5,'harness') ON CONFLICT DO NOTHING`, f.admin.TenantID, f.account.ID, r.used, resets, r.at); err != nil {
					return err
				}
			}
			a, err := getAccount(t.Context(), tx, f.account.ID)
			if err != nil {
				return err
			}
			a.LastProbeAt = &noon
			read := noon.Add(-time.Minute)
			w := Window{AccountID: a.ID, StartsAt: resets.Add(-7 * 24 * time.Hour), EndsAt: resets, Unit: "percent", Allowance: 100, Used: used, PaceModel: "unrestricted", capacityReadAt: &read, capacityAllowed: true, capacityKind: "weekly"}
			ws, wait, err = admission(t.Context(), tx, a, []Window{w}, noon, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ws, wait
	}
	// 25% now, 10% at midnight: 15 of 20 used today, so 5 are left for agents
	// even though the plan (Run now: everything) would allow 75.
	ws, wait := admit(25)
	if wait != nil || len(ws) != 1 || ws[0].capacityBudget == nil || *ws[0].capacityBudget != 5 {
		t.Fatalf("the sentence must bind before the plan: %+v %+v", ws, wait)
	}
	// 31%: the day's 20 are used up; it waits for midnight, Run now included.
	_, wait = admit(31)
	midnight := time.Date(2026, 9, 30, 0, 0, 0, 0, loc)
	if wait == nil || wait.Code != "allowance" || wait.RunNowAllowed || wait.Until == nil || !wait.Until.Equal(midnight) {
		t.Fatalf("a used-up sentence waits for the end of the day: %+v", wait)
	}

	// Runs: the queued run holding the account counts.
	var runs LimitRule
	callStatus(t, mod, &f.admin, "", "PUT", path, `{"amount":1,"unit":"runs","period":"week"}`, 200, &runs)
	if runs.ID == rule.ID {
		t.Fatal("a changed sentence reused the old rule")
	}
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	f.report(t, 30, now, now.Add(49*time.Hour))
	f.route(t, 200)
	f.route(t, 409)
	if c := f.capacity(t); c.Limit == nil || c.Limit.Unit != "runs" || c.Limit.Used != 1 {
		t.Fatalf("runs this week: %+v", c.Limit)
	}
	callStatus(t, mod, &f.admin, "", "DELETE", path, "", 204, nil)
	callStatus(t, mod, &f.admin, "", "DELETE", path, "", 204, nil)
	if c := f.capacity(t); c.Limit != nil {
		t.Fatalf("a removed sentence still applies: %+v", c.Limit)
	}
	f.route(t, 200)
	// Replaced and removed sentences stay as history.
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_limit_rules WHERE account_id=$1 AND removed_at IS NOT NULL`, f.account.ID); n != 2 {
		t.Fatal("rule history", n)
	}
}

// Make this repeat turns an old window into the sentence and removes the
// window in the same step; the use counted this period stays counted.
func TestMakeThisRepeat(t *testing.T) {
	f := limitWorld(t, "repeat", 4)
	mod := accountsMod()
	now := time.Now().UTC().Truncate(time.Second)
	var old Window
	callStatus(t, mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows", windowBody(now.Add(-time.Hour), now.Add(30*24*time.Hour), "requests", 100, "unrestricted"), 201, &old)
	path := "/api/agent-accounts/" + f.account.ID + "/windows/" + old.ID + "/repeat"
	callStatus(t, mod, &f.runner, f.token, "POST", path, "", 403, nil)
	var rule LimitRule
	callStatus(t, mod, &f.admin, "", "POST", path, "", 200, &rule)
	if rule.Amount != 100 || rule.Unit != "requests" || rule.Period != "month" || rule.FromWindowID == nil || *rule.FromWindowID != old.ID {
		t.Fatalf("repeat: %+v", rule)
	}
	if len(f.setByYou(t)) != 0 {
		t.Fatal("the repeated window is still listed next to its sentence")
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_allowance_windows WHERE id=$1 AND removed_at IS NOT NULL AND allowance=100`, old.ID); n != 1 {
		t.Fatal("the repeated window's row was lost")
	}
	callStatus(t, mod, &f.admin, "", "POST", path, "", 404, nil)
	for _, d := range []struct {
		length time.Duration
		period string
	}{{24 * time.Hour, "day"}, {7 * 24 * time.Hour, "week"}, {31 * 24 * time.Hour, "month"}} {
		if got := periodFor(d.length); got != d.period {
			t.Fatalf("%v: %s", d.length, got)
		}
	}
	// Requests count run telemetry this month: 100 turns reach the sentence.
	f.report(t, 10, now.Add(-time.Minute), now.Add(48*time.Hour))
	run, _ := f.route(t, 200)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,turn_count_delta) VALUES($1,$2,1,'usage',100)`, f.admin.TenantID, run)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.route(t, 409)
	if c := f.capacity(t); c.Limit == nil || c.Limit.Used != 100 {
		t.Fatalf("requests this month: %+v", c.Limit)
	}
}

// Inline rename changes the label only; a legacy null model grant stays null.
func TestRenameKeepsModelGrants(t *testing.T) {
	f := limitWorld(t, "rename", 1)
	mod := accountsMod()
	path := "/api/agent-accounts/" + f.account.ID + "/label"
	callStatus(t, mod, &f.runner, f.token, "PUT", path, `{"label":"Spare"}`, 403, nil)
	callStatus(t, mod, &f.admin, "", "PUT", path, `{"label":"  "}`, 400, nil)
	callStatus(t, mod, &f.admin, "", "PUT", path, `{"label":"Spare","plan":"Pro"}`, 400, nil)
	var out Account
	callStatus(t, mod, &f.admin, "", "PUT", path, `{"label":" Spare "}`, 200, &out)
	if out.Label != "Spare" || out.AllowedProfileIDs != nil {
		t.Fatalf("rename: %+v", out)
	}
	if n := scalar(t, f.admin, `SELECT count(*) FROM agent_accounts WHERE id=$1 AND label='Spare' AND allowed_model_profile_ids IS NULL`, f.account.ID); n != 1 {
		t.Fatal("rename changed the model grants")
	}
}

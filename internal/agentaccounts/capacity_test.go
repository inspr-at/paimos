// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func encoded(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCapacityIngestRouteResetAndRLS(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "capacity", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	other := addPrincipal(t, admin.TenantID, "agent", "other", nil)
	foreign := makePrincipal(t, "foreign", "person", "Foreign", []string{"admin"})
	profile := codexProfile(t, admin)
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	var account Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"quota","harness":"codex","daemon_id":"daemon-a","label":"Codex","max_parallel_runs":2}`, 201, &account)
	ownFixtureAccount(t, admin, &account)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+account.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	r := capacity.Reading{WindowKind: "5h", Bucket: "codex", WindowMinutes: 300, UsedPercent: 41.2, ResetsAt: now.Add(time.Hour), ReadAt: now, Source: "harness", Phase: "start"}
	path := "/api/agent-accounts/" + account.ID + "/readings"
	body := encoded(t, readingsWrite{[]capacity.Reading{r}})
	callStatus(t, mod, &admin, "", "POST", path, body, 403, nil)
	callStatus(t, mod, &runner, issueKey(t, runner, []string{"account.read"}), "POST", path, body, 403, nil)
	callStatus(t, mod, &other, issueKey(t, other, []string{"account.probe"}), "POST", path, body, 403, nil)
	callStatus(t, mod, &runner, token, "POST", path, body, 204, nil)
	callStatus(t, mod, &runner, token, "POST", path, body, 204, nil)
	if n := scalar(t, admin, `SELECT count(*) FROM account_capacity_readings`); n != 1 {
		t.Fatal("duplicate readings", n)
	}
	if n := scalar(t, admin, `SELECT used FROM account_allowance_windows WHERE account_id=$1`, account.ID); n != 42 {
		t.Fatal("unsafe rounding", n)
	}
	if n := scalar(t, foreign, `SELECT count(*) FROM account_capacity_readings`); n != 0 {
		t.Fatal("RLS leaked readings")
	}
	callStatus(t, mod, &foreign, "", "GET", path, "", 404, nil)
	var legacy []Account
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts", "", 200, &legacy)
	if len(legacy) != 1 || len(legacy[0].Windows) != 1 || legacy[0].Windows[0].Provisional {
		t.Fatal("measured quota became unknown in account catalog")
	}
	var out []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &out)
	if len(out) != 1 || len(out[0].Windows) != 1 || out[0].Windows[0].Allowance != 100 || out[0].Windows[0].Reading.UsedPercent != 41.2 || out[0].Windows[0].Freshness != "fresh" {
		t.Fatalf("capacity %+v", out)
	}
	var history []capacity.Reading
	callStatus(t, mod, &admin, "", "GET", path, "", 200, &history)
	callStatus(t, mod, &runner, token, "GET", path, "", 200, nil)
	callStatus(t, mod, &other, issueKey(t, other, []string{"account.probe"}), "GET", path, "", 403, nil)
	if len(history) != 1 {
		t.Fatal(history)
	}
	schedule := capacity.DefaultSchedule()
	schedule.Override = "sprint"
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", account.ID, &schedule, false, ""}), 204, nil)
	run := insertRun(t, admin, runner, profile)
	route := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{account}, map[string]int64{"requests": 1})
	if len(route.Reservations) != 1 || route.Reservations[0].Unit != "percent" {
		t.Fatal(route)
	}
	secondRun := insertRun(t, admin, runner, profile)
	mustRoute(t, mod, runner, token, secondRun, "daemon-a", []Account{account}, map[string]int64{"requests": 1})
	// A new observation may consume the remaining quota even with a reservation.
	no := false
	r.UsedPercent = 100
	r.ReadAt = now.Add(time.Millisecond)
	r.OrdinaryUsageAllowed = &no
	r.Phase = "end"
	r.RunID = run
	callStatus(t, mod, &runner, token, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{account}, map[string]int64{"requests": 1}), 409, nil)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error { return Settle(t.Context(), tx, runner, run) })
	if err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, admin, `SELECT used FROM account_allowance_windows WHERE account_id=$1`, account.ID); n != 100 {
		t.Fatal("settlement overwrote observation", n)
	}
	if n := scalar(t, admin, `SELECT reserved FROM account_allowance_windows WHERE account_id=$1`, account.ID); n != 1 {
		t.Fatal("other reservation was changed", n)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if err := Settle(t.Context(), tx, runner, secondRun); err != nil {
			return err
		}
		if err := Release(t.Context(), tx, runner, run, "", ""); err != nil {
			return err
		}
		return Release(t.Context(), tx, runner, secondRun, "", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, admin, `SELECT reserved FROM account_allowance_windows WHERE account_id=$1`, account.ID); n != 0 {
		t.Fatal("reservation remained", n)
	}
	// Delayed readings must not undo a newer vendor denial.
	old := r
	old.ReadAt = now.Add(-time.Minute)
	old.UsedPercent = 0
	old.OrdinaryUsageAllowed = nil
	old.RunID = ""
	old.Phase = "update"
	callStatus(t, mod, &runner, token, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{old}}), 204, nil)
	if n := scalar(t, admin, `SELECT used FROM account_allowance_windows WHERE account_id=$1`, account.ID); n != 100 {
		t.Fatal("old reading replaced current", n)
	}
	// A reset derives a new period, retaining the original ledger and history.
	r.ReadAt = now.Add(2 * time.Millisecond)
	r.ResetsAt = now.Add(4 * time.Hour)
	r.UsedPercent = 5
	yes := true
	r.OrdinaryUsageAllowed = &yes
	r.RunID = ""
	r.Phase = "update"
	callStatus(t, mod, &runner, token, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
	if n := scalar(t, admin, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1`, account.ID); n != 2 {
		t.Fatal("reset did not retain old ledger", n)
	}
	if n := scalar(t, admin, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_allowed`, account.ID); n != 1 {
		t.Fatal("old window still active", n)
	}
	run2 := insertRun(t, admin, runner, profile)
	mustRoute(t, mod, runner, token, run2, "daemon-a", []Account{account}, map[string]int64{"requests": 1})
	// DB policies also reject writes with another tenant's account/principal.
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, foreign.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'5h',300,0,now()+interval '1 hour',now(),'harness')`, foreign.TenantID, account.ID)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant account FK accepted")
	}
}

func TestCapacityStaleManualOverrideAndSchedules(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "schedule", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	reader := addPrincipal(t, admin.TenantID, "person", "Second", []string{"admin"})
	foreign := makePrincipal(t, "schedule-other", "person", "Other", []string{"admin"})
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"quota","harness":"codex","daemon_id":"daemon-a","label":"Codex"}`, 201, &a)
	ownFixtureAccount(t, admin, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	// The stale reading stays dispatchable inside the default 08:00–22:00 band.
	// A wall clock outside that band waits on the schedule instead.
	now := time.Now().UTC()
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday || now.Hour() < 8 || now.Hour() >= 22 {
		now = time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
		if now.After(time.Now()) {
			now = now.AddDate(0, 0, -1)
		}
		for now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
			now = now.AddDate(0, 0, -1)
		}
	}
	r := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 1, ResetsAt: now.Add(time.Hour), ReadAt: now.Add(-time.Hour), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
	// The health checks are about the refresh and the manual window, not the
	// clock: an always-on account schedule keeps them in hours (the default
	// 08-22 UTC band failed them after 22:00 UTC). The saves below replace it.
	always := capacity.DefaultSchedule()
	for i := range always.Week {
		always.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &always, false, ""}), 204, nil)
	health := func() HarnessHealth {
		t.Helper()
		var out map[string]HarnessHealth
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error { var err error; out, err = HarnessHealthAt(t.Context(), tx, now); return err })
		if err != nil {
			t.Fatal(err)
		}
		return out["codex"]
	}
	if h := health(); h.Dispatchable != 1 {
		t.Fatal("stale reading cannot refresh", h)
	}
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/"+a.ID+"/windows", windowBody(now.Add(-time.Minute), now.Add(time.Hour), "requests", 10, "unrestricted"), 201, nil)
	if h := health(); h.Dispatchable != 1 {
		t.Fatal("manual override unavailable", h)
	}
	s := capacity.DefaultSchedule()
	s.Week = capacity.Preset(3)
	save := func(p tenant.Principal, scope, pool, id string, s *capacity.Schedule, status int) {
		t.Helper()
		callStatus(t, mod, &p, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{scope, pool, id, s, false, ""}), status, nil)
	}
	save(admin, "user", "", "", &s, 204)
	s.Week = capacity.Preset(4)
	save(admin, "pool", "codex", "", &s, 204)
	s.Week = capacity.Preset(6)
	save(admin, "account", "", a.ID, &s, 204)
	check := func(p tenant.Principal, days int) {
		t.Helper()
		var out []accountCapacity
		callStatus(t, mod, &p, "", "GET", "/api/agent-accounts/capacity", "", 200, &out)
		if len(out) != 1 || workDays(out[0].Schedule) != days {
			t.Fatal(out)
		}
	}
	check(admin, 6)
	// Other administrators receive a neutral, explicitly redacted schedule.
	check(reader, 7)
	save(admin, "account", "", a.ID, nil, 204)
	check(admin, 4)
	save(admin, "pool", "codex", "", nil, 204)
	check(admin, 3)
	if n := scalar(t, foreign, `SELECT count(*) FROM account_capacity_schedules`); n != 0 {
		t.Fatal("RLS leaked schedules")
	}
	save(foreign, "account", "", a.ID, &s, 404)
	s.Timezone = "invalid-zone"
	save(admin, "user", "", "", &s, 400)
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/capacity", "", 403, nil)
	var schedules []scheduleOverride
	callStatus(t, mod, &reader, "", "GET", "/api/agent-accounts/capacity/schedule", "", 200, &schedules)
	if len(schedules) != 0 {
		t.Fatal("other person's schedules leaked")
	}
}

func TestCapacityActiveWindowSelection(t *testing.T) {
	for _, hour := range []int{3, 14} {
		now := time.Date(2026, time.September, 29, hour, 0, 0, 0, time.UTC)
		t.Run(now.Format("15:04Z"), func(t *testing.T) {
			testCapacityActiveWindowSelection(t, now)
		})
	}
}

func testCapacityActiveWindowSelection(t *testing.T, now time.Time) {
	t.Helper()
	r := now.Add(-time.Minute)
	fresh := Window{ID: "derived", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Unit: "percent", Allowance: 100, Used: 20, PaceModel: "unrestricted", capacityReadAt: &r, capacityAllowed: true}
	if !allowanceHeadroom([]Window{fresh}, now) {
		t.Fatal("fresh reading ineligible")
	}
	stale := fresh
	at := now.Add(-11 * time.Minute)
	stale.capacityReadAt = &at
	if !allowanceHeadroom([]Window{stale}, now) {
		t.Fatal("aging refresh unavailable")
	}
	exhausted := fresh
	exhausted.Used = 100
	if allowanceHeadroom([]Window{exhausted}, now) {
		t.Fatal("exhausted routing allowed")
	}
	denied := fresh
	denied.capacityAllowed = false
	if allowanceHeadroom([]Window{denied}, now) {
		t.Fatal("denied routing allowed")
	}
	reset := fresh
	reset.EndsAt = now
	if allowanceHeadroom([]Window{reset}, now) {
		t.Fatal("expired reading renewed")
	}
	for _, blocked := range []Window{denied, exhausted} {
		blocked.capacityKind = "weekly"
		if allowanceHeadroom([]Window{fresh, blocked}, now) {
			t.Fatal("fresh peer bypassed constrained weekly window")
		}
	}
	previous := stale
	previous.ID = "old-period"
	previous.capacityAllowed = false
	if !allowanceHeadroom([]Window{previous, fresh}, now) {
		t.Fatal("superseded period blocked newer observation")
	}
	manual := fresh
	manual.ID = "manual"
	manual.capacityReadAt = nil
	manual.Unit = "requests"
	if got := activeWindows([]Window{denied, manual}, now); len(got) != 0 {
		t.Fatal("manual bypassed vendor denial")
	}
	denied.EndsAt = now
	if got := activeWindows([]Window{denied, manual}, now); len(got) != 0 {
		t.Fatal("reset bypassed fresh vendor denial")
	}
	// A manual window caps on top of the reading (AEON-384): both bind.
	got := activeWindows([]Window{fresh, manual}, now)
	if len(got) != 2 || got[0].ID != "manual" || got[1].ID != "derived" {
		t.Fatal(got)
	}
}

// Routing and claim validation share applyCapacityPacing. Exercise its existing
// clock argument at both sides of the default work-hours band, including a
// weekend, so the regression is covered even when CI runs during the day.
func TestCapacityPacingAtNightAndDay(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "capacity-clock", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage"})
	mod := accountsMod()
	var account Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"clock","harness":"codex","daemon_id":"daemon-a","label":"Clock"}`, 201, &account)
	ownFixtureAccount(t, admin, &account)
	for _, date := range []struct {
		month time.Month
		day   int
	}{{time.September, 29}, {time.October, 4}} { // Tuesday and Sunday.
		for _, hour := range []int{3, 14} {
			now := time.Date(2026, date.month, date.day, hour, 0, 0, 0, time.UTC)
			t.Run(now.Format(time.RFC3339), func(t *testing.T) {
				for _, override := range []string{"", "sprint", "hold"} {
					t.Run("override="+override, func(t *testing.T) {
						readAt := now.Add(-time.Minute)
						windows := []Window{{AccountID: account.ID, StartsAt: now.Add(-4 * time.Hour), EndsAt: now.Add(time.Hour), Unit: "percent", Allowance: 100, Used: 20, PaceModel: "unrestricted", capacityReadAt: &readAt, capacityAllowed: true, capacityKind: "5h"}}
						schedule := capacity.DefaultSchedule()
						// This fixture isolates the work-hours band from Keep for you.
						schedule.Reserve = capacity.ReserveOff
						schedule.Override = override
						err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
							return applyCapacityPacing(t.Context(), tx, account, windows, now, schedule)
						})
						if err != nil {
							t.Fatal(err)
						}
						want := 0.0
						if override == "sprint" || override == "" && hour == 14 {
							want = 80
						}
						w := windows[0]
						if w.capacityBudget == nil {
							t.Fatal("capacity budget missing")
						}
						if *w.capacityBudget != want {
							t.Fatalf("capacity budget = %v, want %v", *w.capacityBudget, want)
						}
						if _, ok := fits(w, now, 1); ok != (want > 0) {
							t.Fatalf("reservation eligibility = %v with budget %v", ok, want)
						}
						// An explicit Sprint must still respect the vendor's denial.
						w.capacityAllowed = false
						if _, ok := fits(w, now, 1); ok {
							t.Fatal("schedule bypassed reading authority")
						}
					})
				}
			})
		}
	}
}

func workDays(s capacity.Schedule) int {
	n := 0
	for _, d := range s.Week {
		if d.On {
			n++
		}
	}
	return n
}

func TestCapacityEnforcesSchedulesSnapshotsAndSingleRefresh(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "enforced-capacity", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	profile := codexProfile(t, admin)
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"quota","harness":"codex","daemon_id":"daemon-a","label":"Quota","max_parallel_runs":3}`, 201, &a)
	ownFixtureAccount(t, admin, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	path := "/api/agent-accounts/" + a.ID + "/readings"
	report := func(rs ...capacity.Reading) {
		t.Helper()
		callStatus(t, mod, &runner, token, "POST", path, encoded(t, readingsWrite{rs}), 204, nil)
	}
	schedule := capacity.DefaultSchedule("Europe/Vienna")
	schedule.Week = capacity.Preset(7)
	for i := range schedule.Week {
		schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	save := func(scope, override string) {
		t.Helper()
		schedule.Override = override
		pool, id := "", a.ID
		if scope == "pool" {
			pool, id = "codex", ""
		}
		callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{scope, pool, id, &schedule, false, ""}), 204, nil)
	}
	route := func(status int) string {
		t.Helper()
		run := insertRun(t, admin, runner, profile)
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", encoded(t, map[string]any{"run_id": run, "daemon_id": "daemon-a", "account_ids": []string{a.ID}, "estimated_units": map[string]int64{"requests": 1}}), status, nil)
		return run
	}
	release := func(run string) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, run, "", "") }); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	yes := true
	weekly := capacity.Reading{WindowKind: "weekly", Bucket: "codex", WindowMinutes: 10080, UsedPercent: 0, ReadAt: now.Add(-time.Second), ResetsAt: now.Add(48 * time.Hour), Source: "harness", OrdinaryUsageAllowed: &yes}
	report(weekly)
	save("account", "")
	weekly.UsedPercent = 95
	weekly.ReadAt = now
	report(weekly)
	route(409) // Observed use already consumed today's share, despite 5% remaining.
	save("account", "sprint")
	run := route(200)
	release(run)
	save("account", "hold")
	route(409)
	// Pool overrides survive removal of the account override and use the same owner.
	save("pool", "sprint")
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, nil, false, ""}), 204, nil)
	run = route(200)
	release(run)
	save("pool", "hold")
	route(409)
	save("pool", "sprint")
	short := capacity.Reading{WindowKind: "5h", Bucket: "codex", WindowMinutes: 300, ReadAt: now.Add(100 * time.Millisecond), ResetsAt: now.Add(time.Hour), Source: "harness", OrdinaryUsageAllowed: &yes}
	weekly.ReadAt = short.ReadAt
	weekly.UsedPercent = 100
	report(weekly, short)
	route(409)
	short.ReadAt = now.Add(200 * time.Millisecond)
	report(short)
	run = route(200)
	release(run) // Missing weekly bucket no longer constrains this source.
	if n := scalar(t, admin, `SELECT count(*) FROM account_allowance_windows WHERE account_id=$1 AND capacity_retired`, a.ID); n != 1 {
		t.Fatal("missing bucket not retired", n)
	}
	// Out-of-order history cannot resurrect a missing bucket.
	weekly.ReadAt = now.Add(150 * time.Millisecond)
	report(weekly)
	run = route(200)
	release(run)
	// Retire the short window and introduce an older observation from a different source.
	// Its first refresh job is bounded to one reservation even with 3 parallel slots.
	stale := short
	stale.Source = "agentd"
	stale.ReadAt = now.Add(-20 * time.Minute)
	stale.Bucket = "refresh"
	report(stale)
	run = route(200)
	route(409)
	release(run)
	route(409)
	stale.ReadAt = now.Add(300 * time.Millisecond)
	report(stale)
	run = route(200)
	release(run)
	// Vendor denial also fences explicit manual windows, and queued replays.
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/"+a.ID+"/windows", windowBody(now.Add(-time.Minute), now.Add(time.Hour), "requests", 10, "unrestricted"), 201, nil)
	run = route(200)
	no := false
	stale.OrdinaryUsageAllowed = &no
	stale.ReadAt = now.Add(400 * time.Millisecond)
	report(stale)
	route(409)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", encoded(t, map[string]any{"run_id": run, "daemon_id": "daemon-a", "account_ids": []string{a.ID}, "estimated_units": map[string]int64{"requests": 1}}), 409, nil)
}

func TestCapacityDefaultUsesPersonTimezone(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "capacity-zone", "person", "Ada", []string{"admin"})
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO personal_profiles(tenant_id,principal_id,timezone) VALUES($1,$2,'Pacific/Auckland') ON CONFLICT(tenant_id,principal_id) DO UPDATE SET timezone=EXCLUDED.timezone`, p.TenantID, p.ID); err != nil {
			return err
		}
		s, err := effectiveSchedule(t.Context(), tx, p.ID, Account{Harness: "codex"})
		if err == nil && s.Timezone != "Pacific/Auckland" {
			t.Fatal(s.Timezone)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoutingInheritsOwnerScheduleWithoutAccountOverride(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "capacity-inherit", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var account Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"inherit","harness":"codex","daemon_id":"daemon-a","label":"Inherited"}`, 201, &account)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$1 WHERE principal_id=$2`, person.ID, runner.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO personal_profiles(tenant_id,principal_id,timezone) VALUES($1,$2,'Pacific/Auckland') ON CONFLICT(tenant_id,principal_id) DO UPDATE SET timezone=EXCLUDED.timezone`, person.TenantID, person.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantZone, wantOverride string) {
		t.Helper()
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			s, err := routingSchedule(t.Context(), tx, account)
			if err == nil && (s.Timezone != wantZone || s.Override != wantOverride) {
				t.Fatalf("routing schedule = %s/%s, want %s/%s", s.Timezone, s.Override, wantZone, wantOverride)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check("Pacific/Auckland", "")
	user := capacity.DefaultSchedule("Europe/Vienna")
	user.Override = "hold"
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"user", "", "", &user, false, ""}), 204, nil)
	check("Europe/Vienna", "hold")
	pool := capacity.DefaultSchedule("America/New_York")
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"pool", "codex", "", &pool, false, ""}), 204, nil)
	check("America/New_York", "")
	if n := scalar(t, person, `SELECT count(*) FROM account_capacity_schedules WHERE scope='account'`); n != 0 {
		t.Fatal("test must not establish an account override")
	}
	// A second human key creator must not make us choose an arbitrary owner.
	other := addPrincipal(t, person.TenantID, "person", "Other", []string{"admin"})
	issueKey(t, runner, []string{"account.read"})
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET created_by_principal_id=$1 WHERE principal_id=$2 AND created_by_principal_id IS NULL`, other.ID, runner.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	check("UTC", "")
	// An explicitly saved owner remains authoritative even with multiple creators.
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", account.ID, &user, false, ""}), 204, nil)
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", account.ID, nil, false, ""}), 204, nil)
	check("America/New_York", "")
}

func TestExpiredCapacityGetsOneProvisionalRefresh(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "expired-capacity", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	profile := codexProfile(t, person)
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"expired","harness":"codex","daemon_id":"daemon-a","label":"Expired","max_parallel_runs":3}`, 201, &a)
	ownFixtureAccount(t, person, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule()
	s.Week = capacity.Preset(7)
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false, ""}), 204, nil)
	now := time.Now().UTC()
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 100, ReadAt: now.Add(-6 * time.Hour), ResetsAt: now.Add(-2 * time.Hour), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	run := insertRun(t, person, runner, profile)
	route := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	if len(route.Reservations) != 1 || route.Reservations[0].Unit != "percent" {
		t.Fatal(route)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error { return Release(t.Context(), tx, runner, run, "", "") }); err != nil {
		t.Fatal(err)
	}
	second := insertRun(t, person, runner, profile)
	body := encoded(t, map[string]any{"run_id": second, "daemon_id": "daemon-a", "account_ids": []string{a.ID}, "estimated_units": map[string]int64{"requests": 1}})
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", body, 409, nil)
	reading.ReadAt = now
	reading.ResetsAt = now.Add(time.Hour)
	reading.UsedPercent = 1
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", body, 200, nil)
}

func TestCapacityPreviewPacesDraftWithoutSaving(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "preview", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"quota","harness":"codex","daemon_id":"daemon-a","label":"Codex"}`, 201, &a)
	ownFixtureAccount(t, admin, &a)
	now := time.Now().UTC()
	r := capacity.Reading{WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: 40, ResetsAt: now.Add(72 * time.Hour), ReadAt: now.Add(-time.Minute), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
	saved := capacity.DefaultSchedule("Europe/Vienna")
	saved.Week = capacity.Preset(3)
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"user", "", "", &saved, false, ""}), 204, nil)
	hold := saved
	hold.Override = "hold"
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"pool", "codex", "", &hold, false, ""}), 204, nil)

	draft := capacity.DefaultSchedule("Europe/Vienna")
	for i := range draft.Week {
		draft.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	var out []accountCapacity
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": draft}), 200, &out)
	if len(out) != 1 || workDays(out[0].Schedule) != 7 || out[0].Schedule.Override != "hold" || len(out[0].Windows) != 1 {
		t.Fatal("preview must pace the draft and keep the active override", out)
	}
	// Hold keeps today's budget at what was already used, whatever the draft says.
	if p := out[0].Windows[0].Pacing; p.SuggestedTodayPercent != 0 {
		t.Fatal("hold ignored in preview", p)
	}
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"pool", "codex", "", nil, false, ""}), 204, nil)
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": draft}), 200, &out)
	if p := out[0].Windows[0].Pacing; p.UsableHours <= 0 || p.BudgetPercent <= 0 {
		t.Fatal("preview did not pace a round-the-clock draft", p)
	}
	var current []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &current)
	if len(current) != 1 || workDays(current[0].Schedule) != 3 {
		t.Fatal("preview changed the saved schedule", current)
	}
	bad := draft
	bad.Week = capacity.Preset(0)
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": bad}), 400, nil)
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", `{"schedule":null}`, 400, nil)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": draft}), 403, nil)
}

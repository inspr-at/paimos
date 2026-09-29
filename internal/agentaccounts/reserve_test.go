// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"math"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// The router cap (§2.3, AEON-375): at 14:00 a Claude 5-hour window keeps R_eff
// for the person; at 23:30 its full remainder can be routed.
func TestRouterKeepsTheRunwayReserve(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "keep-for-you", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"claude","harness":"claude","daemon_id":"daemon-a","label":"markus"}`, 201, &a)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	s := capacity.DefaultSchedule("Europe/Vienna")
	s.Nights = true
	callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
	loc, _ := time.LoadLocation(s.Timezone)
	at := func(d, h, m int) time.Time { return time.Date(2026, 9, d, h, m, 0, 0, loc) }
	five := func(start time.Time, used int64, now time.Time) Window {
		read := now.Add(-time.Minute)
		return Window{AccountID: a.ID, StartsAt: start, EndsAt: start.Add(5 * time.Hour), Unit: "percent", Allowance: 100, Used: used, PaceModel: "unrestricted", capacityReadAt: &read, capacityAllowed: true, capacityKind: "5h", capacityBucket: ""}
	}
	admit := func(tx pgx.Tx, now time.Time, w Window, override string) ([]Window, *CapacityWait) {
		t.Helper()
		b, err := getAccount(t.Context(), tx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		b.LastProbeAt = &now
		windows, wait, err := admission(t.Context(), tx, b, []Window{w}, now, 0, runRow{Purpose: "managed", CapacityOverride: override}, false)
		if err != nil {
			t.Fatal(err)
		}
		return windows, wait
	}
	budget := func(ws []Window) float64 {
		t.Helper()
		if len(ws) != 1 || ws[0].capacityBudget == nil {
			t.Fatalf("no derived allowance: %+v", ws)
		}
		return *ws[0].capacityBudget
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		// Tue 14:00, window 13:00→18:00, 70% used: R_eff = 30 × 4/5 = 24, so
		// agents may take 30 − 24 = 6, and a 1% hold fits.
		noon := at(29, 14, 0)
		ws, wait := admit(tx, noon, five(at(29, 13, 0), 70, noon), "")
		if wait != nil || math.Abs(budget(ws)-6) > .5 {
			t.Fatalf("14:00 cap %v wait %+v", ws, wait)
		}
		if _, ok := fits(ws[0], noon, 1); !ok {
			t.Fatal("a 1% hold must fit above the reserve")
		}
		// 76% used leaves exactly the reserve: the run waits for it, until the
		// window resets at 18:00 inside the person's hours. Run now once skips it.
		ws, wait = admit(tx, noon, five(at(29, 13, 0), 76, noon), "")
		if ws != nil || wait == nil || wait.Code != "reserve" || !wait.RunNowAllowed || wait.Until == nil || !wait.Until.Equal(at(29, 18, 0)) {
			t.Fatalf("14:00 at the reserve: %+v %+v", ws, wait)
		}
		if ws, wait = admit(tx, noon, five(at(29, 13, 0), 76, noon), "now"); wait != nil || budget(ws) < 23.5 {
			t.Fatalf("Run now once must skip the reserve: %+v %+v", ws, wait)
		}
		// Tue 23:30, window 22:10→03:10, 76% used: no work hours before the
		// reset, so all 24% can be routed tonight.
		night := at(29, 23, 30)
		ws, wait = admit(tx, night, five(at(29, 22, 10), 76, night), "")
		if wait != nil || math.Abs(budget(ws)-24) > .5 {
			t.Fatalf("23:30 cap %v wait %+v", ws, wait)
		}
		// reserve: off gives release 11's routing at 14:00.
		s.Reserve = capacity.ReserveOff
		if _, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=$2::jsonb WHERE account_id=$1`, a.ID, encoded(t, s)); err != nil {
			return err
		}
		if ws, wait = admit(tx, noon, five(at(29, 13, 0), 76, noon), ""); wait != nil || budget(ws) != 24 {
			t.Fatalf("reserve off: %+v %+v", ws, wait)
		}
		// A dated Hold waits until its date, then pacing resumes.
		s.Reserve, s.Override = "", "hold"
		until := noon.Add(2 * time.Hour)
		s.OverrideUntil = &until
		if _, err := tx.Exec(t.Context(), `UPDATE account_capacity_schedules SET schedule=$2::jsonb WHERE account_id=$1`, a.ID, encoded(t, s)); err != nil {
			return err
		}
		if _, wait = admit(tx, noon, five(at(29, 13, 0), 50, noon), ""); wait == nil || wait.Code != "hold" || wait.Until == nil || !wait.Until.Equal(until) {
			t.Fatalf("dated hold: %+v", wait)
		}
		later := until.Add(time.Minute)
		if _, wait = admit(tx, later, five(at(29, 13, 0), 50, later), ""); wait != nil {
			t.Fatalf("hold survived its date: %+v", wait)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Keep for you per scope (user, pool, account), Away across pools, dated Hold,
// and the preview's pool drafts.
func TestKeepForYouScopesAwayAndPreview(t *testing.T) {
	admin, _, _, accounts := capacityWorld(t, "keep-scopes", "codex", "claude")
	mod := accountsMod()
	put := func(body scheduleOverride, status int) {
		t.Helper()
		callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, body), status, nil)
	}
	list := func() map[string]accountCapacity {
		t.Helper()
		var out []accountCapacity
		callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &out)
		return byAccount(out)
	}
	codex, claude := accounts["codex"].ID, accounts["claude"].ID
	// Release 11 schedules carry no reserve: Auto, 30%.
	if got := list(); got[codex].Schedule.Reserve != "" || got[codex].Windows[0].Pacing.ReservePercent != capacity.AutoReserve {
		t.Fatalf("default reserve: %+v", got[codex])
	}
	user := capacity.DefaultSchedule("Europe/Vienna")
	user.Reserve, user.ReservePercent = capacity.ReserveFixed, 50
	put(scheduleOverride{Scope: "user", Schedule: &user}, 204)
	pool := user
	pool.Reserve, pool.ReservePercent = capacity.ReserveOff, 0
	put(scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &pool}, 204)
	got := list()
	if got[codex].Schedule.Reserve != capacity.ReserveOff || got[codex].Windows[0].Pacing.ReservePercent != 0 {
		t.Fatalf("pool reserve: %+v", got[codex].Schedule)
	}
	if got[claude].Schedule.Reserve != capacity.ReserveFixed || got[claude].Windows[0].Pacing.ReservePercent != 50 {
		t.Fatalf("user reserve: %+v", got[claude].Schedule)
	}
	// A pool entry that only carries its own reserve follows a new work week
	// and keeps that reserve (carry_overrides).
	six := user
	six.Week = capacity.Preset(6)
	put(scheduleOverride{Scope: "user", Schedule: &six, CarryOverrides: true}, 204)
	got = list()
	if workDays(got[codex].Schedule) != 6 || got[codex].Schedule.Reserve != capacity.ReserveOff {
		t.Fatalf("carried pool reserve: %+v", got[codex].Schedule)
	}
	// An account-scope schedule without its own reserve inherits the pool's.
	acct := six
	acct.Reserve, acct.ReservePercent = "", 0
	acct.Nights = true
	put(scheduleOverride{Scope: "account", AccountID: codex, Schedule: &acct}, 204)
	if got = list(); !got[codex].Schedule.Nights || got[codex].Schedule.Reserve != capacity.ReserveOff {
		t.Fatalf("account inherits reserve: %+v", got[codex].Schedule)
	}
	put(scheduleOverride{Scope: "account", AccountID: codex, Schedule: nil}, 204)
	// Away: the person's own schedule, every pool, until the date; a pool Hold wins.
	now := time.Now().UTC()
	away := six
	away.Override = "away"
	until := now.Add(72 * time.Hour).Truncate(time.Second)
	away.OverrideUntil = &until
	put(scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &away}, 400)
	past := now.Add(-time.Hour)
	away.OverrideUntil = &past
	put(scheduleOverride{Scope: "user", Schedule: &away}, 400)
	away.OverrideUntil = &until
	put(scheduleOverride{Scope: "user", Schedule: &away}, 204)
	got = list()
	for _, id := range []string{codex, claude} {
		w := got[id].Windows[0].Pacing
		if got[id].Schedule.Override != "away" || w.ReserveEffectivePercent != 0 || w.AvailableNowPercent != 70 {
			t.Fatalf("away on %s: %+v %+v", id, got[id].Schedule, w)
		}
	}
	hold := pool
	hold.Override = "hold"
	far := now.Add(40 * 24 * time.Hour)
	hold.OverrideUntil = &far
	put(scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &hold}, 400)
	soon := now.Add(2 * time.Hour).Truncate(time.Second)
	hold.OverrideUntil = &soon
	put(scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &hold}, 204)
	got = list()
	if got[codex].Schedule.Override != "hold" || got[codex].Schedule.OverrideUntil == nil || !got[codex].Schedule.OverrideUntil.Equal(soon) || got[claude].Schedule.Override != "away" {
		t.Fatalf("hold vs away: %+v / %+v", got[codex].Schedule, got[claude].Schedule)
	}
	// The preview takes the drafted person reserve, Away and pool reserves.
	draft := six
	draft.Reserve, draft.ReservePercent = capacity.ReserveFixed, 20
	var preview []accountCapacity
	body := encoded(t, map[string]any{"schedule": draft, "pool_reserves": []poolReserve{{Pool: "codex", Reserve: ""}, {Pool: "claude", Reserve: capacity.ReserveFixed, ReservePercent: 40}}})
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", body, 200, &preview)
	pv := byAccount(preview)
	if pv[codex].Schedule.Reserve != capacity.ReserveFixed || pv[codex].Schedule.ReservePercent != 20 || pv[claude].Schedule.ReservePercent != 40 {
		t.Fatalf("preview reserves: %+v / %+v", pv[codex].Schedule, pv[claude].Schedule)
	}
	if pv[claude].Schedule.Override != "" {
		t.Fatal("a draft without Away ends Away in the preview", pv[claude].Schedule)
	}
	draft.Override, draft.OverrideUntil = "away", &until
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": draft}), 200, &preview)
	if pv = byAccount(preview); pv[claude].Schedule.Override != "away" {
		t.Fatalf("preview away: %+v", pv[claude].Schedule)
	}
	for _, bad := range []string{
		`{"schedule":` + encoded(t, six) + `,"pool_reserves":[{"pool":"nope","reserve":"off"}]}`,
		`{"schedule":` + encoded(t, six) + `,"pool_reserves":[{"pool":"codex","reserve":"fixed","reserve_percent":7}]}`,
		`{"schedule":` + encoded(t, six) + `,"pool_reserves":[{"pool":"codex","reserve":"off"},{"pool":"codex","reserve":"auto"}]}`,
	} {
		callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", bad, 400, nil)
	}
	if n := scalar(t, admin, `SELECT count(*) FROM account_capacity_schedules WHERE schedule->>'reserve_percent'='20'`); n != 0 {
		t.Fatal("preview stored a draft")
	}
}

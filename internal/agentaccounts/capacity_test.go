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
	if len(history) != 1 {
		t.Fatal(history)
	}
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
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	now := time.Now().UTC()
	r := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 1, ResetsAt: now.Add(time.Hour), ReadAt: now.Add(-time.Hour), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
	health := func() HarnessHealth {
		t.Helper()
		var out map[string]HarnessHealth
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error { var err error; out, err = HarnessHealthAt(t.Context(), tx, now); return err })
		if err != nil {
			t.Fatal(err)
		}
		return out["codex"]
	}
	if h := health(); h.Dispatchable != 0 {
		t.Fatal("stale reading routed", h)
	}
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/"+a.ID+"/windows", windowBody(now.Add(-time.Minute), now.Add(time.Hour), "requests", 10, "unrestricted"), 201, nil)
	if h := health(); h.Dispatchable != 1 {
		t.Fatal("manual override unavailable", h)
	}
	s := capacity.DefaultSchedule()
	s.Week = capacity.Preset(3)
	save := func(p tenant.Principal, scope, pool, id string, s *capacity.Schedule, status int) {
		t.Helper()
		callStatus(t, mod, &p, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{scope, pool, id, s}), status, nil)
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
	check(reader, 5)
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
	now := time.Now()
	r := now.Add(-time.Minute)
	fresh := Window{ID: "derived", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Unit: "percent", Allowance: 100, Used: 20, PaceModel: "unrestricted", capacityReadAt: &r, capacityAllowed: true}
	if !allowanceHeadroom([]Window{fresh}, now) {
		t.Fatal("fresh reading ineligible")
	}
	stale := fresh
	at := now.Add(-11 * time.Minute)
	stale.capacityReadAt = &at
	if allowanceHeadroom([]Window{stale}, now) {
		t.Fatal("aging routing allowed")
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
	for _, blocked := range []Window{stale, denied, reset, exhausted} {
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
	got := activeWindows([]Window{fresh, manual}, now)
	if len(got) != 1 || got[0].ID != "manual" {
		t.Fatal(got)
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

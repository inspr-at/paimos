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

// capacityWorld registers one account per harness with a fresh weekly reading.
func capacityWorld(t *testing.T, slug string, harnesses ...string) (tenant.Principal, tenant.Principal, string, map[string]Account) {
	t.Helper()
	reset(t)
	admin := makePrincipal(t, slug, "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	now := time.Now().UTC()
	out := map[string]Account{}
	for _, h := range harnesses {
		var a Account
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"`+h+`","harness":"`+h+`","daemon_id":"daemon-a","label":"`+h+`"}`, 201, &a)
		r := capacity.Reading{WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: 30, ResetsAt: now.Add(80 * time.Hour), ReadAt: now.Add(-time.Minute), Source: "harness"}
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
		out[h] = a
	}
	return admin, runner, token, out
}

func byAccount(list []accountCapacity) map[string]accountCapacity {
	out := map[string]accountCapacity{}
	for _, c := range list {
		out[c.AccountID] = c
	}
	return out
}

// Review blocker 3 (and 1): the preview follows exactly the inheritance a save
// applies. A deliberate 3-day Grok pool schedule stays; a Hold that only rides
// on the person's schedule follows it; an ended Sprint is removed. The save is
// one transaction with carry_overrides.
func TestCapacityPreviewMatchesSavedInheritance(t *testing.T) {
	admin, _, _, accounts := capacityWorld(t, "carry", "codex", "grok")
	mod := accountsMod()
	put := func(body scheduleOverride, status int) {
		t.Helper()
		callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, body), status, nil)
	}
	user := capacity.DefaultSchedule("Europe/Vienna")
	put(scheduleOverride{Scope: "user", Schedule: &user}, 204)
	grok := user
	grok.Week = capacity.Preset(3)
	put(scheduleOverride{Scope: "pool", Pool: "grok", Schedule: &grok}, 204)
	hold := user
	hold.Override = "hold"
	put(scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &hold}, 204)
	// An ended Sprint left behind on a pool without accounts.
	ended := user
	ended.Override = "sprint"
	past := time.Now().Add(-time.Hour)
	ended.OverrideUntil = &past
	raw, _ := json.Marshal(ended)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,schedule) VALUES($1,$2,'pool','cursor',$3)`, admin.TenantID, admin.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	draft := capacity.DefaultSchedule("Europe/Vienna")
	draft.Week = capacity.Preset(7)
	var preview []accountCapacity
	callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/capacity/preview", encoded(t, map[string]any{"schedule": draft}), 200, &preview)
	put(scheduleOverride{Scope: "user", Schedule: &draft, CarryOverrides: true}, 204)
	var saved []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &saved)

	before, after := byAccount(preview), byAccount(saved)
	for h, a := range accounts {
		p, s := before[a.ID], after[a.ID]
		if !sameShape(p.Schedule, s.Schedule) || p.Schedule.Override != s.Schedule.Override || len(p.Windows) != 1 || len(s.Windows) != 1 {
			t.Fatalf("%s: preview %+v, saved %+v", h, p.Schedule, s.Schedule)
		}
		if pw, sw := p.Windows[0].Pacing, s.Windows[0].Pacing; pw.BudgetPercent != sw.BudgetPercent || pw.UsableHours != sw.UsableHours {
			t.Fatalf("%s: preview paced %+v, saved paced %+v", h, pw, sw)
		}
	}
	if d := workDays(after[accounts["grok"].ID].Schedule); d != 3 {
		t.Fatal("deliberate Grok schedule was overwritten", d)
	}
	if c := after[accounts["codex"].ID].Schedule; workDays(c) != 7 || c.Override != "hold" {
		t.Fatal("Hold did not follow the new week", c)
	}
	if n := scalar(t, admin, `SELECT count(*) FROM account_capacity_schedules WHERE scope='pool' AND scope_key='cursor'`); n != 0 {
		t.Fatal("ended Sprint entry kept", n)
	}
	put(scheduleOverride{Scope: "pool", Pool: "codex", Schedule: &draft, CarryOverrides: true}, 400)
}

// Review blocker 4: the Sprint end the UI promises is the server's limiting
// reset, which includes the 5-hour window.
func TestCapacityLimitingResetIsTheSprintEnd(t *testing.T) {
	admin, runner, token, accounts := capacityWorld(t, "limiting", "claude")
	mod := accountsMod()
	now := time.Now().UTC()
	five := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 40, ResetsAt: now.Add(2 * time.Hour).Truncate(time.Second), ReadAt: now.Add(-time.Second), Source: "harness"}
	weekly := capacity.Reading{WindowKind: "weekly", WindowMinutes: 7 * 24 * 60, UsedPercent: 60, ResetsAt: now.Add(90 * time.Hour), ReadAt: now.Add(-time.Second), Source: "harness"}
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+accounts["claude"].ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{five, weekly}}), 204, nil)
	var list []accountCapacity
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &list)
	limit := list[0].LimitingReset
	if limit == nil || !limit.Equal(five.ResetsAt) {
		t.Fatalf("limiting reset %v, want the 5-hour reset %v", limit, five.ResetsAt)
	}
	sprint := capacity.DefaultSchedule()
	sprint.Override = "sprint"
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "pool", Pool: "claude", Schedule: &sprint}), 204, nil)
	var entries []scheduleOverride
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity/schedule", "", 200, &entries)
	if len(entries) != 1 || entries[0].Schedule.OverrideUntil == nil || !entries[0].Schedule.OverrideUntil.Equal(*limit) {
		t.Fatalf("Sprint end %+v differs from limiting reset %v", entries, limit)
	}
}

// Review blocker 2: the capacity projection names why the last probe failed;
// only a confirmed sign-out is auth_failed.
func TestCapacityProbeFailureCause(t *testing.T) {
	admin, runner, token, accounts := capacityWorld(t, "probe-cause", "codex")
	mod := accountsMod()
	path := "/api/agent-accounts/" + accounts["codex"].ID + "/probe"
	cause := func() string {
		t.Helper()
		var list []accountCapacity
		callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/capacity", "", 200, &list)
		return list[0].ProbeFailure
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":false,"failure":"auth_failed"}`, 200, nil)
	if c := cause(); c != "auth_failed" {
		t.Fatal(c)
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":false}`, 200, nil)
	if c := cause(); c != "unavailable" {
		t.Fatal("an unexplained failure must not claim a sign-out", c)
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	if c := cause(); c != "" {
		t.Fatal(c)
	}
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true,"failure":"auth_failed"}`, 400, nil)
	callStatus(t, mod, &runner, token, "POST", path, `{"daemon_id":"daemon-a","daemon_generation":"g1","available":false,"failure":"expired"}`, 400, nil)
}

// Review blocker 6: the preview is bounded per person, in concurrency, in time
// and in total integration work.
func TestCapacityPreviewBounds(t *testing.T) {
	admin, _, _, _ := capacityWorld(t, "preview-bounds", "codex")
	body := encoded(t, map[string]any{"schedule": capacity.DefaultSchedule("Europe/Vienna")})
	path := "/api/agent-accounts/capacity/preview"
	rate := &Module{pool: appPool, preview: newPreviewGuard(2, time.Minute, 4, previewBudget, previewTimeout)}
	callStatus(t, rate, &admin, "", "POST", path, body, 200, nil)
	callStatus(t, rate, &admin, "", "POST", path, body, 200, nil)
	callStatus(t, rate, &admin, "", "POST", path, body, 429, nil)
	busy := newPreviewGuard(10, time.Minute, 1, previewBudget, previewTimeout)
	busy.acquire()
	callStatus(t, &Module{pool: appPool, preview: busy}, &admin, "", "POST", path, body, 503, nil)
	busy.release()
	callStatus(t, &Module{pool: appPool, preview: busy}, &admin, "", "POST", path, body, 200, nil)
	small := &Module{pool: appPool, preview: newPreviewGuard(10, time.Minute, 4, 10, previewTimeout)}
	callStatus(t, small, &admin, "", "POST", path, body, 413, nil)
	late := &Module{pool: appPool, preview: newPreviewGuard(10, time.Minute, 4, previewBudget, time.Nanosecond)}
	callStatus(t, late, &admin, "", "POST", path, body, 503, nil)
	// A rejected preview holds no slot: the busy guard still serves.
	if !busy.acquire() {
		t.Fatal("slot leaked")
	}
	busy.release()
}

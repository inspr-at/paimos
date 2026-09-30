// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestRoutingOrderLongResetCapAndID(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	window := func(kind string, end time.Duration, used int64) Window {
		return Window{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(end), Unit: "percent", Allowance: 100, Used: used, PaceModel: "unrestricted", capacityKind: kind, capacityReadAt: &now, capacityAllowed: true}
	}
	estimates := map[string]int64{"requests": 1}
	main := routeRank(Account{ID: "main", MaxParallel: 1}, []Window{window("weekly", 72*time.Hour, 10), window("5h", time.Hour, 10)}, 0, estimates, now)
	spare := routeRank(Account{ID: "spare", MaxParallel: 1}, []Window{window("weekly", 30*time.Hour, 90)}, 0, estimates, now)
	picks := []ranked{main, spare}
	orderPicks(picks)
	if picks[0].account.ID != "spare" {
		t.Fatal("five-hour reset or unused headroom beat the sooner weekly reset")
	}
	main.reset = spare.reset
	picks = []ranked{spare, main}
	orderPicks(picks)
	if picks[0].account.ID != "main" {
		t.Fatal("larger cap did not break equal reset tie")
	}
	main.cap = spare.cap
	picks = []ranked{spare, main}
	orderPicks(picks)
	if picks[0].account.ID != "main" {
		t.Fatal("ID tie break is unstable")
	}
	budget := 2.0
	w := window("weekly", time.Hour, 10)
	w.Reserved = 1
	w.capacityBudget = &budget
	p := routeRank(Account{MaxParallel: 5}, []Window{w}, 0, estimates, now)
	if p.slots != 1 || p.cap != 1 {
		t.Fatalf("cap and parallel count ignored reservations: %+v", p)
	}
}

func TestRoutingAdviceMatchesReservationsAndScope(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "route-plan", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	other := addPrincipal(t, person.TenantID, "agent", "other", nil)
	foreign := makePrincipal(t, "route-foreign", "person", "Foreign", []string{"admin"})
	profile := codexProfile(t, person)
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	mod := accountsMod()
	accounts := []Account{}
	now := time.Now().UTC().Add(-time.Second)
	for i, name := range []string{"Main", "Spare"} {
		var a Account
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": name, "harness": "codex", "daemon_id": "daemon-a", "label": name}), 201, &a)
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
		hours, used := 72.0, 10.0
		if i == 1 {
			hours, used = 30, 90
		}
		reading := capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: used, ResetsAt: now.Add(time.Duration(hours) * time.Hour), ReadAt: now, Source: "harness"}
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
		s := capacity.DefaultSchedule()
		s.Override = "sprint"
		callStatus(t, mod, &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", a.ID, &s, false}), 204, nil)
		accounts = append(accounts, a)
	}
	path := "/api/agent-accounts/capacity/next?harness=codex&model_profile_id=" + profile
	var next CapacityNext
	callStatus(t, mod, &runner, token, "GET", path, "", 200, &next)
	if len(next.Accounts) != 2 || next.Accounts[0].AccountID != accounts[1].ID || next.ParallelRuns != 2 {
		t.Fatalf("wrong plan %+v", next)
	}
	var projections []accountCapacity
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/capacity", "", 200, &projections)
	for _, p := range projections {
		want := 2
		if p.AccountID == accounts[1].ID {
			want = 1
		}
		if p.Routing == nil || p.Routing.Rank != want {
			t.Fatal("UI order differs from advice", p)
		}
	}
	raw := encoded(t, next)
	for _, forbidden := range []string{"account_key", "config", "home", "/Users/"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal("local detail leaked")
		}
	}
	// A workspace-authorized lead gets advice without owning daemon accounts.
	lead := addPrincipal(t, person.TenantID, "agent", "lead", []string{"admin"})
	leadKey := issueKey(t, lead, []string{"account.read"})
	callStatus(t, mod, &lead, leadKey, "GET", path, "", 403, nil)
	dbtest.BindRole(t, testDB, person.TenantID, lead.ID, "admin")
	callStatus(t, mod, &lead, leadKey, "GET", path, "", 200, &next)
	if len(next.Accounts) != 2 {
		t.Fatal("lead advice did not use read authority")
	}
	first := insertRun(t, person, runner, profile)
	picked := mustRoute(t, mod, runner, token, first, "daemon-a", accounts, map[string]int64{"requests": 1})
	if picked.AccountID != accounts[1].ID {
		t.Fatal("route differs from advice")
	}
	second := insertRun(t, person, runner, profile)
	picked = mustRoute(t, mod, runner, token, second, "daemon-a", accounts, map[string]int64{"requests": 1})
	if picked.AccountID != accounts[0].ID {
		t.Fatal("parallel routes did not take distinct accounts")
	}
	callStatus(t, mod, &runner, token, "GET", path, "", 200, &next)
	if len(next.Accounts) != 0 || next.ParallelRuns != 0 {
		t.Fatal("occupied accounts still offered")
	}
	// Exhaust Spare and free Main: the sequential continuation takes Main.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		if err := Release(t.Context(), tx, runner, first, "", ""); err != nil {
			return err
		}
		if err := Release(t.Context(), tx, runner, second, "", ""); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET used=100 WHERE account_id=$1`, accounts[1].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	third := insertRun(t, person, runner, profile)
	if picked = mustRoute(t, mod, runner, token, third, "daemon-a", accounts, map[string]int64{"requests": 1}); picked.AccountID != accounts[0].ID {
		t.Fatal("sequential routing ignored spent quota")
	}
	for _, p := range []struct {
		kind    string
		foreign bool
	}{{"other", false}, {"foreign", true}} {
		actor, key := other, issueKey(t, other, []string{"account.probe"})
		if p.foreign {
			actor, key = foreign, ""
		}
		callStatus(t, mod, &actor, key, "GET", path, "", 200, &next)
		if len(next.Accounts) != 0 {
			t.Fatal("advice crossed owner or tenant")
		}
	}
	callStatus(t, mod, &runner, issueKey(t, runner, []string{"run.read"}), "GET", path, "", 403, nil)
}

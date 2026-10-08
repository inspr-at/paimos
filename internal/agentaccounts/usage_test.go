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

// Risk: usage words silently change pacing, or Max out bypasses a hard floor.
func TestUsagePostureMappingAndFloor(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ word, off, reserve, override string }{{"careful", "rest", "fixed", ""}, {"balanced", "expire", "auto", ""}, {"maxout", "normal", "off", "sprint"}} {
		s := capacity.DefaultSchedule()
		s.Nights = true
		s = usageSchedule(s, usagePolicy{Posture: tc.word, active: true}, now)
		if s.Nights || s.OffDays != tc.off || s.Reserve != tc.reserve || s.ActiveOverride(now) != tc.override {
			t.Fatalf("%s: %+v", tc.word, s)
		}
		if tc.word == "careful" && s.ReservePercent != 30 {
			t.Fatal("Careful needs 30%")
		}
		s.Override = "hold"
		s = usageSchedule(s, usagePolicy{Posture: tc.word, active: true}, now)
		if s.ActiveOverride(now) != "hold" {
			t.Fatal("posture released an explicit Hold")
		}
	}
	w := Window{Allowance: 100, Used: 78, Reserved: 1, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), PaceModel: "unrestricted"}
	windows := []Window{w}
	applyUsageFloor(windows, usagePolicy{Posture: "maxout", active: true, Floor: 20})
	if _, ok := fits(windows[0], now, 1); !ok {
		t.Fatal("hold up to floor rejected")
	}
	if _, ok := fits(windows[0], now, 2); ok {
		t.Fatal("reservation crossed floor")
	}
	p := routeRank(Account{MaxParallel: 10}, windows, 0, map[string]int64{"": 1}, now)
	if math.Abs(p.cap-1) > .01 || p.slots != 1 {
		t.Fatalf("advice crossed floor: %+v", p)
	}
}

// Risk: Max out uses a long-window reset rather than the first expiring window,
// or missing resets and ties make account ordering unstable.
func TestMaxOutSoonestResetAndCarefulProjectedOrder(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	rank := func(id, posture string, short, long time.Duration, used int64) ranked {
		windows := []Window{{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(short), Allowance: 100, Used: used, PaceModel: "unrestricted", capacityKind: "5h", usagePosture: posture}, {StartsAt: now.Add(-7 * 24 * time.Hour), EndsAt: now.Add(long), Allowance: 100, Used: used, PaceModel: "unrestricted", capacityKind: "weekly", usagePosture: posture}}
		return routeRank(Account{ID: id, MaxParallel: 3}, windows, 0, map[string]int64{"": 1}, now)
	}
	picks := []ranked{rank("later", "maxout", 2*time.Hour, 24*time.Hour, 0), rank("sooner", "maxout", time.Hour, 7*24*time.Hour, 50), {account: Account{ID: "unknown"}, posture: "maxout"}}
	orderPicks(picks)
	if picks[0].account.ID != "sooner" || picks[2].account.ID != "unknown" {
		t.Fatalf("wrong expiry order %+v", picks)
	}
	picks = []ranked{rank("high", "careful", time.Hour, time.Hour, 70), rank("low", "careful", 2*time.Hour, 24*time.Hour, 10)}
	orderPicks(picks)
	if picks[0].account.ID != "low" {
		t.Fatal("Careful must prefer lowest projected use")
	}
}

// Risk: a non-owner, agent or stale screen changes the account's posture; an
// admin floor is ignored during admission/claim or via another shared door.
func TestUsageOwnerOverrideSharedFloorAdmissionAndGuard(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "posture", "person", "Owner", []string{"admin"})
	peer := addPrincipal(t, owner.TenantID, "person", "Peer", []string{"admin"})
	runner := addPrincipal(t, owner.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe", "account.read"})
	mod := accountsMod()
	var a, b Account
	for _, v := range []struct {
		key string
		a   *Account
	}{{"first", &a}, {"second", &b}} {
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"`+v.key+`","harness":"codex","daemon_id":"daemon-a","label":"`+v.key+`"}`, 201, v.a)
		ownFixtureAccount(t, owner, v.a)
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+v.a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	}
	path := "/api/agent-accounts/" + a.ID
	body := `{"posture":"maxout","revision":0,"binding_revision":0}`
	callStatus(t, mod, &peer, "", "PUT", path+"/posture", body, 403, nil)
	callStatus(t, mod, &runner, token, "PUT", path+"/posture", body, 403, nil)
	var saved usagePolicy
	callStatus(t, mod, &owner, "", "PUT", path+"/posture", body, 200, &saved)
	if saved.Posture != "maxout" || saved.Source != "account" || saved.Revision != 1 {
		t.Fatal(saved)
	}
	callStatus(t, mod, &owner, "", "PUT", path+"/posture", body, 409, nil)
	callStatus(t, mod, &peer, "", "PUT", path+"/floor", `{"floor_percent":20,"revision":1,"binding_revision":0}`, 200, &saved)
	if saved.Floor != 20 || saved.Revision != 2 {
		t.Fatal(saved)
	}
	codexProfile(t, owner)
	now := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_pool_fingerprint='confirmed' WHERE id=ANY($1::uuid[])`, []string{a.ID, b.ID}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_pref_profiles(tenant_id,scope,person_id,usage) VALUES($1,'person',$2,'maxout')`, owner.TenantID, owner.ID); err != nil {
			return err
		}
		u, err := loadUsagePolicy(t.Context(), tx, b.ID)
		if err != nil {
			return err
		}
		if u.Posture != "maxout" || u.Source != "person" || u.Floor != 20 {
			t.Fatalf("shared floor/inheritance: %+v", u)
		}
		account, err := getAccount(t.Context(), tx, b.ID)
		if err != nil {
			return err
		}
		account.LastProbeAt = &now
		w := Window{AccountID: b.ID, Allowance: 100, Used: 80, Unit: "requests", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), PaceModel: "unrestricted"}
		for _, override := range []string{"", "now"} {
			windows, wait, err := admission(t.Context(), tx, account, []Window{w}, now, 0, runRow{Purpose: "managed", CapacityOverride: override}, false)
			if err != nil {
				return err
			}
			if windows != nil || wait == nil || wait.Code != "allowance" || wait.RunNowAllowed {
				t.Fatalf("floor bypass: %+v %+v", windows, wait)
			}
		}
		// An already held run must not launch after the floor was raised.
		w.Reserved = 1
		_, wait, err := admission(t.Context(), tx, account, []Window{w}, now, 0, runRow{Purpose: "managed"}, true)
		if err != nil {
			return err
		}
		if wait == nil || wait.Code != "allowance" {
			t.Fatalf("claim bypass: %+v", wait)
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,link_revision=link_revision+1 WHERE id=$1`, a.ID, peer.ID); err != nil {
			return err
		}
		u, err = loadUsagePolicy(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		if u.Source == "account" {
			t.Fatal("new owner inherited old override")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	callStatus(t, mod, &owner, "", "PUT", path+"/posture", `{"posture":"careful","revision":2,"binding_revision":0}`, 403, nil)
	callStatus(t, mod, &peer, "", "PUT", path+"/posture", `{"posture":"careful","revision":2,"binding_revision":0}`, 409, nil)
	var page struct {
		Accounts []guardPosture `json:"accounts"`
		More     bool           `json:"has_more"`
		Cursor   string         `json:"next_cursor"`
	}
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/posture?harness=codex&limit=1", "", 200, &page)
	if len(page.Accounts) != 1 || !page.More || page.Cursor == "" || page.Accounts[0].Floor != 20 || page.Accounts[0].Boost != 0 || page.Accounts[0].BoostUntil != nil {
		t.Fatalf("projection %+v", page)
	}
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/posture?harness=codex&limit=1&after="+page.Cursor, "", 200, &page)
	if len(page.Accounts) != 1 || page.More {
		t.Fatal("pagination lost account")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"math"
	"strings"
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
		for _, override := range []string{"hold", "away", "sprint"} {
			s.Override = override
			s = usageSchedule(s, usagePolicy{Posture: tc.word, active: true}, now)
			if s.ActiveOverride(now) != override {
				t.Fatalf("posture released explicit %s", override)
			}
		}
	}
	s := capacity.DefaultSchedule()
	s.Reserve, s.ReservePercent = capacity.ReserveFixed, 45
	if usageSchedule(s, usagePolicy{Posture: "careful", active: true}, now).ReservePercent != 45 {
		t.Fatal("Careful lost the allowed custom reserve")
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

// Risk: one Max out account rewrites every sibling's long-window reset, so
// selectAccount orders the others by a five-hour window, or Careful projected
// use and presence stop applying beside that Max out account.
func TestMaxOutSoonestResetAndCarefulProjectedOrder(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	rank := func(id, posture string, short, long time.Duration, used int64) ranked {
		windows := []Window{{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(short), Allowance: 100, Used: used, PaceModel: "unrestricted", capacityKind: "5h", usagePosture: posture}, {StartsAt: now.Add(-7 * 24 * time.Hour), EndsAt: now.Add(long), Allowance: 100, Used: used, PaceModel: "unrestricted", capacityKind: "weekly", usagePosture: posture}}
		return routeRank(Account{ID: id, MaxParallel: 3}, windows, 0, map[string]int64{"": 1}, now)
	}
	at := func(picks []ranked, id string) int {
		for i, p := range picks {
			if p.account.ID == id {
				return i
			}
		}
		return -1
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
	// A Max out sibling keeps its own soonest-window rank. It must leave each
	// Careful account's published ResetsAt on the weekly window, and it must
	// not replace Careful's projected-use order with the five-hour window.
	low := rank("low", "careful", 2*time.Hour, 48*time.Hour, 10)
	high := rank("high", "careful", time.Hour, 24*time.Hour, 70)
	burst := rank("burst", "maxout", 30*time.Minute, 7*24*time.Hour, 0)
	laterMax := rank("later-max", "maxout", 3*time.Hour, 36*time.Hour, 0)
	if low.reset == nil || low.soonest == nil || high.reset == nil || burst.reset == nil || laterMax.reset == nil || low.soonest.Equal(*low.reset) || high.soonest.Equal(*high.reset) || burst.soonest.Equal(*burst.reset) {
		t.Fatal("fixture collapsed the short window and the long reset")
	}
	lowLong, highLong, burstLong, laterLong := *low.reset, *high.reset, *burst.reset, *laterMax.reset
	picks = []ranked{high, burst, low, laterMax}
	orderPicks(picks)
	for _, id := range []string{"low", "high", "burst", "later-max"} {
		if at(picks, id) < 0 {
			t.Fatalf("lost %s", id)
		}
	}
	published := map[string]CapacityRouting{
		"low":       {ResetsAt: picks[at(picks, "low")].reset},
		"high":      {ResetsAt: picks[at(picks, "high")].reset},
		"burst":     {ResetsAt: picks[at(picks, "burst")].reset},
		"later-max": {ResetsAt: picks[at(picks, "later-max")].reset},
	}
	if published["low"].ResetsAt == nil || !published["low"].ResetsAt.Equal(lowLong) || published["high"].ResetsAt == nil || !published["high"].ResetsAt.Equal(highLong) {
		t.Fatalf("Max out sibling moved Careful ResetsAt off the long window: low %v high %v", published["low"].ResetsAt, published["high"].ResetsAt)
	}
	if published["burst"].ResetsAt == nil || !published["burst"].ResetsAt.Equal(burstLong) || published["later-max"].ResetsAt == nil || !published["later-max"].ResetsAt.Equal(laterLong) {
		t.Fatal("Max out replaced its own long-window ResetsAt")
	}
	if at(picks, "low") > at(picks, "high") {
		t.Fatalf("Careful projected order lost beside Max out: %+v", picks)
	}
	if at(picks, "burst") > at(picks, "later-max") {
		t.Fatalf("Max out did not keep soonest-window order: %+v", picks)
	}
	soon, later, far := now.Add(time.Hour), now.Add(24*time.Hour), now.Add(72*time.Hour)
	picks = []ranked{{account: Account{ID: "present"}, presence: true, reset: &soon}, {account: Account{ID: "absent"}, reset: &later}, {account: Account{ID: "burst"}, posture: "maxout", soonest: &far, reset: &far}}
	orderPicks(picks)
	if picks[0].account.ID != "absent" || picks[at(picks, "absent")].reset == nil || !picks[at(picks, "absent")].reset.Equal(later) {
		t.Fatalf("presence no longer precedes the long reset beside Max out: %+v", picks)
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
	callStatus(t, mod, &owner, "", "PUT", path+"/posture", `{"revision":0,"binding_revision":0}`, 400, nil)
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
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=repeat('a',64),quota_pool_fingerprint=repeat('a',64) WHERE id=ANY($1::uuid[])`, []string{a.ID, b.ID}); err != nil {
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
		// The shared-quota loader re-reads the durable ledger, so retain the
		// window being asserted rather than only passing an in-memory fixture.
		if err := tx.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model) VALUES($1,$2,$3,$4,'requests',100,80,'unrestricted') RETURNING id::text`, owner.TenantID, b.ID, w.StartsAt, w.EndsAt).Scan(&w.ID); err != nil {
			return err
		}
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
		if _, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET reserved=1 WHERE id=$1`, w.ID); err != nil {
			return err
		}
		_, wait, err := admission(t.Context(), tx, account, []Window{w}, now, 0, runRow{Purpose: "managed"}, true)
		if err != nil {
			return err
		}
		if wait == nil || wait.Code != "allowance" {
			t.Fatalf("claim bypass: %+v", wait)
		}
		// A hard floor also blocks a blind attempt when no numeric window is
		// known, including an explicit Run now. Unknown never means headroom.
		if _, err := tx.Exec(t.Context(), `DELETE FROM account_allowance_windows WHERE id=$1`, w.ID); err != nil {
			return err
		}
		for _, override := range []string{"", "now"} {
			windows, wait, err := admission(t.Context(), tx, account, nil, now, 0, runRow{Purpose: "managed", CapacityOverride: override}, false)
			if err != nil {
				return err
			}
			if windows != nil || wait == nil || wait.Code != "reading" || wait.RunNowAllowed {
				t.Fatalf("unknown usage bypassed floor: %+v %+v", windows, wait)
			}
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
	// Even an admin who owns one door cannot read a private sibling's
	// learned reserve or routing order through the compact guard endpoint.
	callStatus(t, mod, &peer, "", "GET", "/api/agent-accounts/posture?harness=codex", "", 200, &page)
	if len(page.Accounts) != 0 {
		t.Fatal("guard exposed private shared-quota details")
	}
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/posture?harness=codex&limit=1", "", 403, nil)
	dbtest.BindRole(t, testDB, owner.TenantID, runner.ID, "admin")
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/posture?harness=codex&limit=1", "", 200, &page)
	if len(page.Accounts) != 1 || !page.More || page.Cursor == "" || page.Accounts[0].Floor != 20 || page.Accounts[0].Boost != 0 || page.Accounts[0].BoostUntil != nil {
		t.Fatalf("projection %+v", page)
	}
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/posture?harness=codex&limit=1&after="+strings.ToUpper(page.Cursor), "", 200, &page)
	if len(page.Accounts) != 1 || page.More {
		t.Fatal("pagination lost account")
	}
	callStatus(t, mod, &owner, "", "PUT", "/api/agent-accounts/"+b.ID+"/posture", `{"posture":"careful","revision":0,"binding_revision":0}`, 200, &saved)
	// Linking an owner principal to another canonical person must invalidate
	// the old person's override even when the account binding did not change.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET linked_to=$2 WHERE id=$1`, owner.ID, peer.ID); err != nil {
			return err
		}
		u, err := loadUsagePolicy(t.Context(), tx, b.ID)
		if err == nil && u.Source == "account" {
			t.Fatal("canonical owner change retained the old override")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

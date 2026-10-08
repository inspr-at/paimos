// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: server timezone/DST or a midnight rollover prolongs Boost today;
// boosting a paced share bypasses a reserve, Hold or vendor allowance.
func TestBoostLocalExpiryAndSafety(t *testing.T) {
	for _, tc := range []struct{ now, zone, until string }{
		{"2026-10-08T10:00:00Z", "Europe/Vienna", "2026-10-08T21:59:00Z"},
		{"2026-03-29T00:30:00Z", "Europe/Vienna", "2026-03-29T21:59:00Z"},
		{"2026-10-25T00:30:00Z", "Europe/Vienna", "2026-10-25T22:59:00Z"},
		{"2026-10-09T02:00:00Z", "America/Los_Angeles", "2026-10-09T06:59:00Z"},
		{"2026-10-08T10:00:00Z", "Asia/Kathmandu", "2026-10-08T18:14:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		until, err := boostExpiry(now, tc.zone)
		if err != nil || until.Format(time.RFC3339) != tc.until {
			t.Fatalf("%s %s: %v %v", tc.now, tc.zone, until, err)
		}
		if same, err := boostExpiry(until, tc.zone); err != nil || !same.Equal(until) {
			t.Fatal("cutoff reopened the next day")
		}
	}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, zone := range []string{"", "Local", "invalid/zone"} {
		if _, err := boostExpiry(now, zone); err == nil {
			t.Fatal("accepted invalid local timezone")
		}
	}
	s := capacity.DefaultSchedule()
	s.Timezone = "UTC"
	p := capacity.Pacing{BudgetPercent: 20, UsedTodayPercent: 20, ReserveEffectivePercent: 30}
	got := boostPacing(p, 40, 30, s, now)
	if got.AvailableNowPercent != 10 || got.BudgetPercent != 50 {
		t.Fatalf("burst or reserve lost: %+v", got)
	}
	s.Override = "hold"
	if got := boostPacing(p, 40, 30, s, now); got.AvailableNowPercent != 0 || got.BudgetPercent != p.BudgetPercent {
		t.Fatal("boost bypassed Hold")
	}
	s.Override = ""
	night := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	if got := boostPacing(p, 40, 30, s, night); got.AvailableNowPercent != 0 {
		t.Fatal("boost opened a closed work band")
	}
	w := Window{Allowance: 100, Used: 79, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), PaceModel: "steady"}
	windows := []Window{w}
	applyUsageFloor(windows, usagePolicy{Boost: 30, Floor: 20})
	if windows[0].BurstRatio != .3 {
		t.Fatal("burst not added")
	}
	if _, ok := fits(windows[0], now, 1); !ok {
		t.Fatal("boost did not release paced room")
	}
	if _, ok := fits(windows[0], now, 2); ok {
		t.Fatal("boost crossed hard floor")
	}
	windows[0].Used = 99
	windows[0].usageCeiling = nil
	if _, ok := fits(windows[0], now, 2); ok {
		t.Fatal("boost manufactured vendor allowance")
	}
}

// Risk: stale, partial or non-owner writes commit a boost, or another owner
// inherits one; the overview/guard continues publishing a boost after 23:59.
func TestBoostAtomicOwnerWriteAndLiveExpiry(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "boost", "person", "Owner", []string{"admin"})
	peer := addPrincipal(t, owner.TenantID, "person", "Peer", []string{"admin"})
	runner := addPrincipal(t, owner.TenantID, "agent", "Runner", nil)
	token := issueKey(t, runner, []string{"account.manage"})
	if authz.RoutePermissions["PUT /api/agent-accounts/boost"] != "account.manage" {
		t.Fatal("boost route permission")
	}
	if err := authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(t.Context(), owner), appPool), "PUT /api/agent-accounts/boost", authz.Scope{}); err != nil {
		t.Fatalf("route gate denied the owner: %v", err)
	}
	var a, b Account
	for i, target := range []*Account{&a, &b} {
		callStatus(t, accountsMod(), &runner, token, "POST", "/api/agent-accounts", fmt.Sprintf(`{"account_key":"boost-%d","harness":"codex","daemon_id":"daemon-a","label":"Account"}`, i), 201, target)
		ownFixtureAccount(t, owner, target)
	}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	request := func(who tenant.Principal, method, path, body string, status int) []byte {
		t.Helper()
		mux := http.NewServeMux()
		accountsMod().Mount(mux)
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		ctx := tenant.WithPrincipal(context.WithValue(r.Context(), clockKey{}, now), who)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r.WithContext(ctx))
		if rec.Code != status {
			t.Fatalf("%s: status %d want %d: %s", path, rec.Code, status, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	body := func(percent, revA, revB int) string {
		return fmt.Sprintf(`{"boost_percent":%d,"timezone":"Europe/Vienna","accounts":[{"account_id":%q,"revision":%d,"binding_revision":0},{"account_id":%q,"revision":%d,"binding_revision":0}]}`, percent, a.ID, revA, b.ID, revB)
	}
	request(peer, "PUT", "/api/agent-accounts/boost", body(20, 0, 0), 403)
	request(runner, "PUT", "/api/agent-accounts/boost", body(20, 0, 0), 403)
	request(owner, "PUT", "/api/agent-accounts/boost", body(15, 0, 0), 400)
	partial := fmt.Sprintf(`{"boost_percent":20,"timezone":"Europe/Vienna","accounts":[{"account_id":%q,"revision":0,"binding_revision":0}]}`, a.ID)
	request(owner, "PUT", "/api/agent-accounts/boost", partial, 409)
	request(owner, "PUT", "/api/agent-accounts/boost", body(20, 0, 1), 409)
	var saved struct {
		Accounts []usagePolicy `json:"accounts"`
	}
	if err := json.Unmarshal(request(owner, "PUT", "/api/agent-accounts/boost", body(20, 0, 0), 200), &saved); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 10, 8, 21, 59, 0, 0, time.UTC)
	for _, u := range saved.Accounts {
		if u.Boost != 20 || u.BoostUntil == nil || !u.BoostUntil.Equal(until) || u.Revision != 1 {
			t.Fatalf("atomic save: %+v", u)
		}
	}
	if len(saved.Accounts) != 2 {
		t.Fatal("partial success")
	}
	request(owner, "PUT", "/api/agent-accounts/boost", body(30, 0, 0), 409)
	check := func(boost int) {
		t.Helper()
		var page struct {
			Accounts []guardPosture `json:"accounts"`
		}
		if err := json.Unmarshal(request(owner, "GET", "/api/agent-accounts/posture?harness=codex", "", 200), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Accounts) != 2 {
			t.Fatal("guard lost owned accounts")
		}
		for _, p := range page.Accounts {
			if p.Boost != boost || (p.BoostUntil != nil) != (boost != 0) {
				t.Fatalf("guard expiry: %+v", p)
			}
		}
		var overview overviewPage
		if err := json.Unmarshal(request(owner, "GET", "/api/agent-accounts/overview", "", 200), &overview); err != nil {
			t.Fatal(err)
		}
		for _, a := range overview.Accounts {
			if a.UsagePolicy == nil || a.UsagePolicy.Boost != boost {
				t.Fatal("overview lost live boost")
			}
		}
	}
	now = until.Add(-time.Microsecond)
	check(20)
	now = until
	check(0)
	request(owner, "PUT", "/api/agent-accounts/boost", body(30, 1, 1), 409)
	request(owner, "PUT", "/api/agent-accounts/boost", body(0, 1, 1), 200)
	now = until.Add(-time.Hour)
	request(owner, "PUT", "/api/agent-accounts/boost", body(30, 2, 2), 200)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,link_revision=link_revision+1 WHERE id=$1`, a.ID, peer.ID); err != nil {
			return err
		}
		ctx := context.WithValue(t.Context(), clockKey{}, now)
		u, err := loadUsagePolicy(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if u.Boost != 0 || u.BoostUntil != nil {
			t.Fatal("rebinding inherited old boost")
		}
		if _, err := tx.Exec(t.Context(), `UPDATE principals SET linked_to=$2 WHERE id=$1`, owner.ID, peer.ID); err != nil {
			return err
		}
		u, err = loadUsagePolicy(ctx, tx, b.ID)
		if err == nil && u.Boost != 0 {
			t.Fatal("canonical owner change inherited boost")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request(owner, "PUT", "/api/agent-accounts/boost", body(10, 3, 3), 409)
}

// Risk: a private pool sibling or a linked session makes the privacy-visible
// account list shorter than the canonical-owned list, so every save returns
// 409, or the control has nothing it is allowed to send.
func TestBoostAppliesWithheldOwnedAccounts(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "boost-withheld", "person", "Owner", []string{"admin"})
	peer := addPrincipal(t, owner.TenantID, "person", "Peer", []string{"admin"})
	alias := addPrincipal(t, owner.TenantID, "person", "Alias", []string{"admin"})
	runner := addPrincipal(t, owner.TenantID, "agent", "Runner", nil)
	token := issueKey(t, runner, []string{"account.manage"})
	var visible, hidden, stranger Account
	for i, target := range []*Account{&visible, &hidden, &stranger} {
		callStatus(t, accountsMod(), &runner, token, "POST", "/api/agent-accounts", fmt.Sprintf(`{"account_key":"withheld-%d","harness":"codex","daemon_id":"daemon-a","label":"Account"}`, i), 201, target)
	}
	ownFixtureAccount(t, owner, &visible)
	ownFixtureAccount(t, owner, &hidden)
	ownFixtureAccount(t, peer, &stranger)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=$2,quota_pool_fingerprint=$2 WHERE id=$1 OR id=$3`, hidden.ID, strings.Repeat("ab", 32), stranger.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE principals SET linked_to=$2 WHERE id=$1`, alias.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	request := func(who tenant.Principal, body string, status int) []byte {
		t.Helper()
		mux := http.NewServeMux()
		accountsMod().Mount(mux)
		r := httptest.NewRequest("PUT", "/api/agent-accounts/boost", strings.NewReader(body))
		ctx := tenant.WithPrincipal(context.WithValue(r.Context(), clockKey{}, now), who)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r.WithContext(ctx))
		if rec.Code != status {
			t.Fatalf("status %d want %d: %s", rec.Code, status, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	one := func(id string, revision int) string {
		return fmt.Sprintf(`{"boost_percent":10,"timezone":"Europe/Vienna","accounts":[{"account_id":%q,"revision":%d,"binding_revision":0}]}`, id, revision)
	}
	both := fmt.Sprintf(`{"boost_percent":10,"timezone":"Europe/Vienna","accounts":[{"account_id":%q,"revision":0,"binding_revision":0},{"account_id":%q,"revision":0,"binding_revision":0}]}`, hidden.ID, visible.ID)
	request(owner, both, 409)
	var saved struct {
		Accounts      []usagePolicy `json:"accounts"`
		Withheld      int           `json:"withheld_count"`
		WithheldUntil *time.Time    `json:"withheld_boost_until"`
	}
	raw := request(owner, one(visible.ID, 0), 200)
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 10, 8, 21, 59, 0, 0, time.UTC)
	if len(saved.Accounts) != 1 || saved.Accounts[0].AccountID != visible.ID || saved.Accounts[0].Boost != 10 || saved.Accounts[0].Revision != 1 || saved.Withheld != 1 || saved.WithheldUntil == nil || !saved.WithheldUntil.Equal(until) || strings.Contains(string(raw), hidden.ID) {
		t.Fatalf("visible response leaked or dropped the withheld account: %+v", saved)
	}
	check := func(id string, boost int) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, owner.TenantID, func(tx pgx.Tx) error {
			u, err := loadUsagePolicy(context.WithValue(t.Context(), clockKey{}, now), tx, id)
			if err != nil {
				return err
			}
			if u.Boost != boost {
				t.Fatalf("%s boost %d", id, u.Boost)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(visible.ID, 10)
	check(hidden.ID, 10)
	check(stranger.ID, 0)
	var page struct {
		Accounts []struct {
			AccountID     string       `json:"account_id"`
			UsagePolicy   *usagePolicy `json:"usage_policy"`
			BoostWithheld bool         `json:"boost_withheld"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(requestRaw(t, owner, "GET", "/api/agent-accounts/overview", ""), &page); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range page.Accounts {
		seen[row.AccountID] = true
		switch row.AccountID {
		case hidden.ID:
			if !row.BoostWithheld || row.UsagePolicy != nil {
				t.Fatal("withheld owned account asked the client for its id")
			}
		case stranger.ID:
			if row.BoostWithheld || row.UsagePolicy != nil {
				t.Fatal("private peer looked boostable")
			}
		case visible.ID:
			if row.BoostWithheld || row.UsagePolicy == nil || !row.UsagePolicy.CanSetPosture {
				t.Fatal("visible account lost its policy")
			}
		}
	}
	if !seen[hidden.ID] || !seen[visible.ID] || !seen[stranger.ID] {
		t.Fatal("overview dropped an account")
	}
	request(owner, one(visible.ID, 0), 409)
	request(owner, fmt.Sprintf(`{"boost_percent":20,"timezone":"Europe/Vienna","accounts":[{"account_id":%q,"revision":1,"binding_revision":0},{"account_id":%q,"revision":1,"binding_revision":0}]}`, hidden.ID, visible.ID), 409)
	check(hidden.ID, 10)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET archived_at=now() WHERE id=$1 OR id=$2`, visible.ID, hidden.ID); err != nil {
		t.Fatal(err)
	}
	var linked Account
	callStatus(t, accountsMod(), &runner, token, "POST", "/api/agent-accounts", `{"account_key":"withheld-linked","harness":"codex","daemon_id":"daemon-a","label":"Linked"}`, 201, &linked)
	ownFixtureAccount(t, owner, &linked)
	empty := `{"boost_percent":10,"timezone":"Europe/Vienna","accounts":[]}`
	if err := json.Unmarshal(request(alias, empty, 200), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Accounts) != 0 || saved.Withheld != 1 || saved.WithheldUntil == nil || !saved.WithheldUntil.Equal(until) {
		t.Fatalf("linked session: %+v", saved)
	}
	check(linked.ID, 10)
	request(alias, one(linked.ID, 0), 409)
	check(linked.ID, 10)
}

func requestRaw(t *testing.T, who tenant.Principal, method, path, body string) []byte {
	t.Helper()
	mux := http.NewServeMux()
	accountsMod().Mount(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r.WithContext(tenant.WithPrincipal(r.Context(), who)))
	if rec.Code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestCatalogCascadeMetadataAndRouting(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	other := addPrincipal(t, admin.TenantID, "agent", "other", nil)
	member := addPrincipal(t, admin.TenantID, "person", "member", []string{"member"})
	foreign := makePrincipal(t, "foreign", "person", "Foreign", []string{"admin"})
	profile := codexProfile(t, admin)
	foreignProfile := codexProfile(t, foreign)
	token := issueKey(t, runner, []string{"account.manage"})
	mod := accountsMod()
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/catalog", "", 403, nil)
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	var otherProfile, wrongHarness, disabled string
	seed(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled) VALUES($1,'disabled','1','codex','openai','disabled-model','high','standard',false) RETURNING id::text`, admin.TenantID).Scan(&disabled); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'second','1','codex','openai','second-model','high','standard') RETURNING id::text`, admin.TenantID).Scan(&otherProfile); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'other','1','claude','anthropic','other-model','high','standard') RETURNING id::text`, admin.TenantID).Scan(&wrongHarness); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2),($1,'build',2,$3),($1,'review-gate',1,$2)`, admin.TenantID, otherProfile, profile)
		return err
	})
	register := func(key, daemon string) Account {
		t.Helper()
		var a Account
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", fmt.Sprintf(`{"account_key":%q,"harness":"codex","daemon_id":%q,"label":"Initial"}`, key, daemon), 201, &a)
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", fmt.Sprintf(`{"daemon_id":%q,"daemon_generation":"g1","available":true,"host_label":"Studio"}`, daemon), 200, nil)
		callStatus(t, mod, &admin, "", "POST", "/api/agent-accounts/"+a.ID+"/windows", windowBody(time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "requests", 100, "unrestricted"), 201, nil)
		return a
	}
	a, b, c := register("local-a", "daemon-a"), register("local-b", "daemon-a"), register("local-c", "daemon-b")
	metadata := func(ids []string) string {
		b, err := json.Marshal(map[string]any{"label": "Work subscription", "plan": "Pro", "host_label": "Studio", "allowed_model_profile_ids": ids})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	path := "/api/agent-accounts/" + a.ID + "/metadata"
	for _, ids := range [][]string{nil, {profile, profile}, {foreignProfile}, {wrongHarness}, {disabled}, {"invalid"}} {
		callStatus(t, mod, &admin, "", "PUT", path, metadata(ids), 400, nil)
	}
	callStatus(t, mod, &admin, "", "PUT", path, strings.Replace(metadata([]string{profile}), `"plan":"Pro"`, `"plan":"sk-synthetic-rejected"`, 1), 400, nil)
	callStatus(t, mod, &admin, "", "PUT", path, strings.Replace(metadata([]string{profile}), `"plan":"Pro"`, `"home":"/private/local","plan":"Pro"`, 1), 400, nil)
	callStatus(t, mod, &member, "", "PUT", path, metadata([]string{profile}), 403, nil)
	callStatus(t, mod, &other, issueKey(t, other, []string{"account.manage"}), "PUT", path, metadata([]string{profile}), 403, nil)
	callStatus(t, mod, &runner, issueKey(t, runner, nil), "PUT", path, metadata([]string{profile}), 403, nil)
	callStatus(t, mod, &foreign, "", "PUT", path, metadata([]string{foreignProfile}), 404, nil)
	callStatus(t, mod, &runner, token, "PUT", path, metadata([]string{profile}), 200, &a)
	callStatus(t, mod, &runner, token, "PUT", path, metadata([]string{profile}), 200, nil)
	if scalar(t, admin, `SELECT count(*) FROM events WHERE type='account.updated'`) != 1 {
		t.Fatal("metadata replay was not idempotent")
	}
	if a.Plan != "Pro" || a.Label != "Work subscription" || !slices.Equal(a.AllowedProfileIDs, []string{profile}) {
		t.Fatal("metadata projection lost fields")
	}
	// The grant denies the first role route and preserves the second route's pin.
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET used=70 WHERE account_id=$1`, b.ID)
		return err
	})
	var catalog Catalog
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog", "", 200, &catalog)
	if len(catalog.Hosts) != 2 || catalog.Hosts[0].DaemonID != "daemon-a" || catalog.Hosts[0].Label != "Studio" {
		t.Fatalf("host cascade: %+v", catalog.Hosts)
	}
	h := catalog.Hosts[0].Harnesses[0]
	if h.Harness != "codex" || h.DefaultAccountID == nil || *h.DefaultAccountID != a.ID || len(h.Accounts) != 2 {
		t.Fatalf("harness cascade: %+v", h)
	}
	var choice CatalogAccount
	for _, v := range h.Accounts {
		if v.ID == a.ID {
			choice = v
		}
	}
	if !choice.Available || choice.DefaultProfileID == nil || *choice.DefaultProfileID != profile || len(choice.Models) != 1 || choice.Models[0].Efforts[0].ProfileID != profile || choice.Windows[0].Remaining != 100 || choice.Windows[0].PaceRemaining != 100 {
		t.Fatalf("account cascade: %+v", choice)
	}
	_, raw := call(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog", "")
	if strings.Contains(string(raw), "account_key") || strings.Contains(string(raw), "local-a") {
		t.Fatal("local routing key leaked into UI catalog")
	}
	if strings.Contains(string(raw), disabled) || strings.Contains(string(raw), "disabled-model") {
		t.Fatal("disabled profile offered in catalog")
	}
	callStatus(t, mod, &member, "", "GET", "/api/agent-accounts/catalog", "", 403, nil)
	callStatus(t, mod, nil, "", "GET", "/api/agent-accounts/catalog", "", 401, nil)
	for _, query := range []string{"?role=invalid", "?role=review-gate", "?author_family=invalid"} {
		callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog"+query, "", 400, nil)
	}
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog?role=review-gate&author_family=openai", "", 200, &catalog)
	if len(catalog.Hosts[0].Harnesses[0].Accounts[0].Models) != 0 {
		t.Fatal("review offered author family")
	}
	callStatus(t, mod, &foreign, "", "GET", "/api/agent-accounts/catalog", "", 200, &catalog)
	if len(catalog.Hosts) != 0 {
		t.Fatal("catalog crossed tenant boundary")
	}
	// A forbidden profile cannot reserve even with fresh probes and free allowance.
	run := insertRun(t, admin, runner, disabled)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{a, b}, map[string]int64{"requests": 1}), 409, nil)
	if scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE run_id=$1`, run) != 0 {
		t.Fatal("disabled model silently fell back or reserved allowance")
	}
	run = insertRun(t, admin, runner, otherProfile)
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{a}, map[string]int64{"requests": 1}), 409, nil)
	if scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE run_id=$1`, run) != 0 {
		t.Fatal("denied profile reserved")
	}
	run = insertRun(t, admin, runner, profile)
	route := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{a, b}, map[string]int64{"requests": 1})
	if route.AccountID != a.ID || route.AccountLabel != "Work subscription" {
		t.Fatal("selected account label missing")
	}
	if again := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{a, b}, map[string]int64{"requests": 1}); again.AccountLabel != route.AccountLabel {
		t.Fatal("route replay lost label")
	}
	callStatus(t, mod, &admin, "", "PUT", "/api/agent-accounts/"+c.ID+"/metadata", metadata([]string{}), 200, &c)
	if c.AllowedProfileIDs == nil {
		t.Fatal("empty explicit grant reverted to inherited policy")
	}
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog", "", 200, &catalog)
	if catalog.Hosts[1].Harnesses[0].DefaultAccountID != nil || catalog.Hosts[1].Harnesses[0].Accounts[0].Available {
		t.Fatal("empty grants permit dispatch")
	}
	// Active role suppression disappears at expiry, without changing a profile.
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET state='conserved',reason='test',valid_until=now()+interval '1 hour' WHERE role='build' AND profile_id=$1`, profile)
		return err
	})
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog", "", 200, &catalog)
	for _, item := range catalog.Hosts[0].Harnesses[0].Accounts {
		if item.ID == a.ID && len(item.Models) != 0 {
			t.Fatal("suppressed role profile offered")
		}
	}
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET valid_until=now()-interval '1 second' WHERE role='build' AND profile_id=$1`, profile)
		return err
	})
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts/catalog", "", 200, &catalog)
	for _, item := range catalog.Hosts[0].Harnesses[0].Accounts {
		if item.ID == a.ID && len(item.Models) != 1 {
			t.Fatal("expired suppression still applied")
		}
	}
	fullLabel := strings.Repeat("界", 128)
	body := strings.Replace(metadata([]string{profile}), "Work subscription", fullLabel, 1)
	callStatus(t, mod, &admin, "", "PUT", path, body, 200, &a)
	if a.Label != fullLabel {
		t.Fatal("full-length label did not survive storage")
	}
	callStatus(t, mod, &admin, "", "PUT", path, strings.Replace(body, fullLabel, fullLabel+"x", 1), 400, nil)
}

func TestCatalogDefaultUsesOfferedProfileAndDeterministicAccount(t *testing.T) {
	now := time.Now()
	yes, priority := true, 1
	base := Account{DaemonID: "host", Harness: "codex", State: "available", MaxParallel: 1, LastProbeAt: &now, LastProbeOK: &yes,
		Windows: []Window{{Allowance: 100, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), PaceModel: "unrestricted"}}}
	first, second := base, base
	first.ID, first.AllowedProfileIDs = "a", []string{"allowed"}
	second.ID, second.AllowedProfileIDs = "b", []string{"allowed"}
	profiles := []catalogProfile{
		{ID: "denied", Harness: "codex", Model: "denied-model", Effort: "high", Priority: &priority},
		{ID: "allowed", Harness: "codex", Model: "allowed-model", Effort: "high"},
	}
	got := buildCatalog([]Account{second, first}, profiles, nil, "build", now).Hosts[0].Harnesses[0]
	if got.DefaultAccountID == nil || *got.DefaultAccountID != first.ID {
		t.Fatal("equal allowances did not use deterministic account ID tie-break")
	}
	for _, a := range got.Accounts {
		if a.DefaultProfileID != nil || len(a.Models) != 1 || a.Models[0].Efforts[0].ProfileID != "allowed" {
			t.Fatal("catalog invented a default or offered an ungranted profile")
		}
	}
	profiles[1].Priority = &priority
	got = buildCatalog([]Account{second, first}, profiles, map[string]int{"a": 1}, "build", now).Hosts[0].Harnesses[0]
	if got.DefaultAccountID == nil || *got.DefaultAccountID != second.ID || got.Accounts[1].DefaultProfileID == nil || *got.Accounts[1].DefaultProfileID != "allowed" {
		t.Fatal("catalog default ignored capacity or the account's eligible role route")
	}
}

func TestAccountMetadataCharacterBoundary(t *testing.T) {
	for _, value := range []string{strings.Repeat("a", 128), strings.Repeat("界", 128)} {
		got, err := metadataText(value, false)
		if err != nil || got != value {
			t.Fatal("128-character metadata was rejected or changed")
		}
		if _, err := metadataText(value+"x", false); err == nil {
			t.Fatal("129-character metadata accepted")
		}
	}
}

func TestCatalogAvailabilityAndAllowanceBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	yes := true
	base := Account{ID: "account", Harness: "codex", State: "available", MaxParallel: 1, LastProbeAt: &now, LastProbeOK: &yes,
		Windows: []Window{{Allowance: 100, Used: 20, Reserved: 10, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), PaceModel: "steady"}}}
	profiles := []catalogProfile{{ID: "p", Harness: "codex", Model: "model", Effort: "high"}}
	a := catalogAccount(base, profiles, 0, now)
	if !a.Available || a.RemainingFraction == nil || *a.RemainingFraction != 0.7 || a.Windows[0].PaceRemaining != 20 || a.DefaultProfileID != nil {
		t.Fatalf("pace/default: %+v", a)
	}
	for _, tc := range []struct {
		name, reason string
		change       func(*Account)
		used         int
	}{
		{"draining", "state", func(a *Account) { a.State = "draining" }, 0},
		{"stale", "probe", func(a *Account) { old := now.Add(-ProbeFreshness - time.Second); a.LastProbeAt = &old }, 0},
		{"missing", "probe", func(a *Account) { a.LastProbeAt = nil }, 0},
		{"occupied", "capacity", func(a *Account) {}, 1},
		{"no-window", "allowance", func(a *Account) { a.Windows = nil }, 0},
		{"expired", "allowance", func(a *Account) { a.Windows[0].EndsAt = now }, 0},
		{"pace-blocked", "allowance", func(a *Account) { a.Windows[0].Used = 50 }, 0},
		{"no-grants", "models", func(a *Account) { a.AllowedProfileIDs = []string{} }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			a.Windows = slices.Clone(base.Windows)
			tc.change(&a)
			got := catalogAccount(a, profiles, tc.used, now)
			if got.Available || !slices.Contains(got.UnavailableReasons, tc.reason) {
				t.Fatalf("eligibility: %+v", got)
			}
		})
	}
}

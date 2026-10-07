// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/jackc/pgx/v5"
	"slices"
	"strings"
	"testing"
	"time"
)

// Risk: a daemon must not enable its own login reads or commit observations
// after the owner opts out or the ownership binding changes.
func TestUsageProbeOwnerConsentFencesReadingsAndBinding(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "usage-probe-consent", now)
	path := "/api/agent-accounts/" + f.account.ID
	other := addPrincipal(t, f.admin.TenantID, "person", "Other admin", []string{"admin"})
	on := `{"binding_revision":0,"enabled":true}`
	callStatus(t, f.mod, &other, "", "PUT", path+"/usage-probe", on, 403, nil)
	callStatus(t, f.mod, &f.runner, f.token, "PUT", path+"/usage-probe", on, 403, nil)
	callStatus(t, f.mod, &f.admin, "", "PUT", path+"/usage-probe", `{"binding_revision":1,"enabled":true}`, 409, nil)
	r := capacity.Reading{Source: "agentd", ReadAt: now, WindowKind: "5h", WindowMinutes: 300, UsedPercent: 17, ResetsAt: now.Add(time.Hour)}
	revision := int64(0)
	body := encoded(t, usageReadingsWrite{readingsWrite: readingsWrite{[]capacity.Reading{r}}, UsageProbe: true, Revision: &revision})
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", body, 403, nil)
	if n := scalar(t, f.admin, `SELECT count(*) FROM account_capacity_readings WHERE account_id=$1`, f.account.ID); n != 0 {
		t.Fatal("default-off wrote observations")
	}
	callStatus(t, f.mod, &f.admin, "", "PUT", path+"/usage-probe", on, 204, nil)
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", body, 204, nil)
	if c := f.capacity(t); len(c.Windows) != 1 || c.Windows[0].Reading.Source != "agentd" || c.Windows[0].Reading.UsedPercent != 17 {
		t.Fatal("idle reading did not project a live window")
	}
	var ready struct {
		Items []AccountReadiness `json:"items"`
	}
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &ready)
	if len(ready.Items) != 1 || ready.Items[0].DisplayReason == UsageUnknownReason {
		t.Fatal("idle reading remained usage unknown")
	}
	callStatus(t, f.mod, &f.admin, "", "PUT", path+"/usage-probe", `{"binding_revision":0,"enabled":false}`, 204, nil)
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", body, 403, nil)
	callStatus(t, f.mod, &f.admin, "", "PUT", path+"/usage-probe", on, 204, nil)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET link_revision=link_revision+1 WHERE id=$1`, f.account.ID); err != nil {
		t.Fatal(err)
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", body, 409, nil)
	var a []Account
	callStatus(t, f.mod, &f.runner, f.token, "GET", "/api/agent-accounts", "", 200, &a)
	if len(a) != 1 || a[0].UsageProbeEnabled {
		t.Fatal("owner consent survived binding change")
	}
	// Reject credential-shaped request extensions before persistence.
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", strings.TrimSuffix(body, "}")+`,"access_token":"synthetic-login-value"}`, 400, nil)
	if _, err := adminPool.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.admin.TenantID, f.admin.ID); err != nil {
		t.Fatal(err)
	}
	callStatus(t, f.mod, &f.admin, "", "PUT", path+"/usage-probe", `{"binding_revision":1,"enabled":true}`, 403, nil)
}

// Risk: dollar limits must retain their unit and unknown total balance, with
// the existing private-owner boundary applied to accounts and capacity alike.
func TestUsageProbeDollarBudgetStorageAndPrivacy(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	reset(t)
	f := limitFixture{admin: makePrincipal(t, "usage-probe-budget", "person", "Owner", []string{"admin"})}
	f.runner = addPrincipal(t, f.admin.TenantID, "agent", "runner", []string{"admin"})
	f.token = issueKey(t, f.runner, []string{"account.manage", "account.probe"})
	f.mod = fixedClockModule{Module: accountsMod(), at: now}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"main","harness":"pi","daemon_id":"daemon-a","label":"Main"}`, 201, &f.account)
	ownFixtureAccount(t, f.admin, &f.account)
	seed(t, f.admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'usage-pi','1','pi','openai','test','high','strong')`, f.admin.TenantID)
		return err
	})
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET provider='openrouter' WHERE id=$1`, f.account.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/agent-accounts/" + f.account.ID
	callStatus(t, f.mod, &f.admin, "", "PUT", path+"/usage-probe", `{"binding_revision":0,"enabled":true}`, 204, nil)
	limit, remaining := 50.0, 37.5
	b := capacity.Budget{Currency: "USD", Source: "agentd", ReadAt: now, KeyUsageUSD: 12.5, KeyLimitUSD: &limit, KeyRemainingUSD: &remaining}
	zero := int64(0)
	input := usageReadingsWrite{readingsWrite: readingsWrite{[]capacity.Reading{}}, Budget: &b, UsageProbe: true, Revision: &zero}
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", encoded(t, input), 204, nil)
	c := f.capacity(t)
	if c.Budget == nil || c.Budget.KeyUsageUSD != 12.5 || c.Budget.BalanceUSD != nil || len(c.Windows) != 0 {
		t.Fatal("USD budget lost or converted into quota")
	}
	older := b
	older.ReadAt = now.Add(-time.Minute)
	older.KeyUsageUSD = 1
	input.Budget = &older
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", encoded(t, input), 204, nil)
	if f.capacity(t).Budget.KeyUsageUSD != 12.5 {
		t.Fatal("older budget replaced latest")
	}
	// A zero cap is a durable stop. A later unknown cap cannot erase it;
	// only a later positive observation clears the key-specific stop.
	checkCap := func(at time.Time, cap, left *float64, wantTry bool) {
		t.Helper()
		input.Budget = &capacity.Budget{Currency: "USD", Source: "agentd", ReadAt: at, KeyUsageUSD: 12.5, KeyLimitUSD: cap, KeyRemainingUSD: left}
		callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/readings", encoded(t, input), 204, nil)
		var ready struct {
			Items []AccountReadiness `json:"items"`
		}
		callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &ready)
		if len(ready.Items) != 1 || ready.Items[0].CanTry != wantTry {
			t.Fatal("usage probe key cap did not fence readiness")
		}
		if !wantTry && !slices.Contains(ready.Items[0].ReasonCodes, "key_cap_exhausted") {
			t.Fatal("readiness blocked for a different reason")
		}
	}
	zeroCap := 0.0
	checkCap(now.Add(time.Second), &zeroCap, nil, false)
	checkCap(now.Add(2*time.Second), nil, nil, false)
	checkCap(now.Add(3*time.Second), &limit, &remaining, true)
	callStatus(t, f.mod, &f.runner, f.token, "POST", path+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, OpenRouterCredits: &openrouter.Credits{ObservedAt: now.Add(10 * time.Second), Limit: &zeroCap}}), 200, nil)
	checkCap(now.Add(4*time.Second), &limit, &remaining, false)
	other := addPrincipal(t, f.admin.TenantID, "person", "Other admin", []string{"admin"})
	for _, route := range []string{"/api/agent-accounts", "/api/agent-accounts/capacity"} {
		status, raw := call(t, f.mod, &other, "", "GET", route, "")
		if status != 200 || strings.Contains(string(raw), "key_usage_usd") {
			t.Fatal("private budget disclosed")
		}
	}
	var events []byte
	if err := adminPool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(e),'[]'::jsonb)::text FROM events e WHERE tenant_id=$1`, f.admin.TenantID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	var budgetRaw []byte
	if err := adminPool.QueryRow(t.Context(), `SELECT usage_budget FROM agent_accounts WHERE id=$1`, f.account.ID).Scan(&budgetRaw); err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if json.Unmarshal(budgetRaw, &object) != nil || len(object) != 7 || strings.Contains(string(events), "synthetic-login-value") {
		t.Fatal("unallowlisted data persisted")
	}
}

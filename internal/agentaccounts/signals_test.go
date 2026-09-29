// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"strings"
	"testing"
)

func TestAccountSignalsOwnershipTenantKeyAndConsent(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "signals", "person", "Owner", []string{"admin"})
	one := addPrincipal(t, admin.TenantID, "agent", "one", nil)
	two := addPrincipal(t, admin.TenantID, "agent", "two", nil)
	foreign := makePrincipal(t, "foreign-signals", "agent", "foreign", nil)
	oneKey := issueKey(t, one, []string{"account.manage", "account.probe"})
	twoKey := issueKey(t, two, []string{"account.manage", "account.probe"})
	foreignKey := issueKey(t, foreign, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	var a, b, c Account
	for _, v := range []struct {
		who     int
		account *Account
	}{{0, &a}, {1, &b}, {2, &c}} {
		p, key := one, oneKey
		if v.who == 1 {
			p, key = two, twoKey
		}
		if v.who == 2 {
			p, key = foreign, foreignKey
		}
		callStatus(t, mod, &p, key, "POST", "/api/agent-accounts", `{"account_key":"local","harness":"claude","daemon_id":"fixture-daemon","label":"Claude"}`, 201, v.account)
	}
	path := "/api/agent-accounts/" + a.ID
	var first, second, other struct {
		Key []byte `json:"key"`
	}
	callStatus(t, mod, &one, oneKey, "POST", path+"/quota-key", "", 200, &first)
	callStatus(t, mod, &two, twoKey, "POST", "/api/agent-accounts/"+b.ID+"/quota-key", "", 200, &second)
	callStatus(t, mod, &foreign, foreignKey, "POST", "/api/agent-accounts/"+c.ID+"/quota-key", "", 200, &other)
	if len(first.Key) != 32 || !bytes.Equal(first.Key, second.Key) || bytes.Equal(first.Key, other.Key) {
		t.Fatal("tenant key stability/isolation failed")
	}
	callStatus(t, mod, &two, twoKey, "POST", path+"/quota-key", "", 403, nil)
	callStatus(t, mod, &admin, "", "POST", path+"/quota-key", "", 403, nil)
	callStatus(t, mod, &foreign, foreignKey, "POST", path+"/quota-key", "", 404, nil)
	body := encoded(t, accountSignals{"statusline", strings.Repeat("a", 64)})
	callStatus(t, mod, &two, twoKey, "PUT", path+"/signals", body, 403, nil)
	callStatus(t, mod, &one, oneKey, "PUT", path+"/signals", body, 204, nil)
	callStatus(t, mod, &one, oneKey, "PUT", path+"/signals", body, 204, nil)
	for _, bad := range []string{`{"reading_support":"none","quota_fingerprint":"fixture@example.test"}`, `{"reading_support":"none","quota_fingerprint":"","token":"fixture"}`} {
		callStatus(t, mod, &one, oneKey, "PUT", path+"/signals", bad, 400, nil)
	}
	var consent statuslineConsent
	callStatus(t, mod, &one, oneKey, "GET", path+"/statusline", "", 200, &consent)
	if consent.Enabled == nil || *consent.Enabled {
		t.Fatal("enrollment opted in")
	}
	callStatus(t, mod, &one, oneKey, "PUT", path+"/statusline", `{"enabled":true}`, 403, nil)
	callStatus(t, mod, &admin, "", "PUT", path+"/statusline", `{"enabled":true}`, 200, &consent)
	callStatus(t, mod, &one, oneKey, "GET", path+"/statusline", "", 200, &consent)
	if !*consent.Enabled {
		t.Fatal("explicit consent missing")
	}
	var accounts []Account
	callStatus(t, mod, &admin, "", "GET", "/api/agent-accounts", "", 200, &accounts)
	if accounts[0].QuotaFingerprint != strings.Repeat("a", 64) || !accounts[0].StatuslineEnabled {
		t.Fatal("account projection lost signal")
	}
	if scalar(t, foreign, `SELECT count(*) FROM tenant_quota_keys WHERE tenant_id<>$1`, foreign.TenantID) != 0 {
		t.Fatal("key RLS failed")
	}
}

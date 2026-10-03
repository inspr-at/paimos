// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
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
		callStatus(t, mod, &p, key, "POST", "/api/agent-accounts", fmt.Sprintf(`{"account_key":"local","harness":"claude","daemon_id":"fixture-daemon-%d","label":"Claude"}`, v.who), 201, v.account)
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
	var got Account
	for _, account := range accounts {
		if account.ID == a.ID {
			got = account
		}
	}
	// Workspace intervention permits consent management, not quota disclosure.
	if got.QuotaFingerprint != "" || !got.StatuslineEnabled || got.StatuslineOptIn != "workspace" {
		t.Fatal("account projection lost signal")
	}
	clerk := addPrincipal(t, admin.TenantID, "person", "clerk", nil)
	owner := addPrincipal(t, admin.TenantID, "person", "computer-owner", nil)
	grantAccountManage(t, clerk, owner)
	callStatus(t, mod, &clerk, "", "PUT", path+"/statusline", `{"enabled":false}`, 403, nil)
	pairClaudeAccount(t, owner, one, a)
	callStatus(t, mod, &owner, "", "PUT", path+"/statusline", `{"enabled":false}`, 200, &consent)
	if consent.Enabled == nil || *consent.Enabled {
		t.Fatal("owner opt-out lost")
	}
	callStatus(t, mod, &clerk, "", "PUT", path+"/statusline", `{"enabled":true}`, 403, nil)
	var owned, managed []Account
	callStatus(t, mod, &owner, "", "GET", "/api/agent-accounts", "", 200, &owned)
	callStatus(t, mod, &clerk, "", "GET", "/api/agent-accounts", "", 200, &managed)
	if audience := optIn(owned, a.ID); audience != "own" {
		t.Fatalf("owner audience %q", audience)
	}
	if audience := optIn(managed, a.ID); audience != "" {
		t.Fatalf("account.manage audience %q", audience)
	}
	if scalar(t, foreign, `SELECT count(*) FROM tenant_quota_keys WHERE tenant_id<>$1`, foreign.TenantID) != 0 {
		t.Fatal("key RLS failed")
	}
}

func optIn(items []Account, id string) string {
	for _, account := range items {
		if account.ID == id {
			return account.StatuslineOptIn
		}
	}
	return "missing"
}

func grantAccountManage(t *testing.T, people ...tenant.Principal) {
	t.Helper()
	if len(people) == 0 {
		return
	}
	tenantID := people[0].TenantID
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'account_clerk','Account clerk') RETURNING id::text`, tenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'account.manage'),($1::uuid,$2::uuid,'account.read')`, tenantID, role); err != nil {
			return err
		}
		for _, p := range people {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, tenantID, p.ID, role); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func pairClaudeAccount(t *testing.T, approver, agent tenant.Principal, account Account) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, approver.TenantID, func(tx pgx.Tx) error {
		var profile, key, request, computer string
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1::uuid,'claude-statusline','1','claude','anthropic','claude','medium','fast') RETURNING id::text`, approver.TenantID).Scan(&profile); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM agent_keys WHERE principal_id=$1::uuid`, agent.ID).Scan(&key); err != nil {
			return err
		}
		hash := strings.Repeat("ab", 32)
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1::uuid,gen_random_uuid(),'123456789',$2,$2,$2,'{}','fixture','redeemed',$3::uuid) RETURNING id::text`, approver.TenantID, hash, approver.ID).Scan(&request); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash) VALUES($1::uuid,gen_random_uuid(),$2::uuid,$3::uuid,$4::uuid,'paired-daemon',$5) RETURNING id::text`, approver.TenantID, request, agent.ID, key, hash).Scan(&computer); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,now()+interval '1 day')`, approver.TenantID, account.ID, computer, request, profile)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

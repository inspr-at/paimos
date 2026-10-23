// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/cli"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Included in the existing ESSENTIAL TestMessagingAssistantProvisioningBounds:
// risk: policy metadata replaces Amy's live principal, changes her authority,
// leaks across tenants, admits agent writes, or accepts a stale policy edit.
func testAssistantIdentityPolicy(t *testing.T) {
	m, owner, now := assistantFixture(t)
	expires := now.AddDate(0, 0, 7)
	amy, err := m.messagingAssistant(t.Context(), owner, "create", "", assistantInput{Name: "Amy", ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	policyModule := assistants.New(m.pool)
	server := &httpapi.Server{Pool: m.pool, Modules: []httpapi.Module{policyModule, authz.New(m.pool)}}
	productionAuth, err := Attach(server, Config{Env: "dev", SessionKey: bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	productionAuth.AssistantNoticeTx = func(context.Context, pgx.Tx, string, string, string, string) error { return nil }
	srv := httptest.NewServer(server.Handler())
	t.Cleanup(srv.Close)
	seed := func(tenantID string, fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, tenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	session := func(p tenant.Principal) string {
		t.Helper()
		var identity string
		seed(p.TenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject) VALUES('assistant-policy',$1) RETURNING id::text`, p.ID).Scan(&identity); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE principals SET identity_id=$2::uuid WHERE id=$1::uuid`, p.ID, identity)
			return err
		})
		cookie, err := productionAuth.startSession(t.Context(), identity, p.TenantID, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return cookie
	}
	ownerSession := session(owner)
	call := func(method, path, credential string, body any, want int) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(credential, "aeon_") {
			req.Header.Set("Authorization", "Bearer "+credential)
		} else if credential != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: credential})
		}
		req.Header.Set("Origin", srv.URL)
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
		if err != nil || len(data) > 65536 {
			t.Fatal("invalid bounded response")
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, resp.StatusCode, want)
		}
		if resp.StatusCode == 200 || resp.StatusCode == 201 {
			if resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("assistant response is cacheable")
			}
		}
		return data
	}
	decodeAssistant := func(raw []byte) assistants.Assistant {
		t.Helper()
		var out assistants.Assistant
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	path := "/api/assistants/" + amy.PrincipalID
	legacy := decodeAssistant(call("GET", path, ownerSession, nil, 200))
	if legacy.PrincipalID != amy.PrincipalID || legacy.Kind != "assistant" || legacy.PrincipalKind != "agent" || legacy.OwnerPersonID != owner.ID || legacy.Revision != 0 || !reflect.DeepEqual(legacy.Policy, assistants.DefaultPolicy()) {
		t.Fatal("legacy identity/default policy differs")
	}
	call("GET", path+"/policy", amy.Token, nil, 200)
	call("GET", path+"/policy", "", nil, 401)
	policy := assistants.DefaultPolicy()
	policy.WeeklyBudget = assistants.Budget{Amount: "12.500001", Unit: "EUR"}
	var project string
	seed(owner.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'ASSISTANT-1','Assistant project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, owner.TenantID).Scan(&project)
	})
	policy.AllowedProjects = []string{project}
	linked := decodeAssistant(call("POST", "/api/assistants", ownerSession, map[string]any{"principal_id": amy.PrincipalID, "display_name": "Amy", "vendor": "grok", "owner_person_id": owner.ID, "policy": policy}, 201))
	if linked.PrincipalID != amy.PrincipalID || linked.Vendor != "grok" || linked.Revision != 1 || !reflect.DeepEqual(linked.Policy, policy) {
		t.Fatal("link changed identity or policy")
	}
	call("GET", path+"/policy", amy.Token, nil, 200)
	// The real CLI reads Amy and her exact policy through this same production
	// HTTP/auth stack, using only a synthetic fixture session in its temp dir.
	t.Setenv("AEON_URL", srv.URL)
	t.Setenv("AEON_API_KEY", amy.Token)
	sessionFile := filepath.Join(t.TempDir(), "synthetic-person-session")
	if err := os.WriteFile(sessionFile, []byte(ownerSession), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list"}, {"show", amy.PrincipalID}, {"policy", "show", amy.PrincipalID}} {
		argv := append([]string{"aeon", "assistant"}, args...)
		argv = append(argv, "--session-cookie-file", sessionFile, "--json")
		var stdout, stderr bytes.Buffer
		if code := cli.Run(argv, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), amy.PrincipalID) || !strings.Contains(stdout.String(), "12.500001") {
			t.Fatalf("live assistant CLI %s failed (code %d)", args[0], code)
		}
	}
	var ownPolicy, ownErrors bytes.Buffer
	if code := cli.Run([]string{"aeon", "assistant", "policy", "show", amy.PrincipalID}, strings.NewReader(""), &ownPolicy, &ownErrors); code != 0 || !strings.Contains(ownPolicy.String(), "12.500001") {
		t.Fatalf("own-policy CLI failed (code %d)", code)
	}
	newBody := map[string]any{"display_name": "Helper", "vendor": "grok", "owner_person_id": owner.ID, "policy": policy}
	helper := decodeAssistant(call("POST", "/api/assistants", ownerSession, newBody, 201))
	call("POST", "/api/assistants", ownerSession, newBody, 409)
	// Existing inbox authority authenticates after linking; no extra grant/key.
	seed(owner.TenantID, func(tx pgx.Tx) error {
		var roles, keys int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM role_bindings WHERE principal_id=$1::uuid),
 (SELECT count(*) FROM agent_keys WHERE principal_id=$2::uuid)`, amy.PrincipalID, helper.PrincipalID).Scan(&roles, &keys); err != nil {
			return err
		}
		if roles != 0 || keys != 0 {
			t.Fatal("policy granted a role or created credentials")
		}
		return nil
	})
	for _, endpoint := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/assistants", nil}, {"GET", path, nil},
		{"POST", "/api/assistants", newBody}, {"PATCH", path, map[string]any{"expected_revision": 1, "display_name": "Bad"}},
		{"PUT", path + "/policy", map[string]any{"expected_revision": 1, "policy": policy}}, {"POST", path + "/disable", nil},
		{"GET", "/api/assistants/" + helper.PrincipalID + "/policy", nil}, {"GET", "/api/me", nil},
	} {
		call(endpoint.method, endpoint.path, amy.Token, endpoint.body, 403)
	}
	// A person-owned full-access/coordinator key is still an agent.
	fullKey, err := m.createAgentKey(tenant.WithPrincipal(t.Context(), owner), owner, "Full assistant editor", "", authz.CoordinatorKeyScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed(owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET full_access=true,scopes='{}' WHERE id=$1`, fullKey.ID)
		return err
	})
	call("POST", "/api/assistants", fullKey.Token, newBody, 403)
	call("PUT", path+"/policy", fullKey.Token, map[string]any{"expected_revision": 1, "policy": policy}, 403)
	seed(owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['inbox.send'] WHERE id=$1::uuid`, amy.ID)
		return err
	})
	call("GET", path+"/policy", amy.Token, nil, 403)
	seed(owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE id=$1::uuid`, amy.ID, authz.MessagingAssistantScopes())
		return err
	})
	policy.AllowedScopes = []string{"inbox.read"}
	updated := call("PUT", path+"/policy", ownerSession, map[string]any{"expected_revision": 1, "policy": policy}, 200)
	var snap assistants.Snapshot
	if json.Unmarshal(updated, &snap) != nil || snap.Revision != 2 || !reflect.DeepEqual(snap.Policy, policy) {
		t.Fatal("policy update differs")
	}
	call("PUT", path+"/policy", ownerSession, map[string]any{"expected_revision": 1, "policy": assistants.DefaultPolicy()}, 409)
	// Bounds and governance flags fail without changing revision.
	for _, bad := range []assistants.Policy{
		{AllowedProjects: []string{}, AllowedScopes: []string{"keys.manage"}, WeeklyBudget: policy.WeeklyBudget, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true},
		{AllowedProjects: []string{}, AllowedScopes: []string{"unknown.scope"}, WeeklyBudget: policy.WeeklyBudget, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true},
		{AllowedProjects: []string{}, AllowedScopes: []string{}, WeeklyBudget: policy.WeeklyBudget, OutwardActionRule: "automatic", NeverAskPersonForSecretsOrManualSteps: true},
		{AllowedProjects: []string{}, AllowedScopes: []string{}, WeeklyBudget: policy.WeeklyBudget, OutwardActionRule: "approval_required"},
		{AllowedProjects: []string{}, AllowedScopes: []string{}, WeeklyBudget: assistants.Budget{Amount: "1e99", Unit: "EUR"}, OutwardActionRule: "approval_required", NeverAskPersonForSecretsOrManualSteps: true},
	} {
		call("PUT", path+"/policy", ownerSession, map[string]any{"expected_revision": 2, "policy": bad}, 400)
	}
	call("PATCH", path, ownerSession, map[string]any{"expected_revision": 2, "owner_person_id": owner.ID}, 400)
	call("GET", "/api/assistants?limit=101", ownerSession, nil, 400)
	// Each page exposes truncation with a cursor and never repeats a row.
	type assistantPage struct {
		Assistants []assistants.Assistant `json:"assistants"`
		NextCursor string                 `json:"next_cursor"`
	}
	var page assistantPage
	if json.Unmarshal(call("GET", "/api/assistants?limit=1", ownerSession, nil, 200), &page) != nil || len(page.Assistants) != 1 || page.NextCursor == "" {
		t.Fatal("first page missing cursor")
	}
	first, cursor := page.Assistants[0].PrincipalID, page.NextCursor
	page = assistantPage{}
	if json.Unmarshal(call("GET", "/api/assistants?limit=1&after="+cursor, ownerSession, nil, 200), &page) != nil || len(page.Assistants) != 1 || page.Assistants[0].PrincipalID == first || page.NextCursor != "" {
		t.Fatal("keyset page repeated or hid truncation")
	}
	read, err := policyModule.ReadPolicy(tenant.WithPrincipal(t.Context(), owner), owner.TenantID, amy.PrincipalID)
	if err != nil || read.Revision != 2 || !reflect.DeepEqual(read.Policy, policy) {
		t.Fatal("PolicyReader differs from API")
	}
	policyCtx := tenant.WithPrincipal(t.Context(), owner)
	if err := db.InTenant(policyCtx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		current, err := policyModule.ReadPolicyTx(policyCtx, tx, owner.TenantID, amy.PrincipalID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, read) {
			t.Fatal("transactional PolicyReader differs from API")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A separate tenant has its own person, assistant and project. Real RLS,
	// not a forged principal TenantID, proves the cross-tenant negatives.
	other := tenant.Principal{Kind: tenant.Person}
	seed("00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('assistant-other','Other') RETURNING id::text`).Scan(&other.TenantID)
	})
	var otherProject string
	seed(other.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other',ARRAY['super_admin']) RETURNING id::text`, other.TenantID).Scan(&other.ID); err != nil {
			return err
		}
		if err := dbtest.BindLegacyTx(t.Context(), tx, other.TenantID, other.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'OTHER-1','Other' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, other.TenantID).Scan(&otherProject)
	})
	otherSession := session(other)
	call("GET", path, otherSession, nil, 404)
	call("PUT", path+"/policy", otherSession, map[string]any{"expected_revision": 2, "policy": policy}, 404)
	foreignPolicy := policy
	foreignPolicy.AllowedProjects = []string{otherProject}
	call("PUT", path+"/policy", ownerSession, map[string]any{"expected_revision": 2, "policy": foreignPolicy}, 400)
	if _, err := policyModule.ReadPolicy(tenant.WithPrincipal(t.Context(), other), other.TenantID, amy.PrincipalID); !errors.Is(err, assistants.ErrNotFound) {
		t.Fatal("PolicyReader crossed tenant")
	}
	seed(other.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM assistant_profiles WHERE principal_id=$1`, amy.PrincipalID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("profile RLS leaked another tenant")
		}
		return nil
	})
	// A person with only keys.manage and members.read cannot create or edit.
	limited := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person}
	seed(owner.TenantID, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Limited') RETURNING id::text`, owner.TenantID).Scan(&limited.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'assistant_limited','Limited') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'keys.manage'),($1,$2,'members.read')`, owner.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, limited.ID, role)
		return err
	})
	limitedSession := session(limited)
	call("POST", "/api/assistants", limitedSession, newBody, 403)
	call("PUT", path+"/policy", limitedSession, map[string]any{"expected_revision": 2, "policy": policy}, 403)
	// Possessing both permissions never transfers another person's assistant.
	seed(owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission)
 SELECT tenant_id,id,'agents.create' FROM roles WHERE key='assistant_limited'`)
		return err
	})
	call("PUT", path+"/policy", limitedSession, map[string]any{"expected_revision": 2, "policy": policy}, 403)
	call("POST", path+"/disable", limitedSession, nil, 403)
	call("POST", "/api/assistants", limitedSession, newBody, 403)
	// Conversely, agents.create alone cannot replace the keys.manage check.
	seed(owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE permission='keys.manage'
 AND role_id=(SELECT id FROM roles WHERE key='assistant_limited')`)
		return err
	})
	limitedBody := map[string]any{"display_name": "Limited helper", "vendor": "grok", "owner_person_id": limited.ID, "policy": assistants.DefaultPolicy()}
	call("POST", "/api/assistants", limitedSession, limitedBody, 403)
	renamed := decodeAssistant(call("PATCH", path, ownerSession, map[string]any{"expected_revision": 2, "display_name": "Amy hosted"}, 200))
	if renamed.DisplayName != "Amy hosted" || renamed.Revision != 3 || renamed.PrincipalID != amy.PrincipalID {
		t.Fatal("metadata update changed identity or lost revision")
	}
	call("PATCH", path, ownerSession, map[string]any{"expected_revision": 2, "display_name": "Stale Amy"}, 409)
	// Disable revokes the same pre-link key, with one audit and no resurrection.
	disabled := decodeAssistant(call("POST", path+"/disable", ownerSession, nil, 200))
	if disabled.Status != "deactivated" || disabled.PrincipalID != amy.PrincipalID {
		t.Fatal("disable changed identity or left it active")
	}
	call("GET", path+"/policy", amy.Token, nil, 401)
	again := decodeAssistant(call("POST", path+"/disable", ownerSession, nil, 200))
	if again.Revision != disabled.Revision {
		t.Fatal("repeated disable changed revision")
	}
	seed(owner.TenantID, func(tx pgx.Tx) error {
		var revoked bool
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM agent_keys WHERE id=$1`, amy.ID).Scan(&revoked); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='assistant.disabled' AND after->>'principal_id'=$1`, amy.PrincipalID).Scan(&count); err != nil {
			return err
		}
		if !revoked || count != 1 {
			return fmt.Errorf("disable audit/revocation differs")
		}
		return nil
	})
}

// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestOIDCTenantSelection(t *testing.T) {
	reset(t)
	insertTenant(t, "inspr", "INSPR")
	insertTenant(t, "augmentoring", "Augmentoring")
	issuer := startFakeOIDC(t, "aeon-public")
	if _, err := tenantbootstrap.BindOIDC(t.Context(), appPool, "augmentoring", issuer.issuer, "shared-subject", "Customer", "customer"); err != nil {
		t.Fatal(err)
	}
	mod := newMod(t, Config{Env: envDev, OIDCIssuer: issuer.issuer,
		OIDCClientID: "aeon-public", SessionKey: bytes.Repeat([]byte{9}, 32),
		BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	app := startApp(t, mod)
	mod.cfg.PublicURL = app.URL
	c := newHTTPClient()
	login, err := c.Get(app.URL + "/api/auth/login?tenant=augmentoring")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusFound {
		t.Fatalf("login %d", login.StatusCode)
	}
	q := assertAuthURL(t, app.URL, login.Header.Get("Location"))
	issuer.allow("selected", q.Get("code_challenge"), q.Get("nonce"), "shared-subject", "admin@example.com", "Customer", false)
	status, _, _ := do(t, c, http.MethodGet, callbackURL(app.URL, "selected", q.Get("state")), "", nil)
	if status != http.StatusFound {
		t.Fatalf("callback %d", status)
	}
	status, body, _ := do(t, c, http.MethodGet, app.URL+"/api/me", "", nil)
	if status != http.StatusOK {
		t.Fatalf("me %d %s", status, body)
	}
	var me meJSON
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if me.Tenant.Slug != "augmentoring" || len(me.Principal.Roles) != 1 || me.Principal.Roles[0] != "customer" {
		t.Fatalf("wrong tenant membership: %+v", me)
	}
	for _, path := range []string{"/api/nodes", "/api/events", "/api/search?q=customer", "/api/relations", "/api/imports"} {
		if status, _, _ := do(t, c, http.MethodGet, app.URL+path, "", nil); status != http.StatusForbidden {
			t.Fatalf("customer reached %s: %d", path, status)
		}
	}
	if status, _, _ := do(t, c, http.MethodGet, app.URL+"/api/quotes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", nil); status != http.StatusNotFound {
		t.Fatalf("customer quote path blocked before its contact guard: %d", status)
	}
	other := newHTTPClient()
	login, err = other.Get(app.URL + "/api/auth/login?tenant=inspr")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	q = assertAuthURL(t, app.URL, login.Header.Get("Location"))
	issuer.allow("unbound", q.Get("code_challenge"), q.Get("nonce"), "shared-subject", "customer@example.com", "Customer", false)
	status, _, response := do(t, other, http.MethodGet, callbackURL(app.URL, "unbound", q.Get("state")), "", nil)
	if status != http.StatusFound || response.Header.Get("Location") != "/signin?error=not_member" {
		t.Fatalf("cross-tenant sign-in %d", status)
	}
	if status, _, _ := do(t, other, http.MethodGet, app.URL+"/api/me", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("cross-tenant session %d", status)
	}
}

func TestOIDCSessionLifecycle(t *testing.T) {
	reset(t)
	insertTenant(t, "inspr", "INSPR")
	issuer := startFakeOIDC(t, "aeon-public")
	mod := newMod(t, Config{
		Env:                 envDev,
		OIDCIssuer:          issuer.issuer,
		OIDCClientID:        "aeon-public",
		SessionKey:          bytes.Repeat([]byte{5}, 32),
		BootstrapTenantSlug: "inspr",
		BootstrapAdminEmail: "admin@example.com",
	})
	app := startApp(t, mod)
	mod.cfg.PublicURL = app.URL
	c := newHTTPClient()

	status, body, _ := do(t, c, http.MethodGet, app.URL+"/api/me", "", nil)
	if status != http.StatusUnauthorized || !strings.Contains(string(body), `"unauthorized"`) {
		t.Fatalf("me %d %s", status, body)
	}
	if status, _, _ := do(t, c, http.MethodGet, app.URL+"/api/health", "", nil); status != http.StatusOK {
		t.Fatalf("health %d", status)
	}
	if status, _, _ := do(t, c, http.MethodGet, app.URL+"/api/version", "", nil); status != http.StatusOK {
		t.Fatalf("version %d", status)
	}
	if status, _, _ := do(t, c, http.MethodGet, app.URL+"/api/nope", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unknown api %d", status)
	}
	if status, _, _ := do(t, c, http.MethodGet, app.URL+"/", "", nil); status != http.StatusNotFound {
		t.Fatalf("non-api %d", status)
	}
	if status, _, _ := do(t, c, http.MethodPost, app.URL+"/api/auth/logout", "", nil); status != http.StatusNoContent {
		t.Fatalf("logout %d", status)
	}

	login, err := c.Get(app.URL + "/api/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusFound {
		t.Fatalf("login %d", login.StatusCode)
	}
	q := assertAuthURL(t, app.URL, login.Header.Get("Location"))

	issuer.allow("stranger", q.Get("code_challenge"), q.Get("nonce"), "sub-stranger", "stranger@example.com", "Stranger", false)
	status, body, response := do(t, c, http.MethodGet, callbackURL(app.URL, "stranger", q.Get("state")), "", nil)
	if status != http.StatusFound || response.Header.Get("Location") != "/signin?error=not_member" {
		t.Fatalf("stranger %d %s", status, body)
	}
	if n := scalar(t, adminPool, `SELECT count(*) FROM identities`); n != 0 {
		t.Fatalf("identities %d", n)
	}
	if n := scalar(t, adminPool, `SELECT count(*) FROM principals`); n != 0 {
		t.Fatalf("principals %d", n)
	}

	login, err = c.Get(app.URL + "/api/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	q = assertAuthURL(t, app.URL, login.Header.Get("Location"))
	status, _, response = do(t, c, http.MethodGet, callbackURL(app.URL, "nope", "wrong-state"), "", nil)
	if status != http.StatusFound || response.Header.Get("Location") != "/signin?error=expired" {
		t.Fatalf("bad state %d", status)
	}

	login, err = c.Get(app.URL + "/api/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	rawCookie := login.Header.Get("Set-Cookie")
	login.Body.Close()
	q = assertAuthURL(t, app.URL, login.Header.Get("Location"))
	req, err := http.NewRequest(http.MethodGet, callbackURL(app.URL, "tamper", q.Get("state")), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", tamperCookie(rawCookie))
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/signin?error=expired" {
		t.Fatalf("tamper %d", res.StatusCode)
	}

	login, err = c.Get(app.URL + "/api/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	q = assertAuthURL(t, app.URL, login.Header.Get("Location"))
	issuer.allow("badnonce", q.Get("code_challenge"), q.Get("nonce"), "sub-admin", "Admin@Example.com", "Ada", true)
	status, _, response = do(t, c, http.MethodGet, callbackURL(app.URL, "badnonce", q.Get("state")), "", nil)
	if status != http.StatusFound || response.Header.Get("Location") != "/signin?error=expired" {
		t.Fatalf("bad nonce %d", status)
	}

	login, err = c.Get(app.URL + "/api/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	q = assertAuthURL(t, app.URL, login.Header.Get("Location"))
	issuer.allow("admin", q.Get("code_challenge"), q.Get("nonce"), "sub-admin", "Admin@Example.com", "Ada", false)
	status, _, res = do(t, c, http.MethodGet, callbackURL(app.URL, "admin", q.Get("state")), "", nil)
	if status != http.StatusFound || res.Header.Get("Location") != app.URL+"/" {
		t.Fatalf("admin callback %d location %q", status, res.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookieName {
			session = ck
		}
	}
	if session == nil || !session.HttpOnly || session.Secure || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie %+v", session)
	}

	status, body, _ = do(t, c, http.MethodGet, app.URL+"/api/me", "", nil)
	if status != http.StatusOK {
		t.Fatalf("me %d %s", status, body)
	}
	var me meJSON
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if me.Tenant.Slug != "inspr" || me.Principal.Kind != "person" || me.Principal.Name != "Ada" || me.Identity == nil || me.Identity.Email == nil || *me.Identity.Email != "Admin@Example.com" {
		t.Fatalf("me %+v identity %+v", me, me.Identity)
	}
	if len(me.Principal.Roles) != 1 || me.Principal.Roles[0] != "admin" {
		t.Fatalf("roles %v", me.Principal.Roles)
	}
	if scalar(t, adminPool, `SELECT count(*) FROM principals WHERE kind='person'`) != 1 {
		t.Fatal("principal count")
	}
	if scalar(t, appPool, `SELECT count(*) FROM principals`) != 0 {
		t.Fatal("rls hid nothing")
	}

	login, err = c.Get(app.URL + "/api/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	q = assertAuthURL(t, app.URL, login.Header.Get("Location"))
	issuer.allow("admin2", q.Get("code_challenge"), q.Get("nonce"), "sub-admin", "Admin@Example.com", "Ada", false)
	status, _, _ = do(t, c, http.MethodGet, callbackURL(app.URL, "admin2", q.Get("state")), "", nil)
	if status != http.StatusFound {
		t.Fatalf("second login %d", status)
	}
	if scalar(t, adminPool, `SELECT count(*) FROM principals WHERE kind='person'`) != 1 {
		t.Fatal("second login created another principal")
	}

	if _, err := adminPool.Exec(t.Context(), `UPDATE sessions SET expires_at = now() + interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = do(t, c, http.MethodGet, app.URL+"/api/me", "", nil); status != http.StatusOK {
		t.Fatalf("rolling me %d", status)
	}
	var extended bool
	if err := adminPool.QueryRow(t.Context(), `
		SELECT expires_at > now() + interval '29 days' FROM sessions ORDER BY last_seen_at DESC LIMIT 1
	`).Scan(&extended); err != nil || !extended {
		t.Fatalf("session not rolled extended=%v err=%v", extended, err)
	}
	if status, _, _ = do(t, c, http.MethodGet, app.URL+"/api/missing", "", nil); status != http.StatusForbidden {
		t.Fatalf("authed unknown %d", status)
	}

	if _, err := adminPool.Exec(t.Context(), `UPDATE sessions SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	status, _, res = do(t, c, http.MethodGet, app.URL+"/api/me", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("expired %d", status)
	}
	cleared := false
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("expired session cookie was not cleared")
	}

	login, err = c.Get(app.URL + "/api/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	q = assertAuthURL(t, app.URL, login.Header.Get("Location"))
	issuer.allow("admin3", q.Get("code_challenge"), q.Get("nonce"), "sub-admin", "Admin@Example.com", "Ada", false)
	if status, _, _ = do(t, c, http.MethodGet, callbackURL(app.URL, "admin3", q.Get("state")), "", nil); status != http.StatusFound {
		t.Fatalf("relogin %d", status)
	}
	if status, _, _ = do(t, c, http.MethodPost, app.URL+"/api/auth/logout", "", nil); status != http.StatusNoContent {
		t.Fatalf("logout %d", status)
	}
	if status, _, _ = do(t, c, http.MethodGet, app.URL+"/api/me", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("after logout %d", status)
	}
	if scalar(t, adminPool, `SELECT count(*) FROM sessions WHERE expires_at > now()`) != 0 {
		t.Fatal("live session survived logout")
	}
	if issuer.sawSecret {
		t.Fatal("token request sent a client secret")
	}
}

func TestDevLoginAndAgentKeys(t *testing.T) {
	reset(t)
	mod := newMod(t, Config{
		Env:                 envDev,
		SessionKey:          bytes.Repeat([]byte{6}, 32),
		BootstrapTenantSlug: "inspr",
		BootstrapAdminEmail: "admin@example.com",
	})
	app := startApp(t, mod)
	c := newHTTPClient()

	status, body, _ := do(t, c, http.MethodPost, app.URL+"/api/auth/dev-login", `{"email":"admin@example.com"}`, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("missing tenant %d %s", status, body)
	}
	insertTenant(t, "inspr", "INSPR")

	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/auth/dev-login", `{"email":"nope"}`, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("bad email %d %s", status, body)
	}
	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/auth/dev-login", `{"email":"other@example.com"}`, nil)
	if status != http.StatusForbidden || !strings.Contains(string(body), notMemberSentence) {
		t.Fatalf("other %d %s", status, body)
	}

	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/auth/dev-login", `{"email":"admin@example.com"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("dev admin %d %s", status, body)
	}
	var me meJSON
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatal(err)
	}
	if me.Principal.Kind != string(tenant.Person) || !slices.Contains(me.Principal.Roles, "admin") {
		t.Fatalf("dev me %+v", me.Principal)
	}

	bare := newHTTPClient()
	if status, _, _ = do(t, bare, http.MethodPost, app.URL+"/api/agent-keys", `{"name":"ci"}`, nil); status != http.StatusUnauthorized {
		t.Fatalf("anon create %d", status)
	}

	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/agent-keys", `{"name":"ci","scopes":["events:read","account.manage"]}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	var created agentKeyCreatedJSON
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "aeon_"+created.Prefix+"_") || created.Token == "" {
		t.Fatalf("token %q prefix %q", created.Token, created.Prefix)
	}
	secret := strings.TrimPrefix(created.Token, "aeon_"+created.Prefix+"_")
	if secret == "" || strings.Contains(secret, "_") {
		t.Fatalf("secret shape")
	}
	compact := strings.ReplaceAll(me.Tenant.ID, "-", "")
	if !strings.HasPrefix(created.Prefix, compact) {
		t.Fatalf("prefix %q does not carry tenant %s", created.Prefix, compact)
	}

	authz := http.Header{"Authorization": {"Bearer " + created.Token}}
	status, body, _ = do(t, c, http.MethodGet, app.URL+"/api/me", "", authz)
	if status != http.StatusOK {
		t.Fatalf("bearer me %d %s", status, body)
	}
	var agentMe meJSON
	if err := json.Unmarshal(body, &agentMe); err != nil {
		t.Fatal(err)
	}
	if agentMe.Principal.Kind != "agent" || agentMe.Principal.Name != "ci" || agentMe.Identity != nil || agentMe.Principal.ID != created.PrincipalID {
		t.Fatalf("agent me %+v identity %v", agentMe.Principal, agentMe.Identity)
	}
	for _, path := range []string{"/api/nodes", "/api/relations", "/api/search?q=anything"} {
		if status, _, _ := do(t, c, http.MethodGet, app.URL+path, "", authz); status != http.StatusForbidden {
			t.Fatalf("events:read agent reached %s: %d", path, status)
		}
	}
	if status, _, _ := do(t, c, http.MethodGet, app.URL+"/api/events", "", authz); status != http.StatusNotFound {
		t.Fatalf("events:read agent denied its scoped path: %d", status)
	}
	if status, _, _ = do(t, c, http.MethodPost, app.URL+"/api/agent-keys", `{"name":"nope"}`, authz); status != http.StatusForbidden {
		t.Fatalf("agent create %d", status)
	}

	status, body, _ = do(t, c, http.MethodGet, app.URL+"/api/agent-keys", "", nil)
	if status != http.StatusOK || strings.Contains(string(body), secret) || strings.Contains(string(body), `"token"`) {
		t.Fatalf("list %d %s", status, body)
	}
	var list struct {
		Keys []agentKeyJSON `json:"keys"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Keys) != 1 || list.Keys[0].LastUsedAt == nil || len(list.Keys[0].Scopes) != 2 || list.Keys[0].Scopes[0] != "events.read" || list.Keys[0].Scopes[1] != "account.manage" {
		t.Fatalf("list %+v", list.Keys)
	}

	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/agent-keys", `{"name":"ci"}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("second %d %s", status, body)
	}
	var second agentKeyCreatedJSON
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	if second.PrincipalID != created.PrincipalID || second.Token == created.Token {
		t.Fatalf("reuse principal %s %s", second.PrincipalID, created.PrincipalID)
	}
	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/agent-keys", `{"name":"other"}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("other key %d %s", status, body)
	}
	var third agentKeyCreatedJSON
	if err := json.Unmarshal(body, &third); err != nil {
		t.Fatal(err)
	}
	if third.PrincipalID == created.PrincipalID {
		t.Fatal("different name reused the principal")
	}

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	status, _, _ = do(t, c, http.MethodPost, app.URL+"/api/agent-keys", `{"name":"old","expires_at":"`+past+`"}`, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("past expiry %d", status)
	}

	status, _, _ = do(t, c, http.MethodDelete, app.URL+"/api/agent-keys/"+created.ID, "", nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke %d", status)
	}
	if status, _, _ = do(t, bare, http.MethodGet, app.URL+"/api/me", "", authz); status != http.StatusUnauthorized {
		t.Fatalf("revoked bearer %d", status)
	}
	if status, _, _ = do(t, c, http.MethodGet, app.URL+"/api/me", "", authz); status != http.StatusUnauthorized {
		t.Fatalf("revoked bearer fell back to the session %d", status)
	}
	if status, _, _ = do(t, c, http.MethodGet, app.URL+"/api/me", "", nil); status != http.StatusOK {
		t.Fatalf("session after revoked bearer %d", status)
	}
	status, body, _ = do(t, c, http.MethodGet, app.URL+"/api/agent-keys", "", nil)
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	var revoked bool
	for _, k := range list.Keys {
		if k.ID == created.ID && k.RevokedAt != nil {
			revoked = true
		}
	}
	if !revoked {
		t.Fatalf("revoked key missing %s", body)
	}
	if status, _, _ = do(t, c, http.MethodDelete, app.URL+"/api/agent-keys/"+created.ID, "", nil); status != http.StatusNoContent {
		t.Fatalf("second revoke %d", status)
	}
	if status, _, _ = do(t, c, http.MethodDelete, app.URL+"/api/agent-keys/00000000-0000-0000-0000-000000000099", "", nil); status != http.StatusNotFound {
		t.Fatalf("missing key %d", status)
	}
	if status, _, _ = do(t, c, http.MethodDelete, app.URL+"/api/agent-keys/not-a-uuid", "", nil); status != http.StatusBadRequest {
		t.Fatalf("bad id %d", status)
	}

	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_keys SET expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, second.ID); err != nil {
		t.Fatal(err)
	}
	secondAuth := http.Header{"Authorization": {"Bearer " + second.Token}}
	if status, _, _ = do(t, bare, http.MethodGet, app.URL+"/api/me", "", secondAuth); status != http.StatusUnauthorized {
		t.Fatalf("expired key %d", status)
	}

	prod := newMod(t, Config{Env: "prod", SessionKey: bytes.Repeat([]byte{8}, 32), BootstrapTenantSlug: "inspr"})
	prodApp := startApp(t, prod)
	if status, _, _ = do(t, newHTTPClient(), http.MethodPost, prodApp.URL+"/api/auth/dev-login", `{"email":"admin@example.com"}`, nil); status != http.StatusNotFound {
		t.Fatalf("prod dev-login %d", status)
	}
}

func TestAgentKeyRLS(t *testing.T) {
	reset(t)
	tenantA := insertTenant(t, "inspr", "INSPR")
	tenantB := insertTenant(t, "other", "Other")
	mod := newMod(t, Config{
		Env:                 envDev,
		SessionKey:          bytes.Repeat([]byte{9}, 32),
		BootstrapTenantSlug: "inspr",
		BootstrapAdminEmail: "admin@example.com",
	})
	app := startApp(t, mod)
	c := newHTTPClient()
	status, body, _ := do(t, c, http.MethodPost, app.URL+"/api/auth/dev-login", `{"email":"admin@example.com"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("dev %d %s", status, body)
	}
	status, body, _ = do(t, c, http.MethodPost, app.URL+"/api/agent-keys", `{"name":"ci","scopes":["account.manage"]}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	var created agentKeyCreatedJSON
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	if scalar(t, appPool, `SELECT count(*) FROM agent_keys`) != 0 {
		t.Fatal("owner saw keys without a tenant")
	}
	if scalar(t, adminPool, `SELECT count(*) FROM agent_keys`) != 1 {
		t.Fatal("superuser count")
	}
	if countInTenant(t, tenantA, `SELECT count(*) FROM agent_keys`) != 1 {
		t.Fatal("tenant A count")
	}
	if countInTenant(t, tenantB, `SELECT count(*) FROM agent_keys`) != 0 {
		t.Fatal("tenant B saw tenant A keys")
	}

	var got string
	if err := testInTenant(t.Context(), appPool, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT current_setting('aeon.tenant_id', true)`).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got != tenantA {
		t.Fatalf("guc %q", got)
	}
	if scalar(t, appPool, `SELECT count(*) FROM agent_keys`) != 0 {
		t.Fatal("tenant setting leaked past the transaction")
	}

	var owner string
	var rls, force bool
	if err := adminPool.QueryRow(t.Context(), `
		SELECT r.rolname, c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
		WHERE c.relname = 'agent_keys'
	`).Scan(&owner, &rls, &force); err != nil {
		t.Fatal(err)
	}
	if owner != appRole || !rls || !force {
		t.Fatalf("owner %s rls %v force %v", owner, rls, force)
	}
	var using, check string
	if err := adminPool.QueryRow(t.Context(), `
		SELECT pg_get_expr(polqual, polrelid), pg_get_expr(polwithcheck, polrelid)
		FROM pg_policy WHERE polrelid = 'agent_keys'::regclass
	`).Scan(&using, &check); err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{using, check} {
		if !strings.Contains(expr, "aeon.tenant_id") || !strings.Contains(expr, "tenant_id") {
			t.Fatalf("policy %q", expr)
		}
	}
	var sessionRLS, sessionForced bool
	if err := adminPool.QueryRow(t.Context(), `SELECT relrowsecurity,relforcerowsecurity FROM pg_class WHERE relname = 'sessions'`).Scan(&sessionRLS, &sessionForced); err != nil || !sessionRLS || !sessionForced {
		t.Fatalf("sessions rls=%v force=%v err=%v", sessionRLS, sessionForced, err)
	}
	if count := scalar(t, appPool, `SELECT count(*) FROM sessions`); count != 0 {
		t.Fatalf("unscoped app role saw %d sessions", count)
	}

	secret := strings.TrimPrefix(created.Token, "aeon_"+created.Prefix+"_")
	swapped := "aeon_" + strings.ReplaceAll(tenantB, "-", "") + created.Prefix[32:] + "_" + secret
	bare := newHTTPClient()
	if status, _, _ = do(t, bare, http.MethodGet, app.URL+"/api/me", "", http.Header{"Authorization": {"Bearer " + swapped}}); status != http.StatusUnauthorized {
		t.Fatalf("swapped tenant bearer %d", status)
	}
	if status, _, _ = do(t, bare, http.MethodGet, app.URL+"/api/me", "", http.Header{"Authorization": {"Bearer " + created.Token}}); status != http.StatusOK {
		t.Fatalf("original bearer %d", status)
	}

	other, err := mod.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantB, Roles: []string{"admin"}}, "b", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == "" || countInTenant(t, tenantB, `SELECT count(*) FROM agent_keys`) != 1 {
		t.Fatal("tenant B key")
	}
	if countInTenant(t, tenantA, `SELECT count(*) FROM agent_keys`) != 1 {
		t.Fatal("tenant A gained a key")
	}
	status, body, _ = do(t, c, http.MethodGet, app.URL+"/api/agent-keys", "", nil)
	if status != http.StatusOK || strings.Contains(string(body), other.Prefix) {
		t.Fatalf("list leaked %d %s", status, body)
	}

	err = testInTenant(t.Context(), appPool, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO agent_keys (tenant_id, principal_id, name, prefix, hash)
			VALUES ($1::uuid, $2::uuid, 'x', 'cross-prefix-should-fail', 'hash')
		`, tenantB, created.PrincipalID)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant insert succeeded")
	}
}

func countInTenant(t *testing.T, tenantID, query string) int {
	t.Helper()
	var n int
	err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), query).Scan(&n)
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func newMod(t *testing.T, cfg Config) *Module {
	t.Helper()
	useDB(t)
	if len(cfg.SessionKey) < 32 {
		cfg.SessionKey = bytes.Repeat([]byte{7}, 32)
	}
	m, err := New(cfg, appPool)
	if err != nil {
		t.Fatal(err)
	}
	m.inTenant = testInTenant
	return m
}

func startApp(t *testing.T, m *Module) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	m.Mount(mux)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","db":"ok"}`)
	})
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"dev","scheme":"inspr-calendar-v2"}`)
	})
	mux.HandleFunc("GET /api/events", http.NotFound)
	mux.HandleFunc("GET /api/quotes/{quoteId}", http.NotFound)
	secured := m.Middleware(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, r.Pattern = mux.Handler(r)
		secured.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newHTTPClient() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func do(t *testing.T, c *http.Client, method, url, body string, hdr http.Header) (int, []byte, *http.Response) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf, res
}

func assertAuthURL(t *testing.T, appURL, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "aeon-public" {
		t.Fatalf("auth params %v", q)
	}
	if q.Get("redirect_uri") != appURL+"/api/auth/callback" {
		t.Fatalf("redirect %q", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("pkce %v", q)
	}
	if q.Get("client_secret") != "" {
		t.Fatal("client secret in the authorize URL")
	}
	for _, s := range []string{"openid", "profile", "email"} {
		if !strings.Contains(q.Get("scope"), s) {
			t.Fatalf("scope %q", q.Get("scope"))
		}
	}
	return q
}

func callbackURL(app, code, state string) string {
	return app + "/api/auth/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
}

func tamperCookie(raw string) string {
	parts := strings.Split(raw, ";")
	if len(parts) == 0 {
		return raw
	}
	nv := parts[0]
	name, value, ok := strings.Cut(nv, "=")
	if !ok || len(value) < 8 {
		return raw
	}
	b := []byte(value)
	b[4] = 'A'
	if b[4] == value[4] {
		b[4] = 'B'
	}
	parts[0] = name + "=" + string(b)
	return strings.Join(parts, ";")
}

func TestReadinessProbeIsPublic(t *testing.T) {
	if !isPublicAPI("/api/ready") || isProtectedAPI("/api/ready") {
		t.Fatal("GET /api/ready must be public")
	}
	if !isPublicAPI("/api/health") || isProtectedAPI("/api/health") {
		t.Fatal("GET /api/health must stay public")
	}
}

func scalar(t *testing.T, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

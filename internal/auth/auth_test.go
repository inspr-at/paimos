// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestNewRejectsShortKey(t *testing.T) {
	_, err := New(Config{SessionKey: []byte("short")}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewDefaults(t *testing.T) {
	m, err := New(Config{SessionKey: bytes.Repeat([]byte{1}, 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.cfg.BootstrapTenantSlug != "inspr" {
		t.Fatalf("slug %q", m.cfg.BootstrapTenantSlug)
	}
}

func TestAgentRoleCannotManageKeys(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/agent-keys", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{Kind: tenant.Agent, Roles: []string{"admin"}}))
	rec := httptest.NewRecorder()
	_, ok := (&Module{}).requireKeyManagement(rec, req)
	if ok || rec.Code != http.StatusForbidden {
		t.Fatalf("agent with admin role managed keys: %d", rec.Code)
	}
}

func TestCustomerRoutesStayBoundToOwnProfileAndQuotes(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	p := tenant.Principal{ID: id, Kind: tenant.Person, Roles: []string{"customer"}}
	for _, route := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/api/me/greeting", true},
		{http.MethodGet, "/api/people/" + id + "/avatar/small", true},
		{http.MethodGet, "/api/people/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/avatar/small", false},
		{http.MethodGet, "/api/quotes/" + id, true},
		{http.MethodPost, "/api/quotes/" + id + "/versions/1/accept", true},
		{http.MethodGet, "/api/quotes/" + id + "/versions/1/export", true},
		{http.MethodGet, "/api/quotes", false},
		{http.MethodGet, "/api/quotes/" + id + "/draft", false},
		{http.MethodGet, "/api/quotes/" + id + "/versions/1/public-link", false},
		{http.MethodGet, "/api/business", false},
	} {
		got := customerRouteAllowed(httptest.NewRequest(route.method, route.path, nil), p)
		if got != route.allowed {
			t.Errorf("%s %s: allowed=%t", route.method, route.path, got)
		}
	}
}

func TestOnlyPublicQuoteCapabilityPathsBypassAuthentication(t *testing.T) {
	m, err := New(Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{
		"/api/public/quotes/tenant/token", "/api/public/quotes/tenant/token/accept", "/api/public/quotes/tenant/token/pdf",
		"/api/public/portal/harbour", "/api/public/portal/harbour/wishes/PWS-1/votes",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Errorf("public path %s: %d", path, rec.Code)
		}
	}
	for _, path := range []string{
		"/api/quotes", "/api/quotes/id/versions/1/public-link", "/api/quotes/id/versions/1/public-link/revoke",
		"/api/public/quotes", "/api/public/quotesx/tenant/token", "/api/public/quotes-other/tenant/token",
		"/api/public/portal", "/api/portal/settings", "/api/public/portals/harbour",
		"/api/public/quote/tenant/token", "/api/me", "/api/events", "/api/plugins",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("protected path %s: %d", path, rec.Code)
		}
	}
}

func TestAttach(t *testing.T) {
	srv := &httpapi.Server{}
	m, err := Attach(srv, Config{SessionKey: bytes.Repeat([]byte{2}, 32), Env: envDev})
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.Modules) != 1 || len(srv.Middleware) != 1 {
		t.Fatalf("modules %d middleware %d", len(srv.Modules), len(srv.Middleware))
	}
	if srv.Modules[0] != m {
		t.Fatal("module not registered")
	}
}

func TestSessionCookieFlags(t *testing.T) {
	prod, err := New(Config{SessionKey: bytes.Repeat([]byte{3}, 32)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	prod.setSessionCookie(rec, "abc")
	c := rec.Result().Cookies()[0]
	if c.Name != "aeon_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != int(sessionTTL.Seconds()) || c.Path != "/" {
		t.Fatalf("prod cookie %+v", c)
	}
	dev, err := New(Config{SessionKey: bytes.Repeat([]byte{3}, 32), Env: envDev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	dev.setSessionCookie(rec, "abc")
	if rec.Result().Cookies()[0].Secure {
		t.Fatal("dev cookie must not be Secure")
	}
}

func TestOIDCCookieSeal(t *testing.T) {
	m, err := New(Config{SessionKey: bytes.Repeat([]byte{4}, 32), Env: envDev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	payload := oidcPayload{State: "st", Nonce: "no", Verifier: "ve", Tenant: "inspr", Exp: time.Now().Add(time.Minute).Unix()}
	if err := m.setOIDCCookie(rec, payload); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	got, err := m.readOIDCCookie(req)
	if err != nil || got.State != "st" || got.Nonce != "no" || got.Verifier != "ve" {
		t.Fatalf("open %+v %v", got, err)
	}
	tampered := *rec.Result().Cookies()[0]
	tampered.Value = tampered.Value[:len(tampered.Value)-2] + "aa"
	req = httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil)
	req.AddCookie(&tampered)
	if _, err := m.readOIDCCookie(req); err == nil {
		t.Fatal("tampered cookie accepted")
	}
	expired := oidcPayload{State: "st", Nonce: "no", Verifier: "ve", Tenant: "inspr", Exp: time.Now().Add(-time.Minute).Unix()}
	raw, err := m.seal(expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.open(raw); err == nil {
		t.Fatal("expired cookie accepted")
	}
}

func TestPrefixCarriesTenant(t *testing.T) {
	id := "123e4567-e89b-12d3-a456-426614174000"
	prefix, err := prefixFor(id)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := tenantFromPrefix(prefix)
	if !ok || got != id {
		t.Fatalf("tenant %q ok %v", got, ok)
	}
	if _, ok := tenantFromPrefix("abcd"); ok {
		t.Fatal("short prefix accepted")
	}
}

func TestParseBearer(t *testing.T) {
	p, s, ok := parseBearer("Bearer aeon_abc_def")
	if !ok || p != "abc" || s != "def" {
		t.Fatalf("%q %q %v", p, s, ok)
	}
	if _, _, ok := parseBearer("Basic aeon_abc_def"); ok {
		t.Fatal("basic accepted")
	}
	if _, _, ok := parseBearer("Bearer nope"); ok {
		t.Fatal("non-aeon accepted")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(envAppEnv, envDev)
	t.Setenv(envSessionKeyFile, "")
	t.Setenv(envTenantSlug, "")
	t.Setenv(envPublicURL, "https://aeon.example/")
	t.Setenv(envOIDCIssuer, "https://auth.inspr.at/")
	t.Setenv(envOIDCClientID, "public")
	t.Setenv(envAdminEmail, "admin@example.com")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SessionKey) != 32 || !cfg.Dev() || cfg.BootstrapTenantSlug != "inspr" {
		t.Fatalf("cfg env %q slug %q key %d", cfg.Env, cfg.BootstrapTenantSlug, len(cfg.SessionKey))
	}
	if cfg.PublicURL != "https://aeon.example" || cfg.OIDCIssuer != "https://auth.inspr.at" {
		t.Fatalf("urls %q %q", cfg.PublicURL, cfg.OIDCIssuer)
	}

	t.Setenv(envAppEnv, "prod")
	t.Setenv(envSessionKeyFile, "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("prod without key file")
	}

	dir := t.TempDir()
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionKeyFile, short)
	if _, err := FromEnv(); err == nil {
		t.Fatal("short key accepted")
	}
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, bytes.Repeat([]byte{9}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionKeyFile, good)
	t.Setenv(envTenantSlug, "studio")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SessionKey) != 32 || cfg.Dev() || cfg.BootstrapTenantSlug != "studio" {
		t.Fatalf("prod cfg env %q slug %q key %d", cfg.Env, cfg.BootstrapTenantSlug, len(cfg.SessionKey))
	}
	for _, value := range []string{"", "test", "DEV"} {
		t.Setenv(envAppEnv, value)
		cfg, err = FromEnv()
		if err != nil || cfg.Dev() {
			t.Fatalf("non-dev environment %q enabled dev mode: %v", value, err)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/portal"
)

func TestPublicRoutesDoNotEmitSessionCookies(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "harbour", "Harbour")
	pid, iid := signinPerson(t, tid, "ada", "Ada", "ada@example.com", "ada@example.com", "admin")
	mod := newMod(t, Config{Env: envDev})
	token, err := mod.startSession(t.Context(), iid, tid, pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := testInTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO portal_settings(tenant_id, enabled) VALUES ($1::uuid, true)`, tid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	portalMod := portal.New(appPool, false, bytes.Repeat([]byte{7}, 32))
	page := []byte("<!doctype html><title>SPA</title><p>SPA-MARKER</p>")
	handler := (&httpapi.Server{
		Modules:    []httpapi.Module{mod, portalMod},
		Middleware: []func(http.Handler) http.Handler{mod.Middleware},
		Web:        fstest.MapFS{"index.html": &fstest.MapFile{Data: page}},
	}).Handler()

	call := func(method, path, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "203.0.113.80:1800"
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	assertPublic := func(method, path, cache string) {
		t.Helper()
		anon := call(method, path, "")
		signed := call(method, path, token)
		if anon.Code != http.StatusOK || signed.Code != anon.Code || anon.Body.String() != signed.Body.String() {
			t.Fatalf("%s %s varied: anon %d signed %d", method, path, anon.Code, signed.Code)
		}
		if anon.Header().Get("Cache-Control") != cache || signed.Header().Get("Cache-Control") != cache {
			t.Fatalf("%s %s cache anon %q signed %q", method, path, anon.Header().Get("Cache-Control"), signed.Header().Get("Cache-Control"))
		}
		if anon.Header().Get("Set-Cookie") != "" || signed.Header().Get("Set-Cookie") != "" {
			t.Fatalf("%s %s emitted a cookie", method, path)
		}
	}
	const cached = "public, max-age=60"
	for _, route := range []struct{ method, path, cache string }{
		{http.MethodGet, "/api/public/portal/harbour/roadmap", cached},
		{http.MethodHead, "/api/public/portal/harbour/roadmap", cached},
		{http.MethodGet, "/api/public/portal/harbour/roadmap.json", cached},
		{http.MethodHead, "/api/public/portal/harbour/roadmap.json", cached},
		{http.MethodGet, "/portal/harbour/roadmap.json", cached},
		{http.MethodGet, "/portal/harbour/roadmap", "no-cache"},
		{http.MethodGet, "/api/public/portal/harbour", "no-store"},
		{http.MethodGet, "/api/public/portal/harbour/catalog.json", "no-store"},
		{http.MethodGet, "/portal/harbour/catalog.json", "no-store"},
		{http.MethodGet, "/api/public/portal/harbour/llms.txt", "no-store"},
		{http.MethodGet, "/portal/harbour/llms.txt", "no-store"},
		{http.MethodGet, "/api/public/portal/harbour/releases", "no-store"},
	} {
		assertPublic(route.method, route.path, route.cache)
	}
	if !strings.Contains(call(http.MethodGet, "/portal/harbour/roadmap", "").Body.String(), "SPA-MARKER") {
		t.Fatal("roadmap page left the SPA")
	}
	stale := call(http.MethodGet, "/api/public/portal/harbour/roadmap", "not-a-session")
	if stale.Code != http.StatusOK || stale.Header().Get("Cache-Control") != cached || stale.Header().Get("Set-Cookie") != "" {
		t.Fatalf("stale public: %d cache %q set %v", stale.Code, stale.Header().Get("Cache-Control"), stale.Header().Get("Set-Cookie") != "")
	}
	me := call(http.MethodGet, "/api/me", token)
	if me.Code == http.StatusUnauthorized || !strings.Contains(me.Header().Get("Set-Cookie"), sessionCookieName+"=") {
		t.Fatalf("protected refresh: %d set %v", me.Code, strings.Contains(me.Header().Get("Set-Cookie"), sessionCookieName+"="))
	}
	expired := call(http.MethodGet, "/api/me", "not-a-session")
	cleared := false
	for _, ck := range expired.Result().Cookies() {
		if ck.Name == sessionCookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if expired.Code != http.StatusUnauthorized || !cleared {
		t.Fatalf("stale protected: %d cleared %v", expired.Code, cleared)
	}
}

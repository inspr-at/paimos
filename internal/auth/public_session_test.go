// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/portal"
)

func TestPublicRoutesDoNotEmitSessionCookies(t *testing.T) {
	handler, _, token := newPublicEdge(t)
	call := func(method, path, cookie string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = nextPublicTestIP()
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	noCookie := func(method, path string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
			t.Fatalf("%s %s set %v", method, path, got)
		}
	}
	assertPublic := func(method, path, cache string) *httptest.ResponseRecorder {
		t.Helper()
		anon := call(method, path, "")
		signed := call(method, path, token)
		if anon.Code != http.StatusOK || signed.Code != anon.Code || anon.Body.String() != signed.Body.String() {
			t.Fatalf("%s %s varied: anon %d signed %d", method, path, anon.Code, signed.Code)
		}
		if anon.Header().Get("Cache-Control") != cache || signed.Header().Get("Cache-Control") != cache {
			t.Fatalf("%s %s cache anon %q signed %q", method, path, anon.Header().Get("Cache-Control"), signed.Header().Get("Cache-Control"))
		}
		noCookie(method, path, anon)
		noCookie(method, path, signed)
		return anon
	}
	const cached = "public, max-age=60"
	for _, route := range []struct{ method, path, cache string }{
		{http.MethodGet, "/api/public/portal/harbour/roadmap", cached},
		{http.MethodHead, "/api/public/portal/harbour/roadmap", cached},
		{http.MethodGet, "/api/public/portal/harbour/roadmap.json", cached},
		{http.MethodHead, "/api/public/portal/harbour/roadmap.json", cached},
		{http.MethodGet, "/portal/harbour/roadmap.json", cached},
		{http.MethodHead, "/portal/harbour/roadmap.json", cached},
		{http.MethodGet, "/portal/harbour/roadmap", "no-cache"},
		{http.MethodHead, "/portal/harbour/roadmap", "no-cache"},
		{http.MethodGet, "/portal/harbour", "no-cache"},
		{http.MethodHead, "/portal/harbour", "no-cache"},
		{http.MethodGet, "/api/public/portal/harbour", "no-store"},
		{http.MethodHead, "/api/public/portal/harbour", "no-store"},
		{http.MethodGet, "/api/public/portal/harbour/catalog.json", "no-store"},
		{http.MethodHead, "/api/public/portal/harbour/catalog.json", "no-store"},
		{http.MethodGet, "/portal/harbour/catalog.json", "no-store"},
		{http.MethodHead, "/portal/harbour/catalog.json", "no-store"},
		{http.MethodGet, "/api/public/portal/harbour/llms.txt", "no-store"},
		{http.MethodHead, "/api/public/portal/harbour/llms.txt", "no-store"},
		{http.MethodGet, "/portal/harbour/llms.txt", "no-store"},
		{http.MethodHead, "/portal/harbour/llms.txt", "no-store"},
		{http.MethodGet, "/api/public/portal/harbour/releases", "no-store"},
		{http.MethodHead, "/api/public/portal/harbour/releases", "no-store"},
	} {
		canonical := assertPublic(route.method, route.path, route.cache)
		servedEncoded := false
		for _, spelling := range publicSpellings(route.path) {
			probe := httptest.NewRequest(route.method, spelling, nil)
			_, pat := policyMux.Handler(probe)
			if !authz.PatternIsPublic(pat) {
				continue
			}
			anon := call(route.method, spelling, "")
			signed := call(route.method, spelling, token)
			stale := call(route.method, spelling, "not-a-session")
			for _, rec := range []*httptest.ResponseRecorder{anon, signed, stale} {
				noCookie(route.method, spelling, rec)
			}
			if anon.Code == http.StatusUnauthorized || signed.Code == http.StatusUnauthorized || stale.Code == http.StatusUnauthorized {
				t.Fatalf("%s %s challenged: anon %d signed %d stale %d", route.method, spelling, anon.Code, signed.Code, stale.Code)
			}
			if anon.Code != signed.Code || anon.Code != stale.Code || anon.Body.String() != signed.Body.String() || anon.Body.String() != stale.Body.String() {
				t.Fatalf("%s %s varied: anon %d signed %d stale %d", route.method, spelling, anon.Code, signed.Code, stale.Code)
			}
			unescaped, err := url.PathUnescape(spelling)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(spelling, "%") && unescaped == route.path {
				if anon.Code != http.StatusOK {
					t.Fatalf("%s %s encoded segment: %d", route.method, spelling, anon.Code)
				}
				if anon.Header().Get("Cache-Control") != route.cache || signed.Header().Get("Cache-Control") != route.cache || stale.Header().Get("Cache-Control") != route.cache {
					t.Fatalf("%s %s cache %q", route.method, spelling, anon.Header().Get("Cache-Control"))
				}
				if anon.Body.String() != canonical.Body.String() {
					t.Fatalf("%s %s body diverged from %s", route.method, spelling, route.path)
				}
				servedEncoded = true
			}
		}
		if !servedEncoded {
			t.Fatalf("%s %s: no encoded spelling reached the public handler", route.method, route.path)
		}
	}
	if !strings.Contains(call(http.MethodGet, "/portal/harbour/roadmap", "").Body.String(), "SPA-MARKER") {
		t.Fatal("roadmap page left the SPA")
	}
	stale := call(http.MethodGet, "/api/public/portal/harbour/roadmap", "not-a-session")
	if stale.Code != http.StatusOK || stale.Header().Get("Cache-Control") != cached || len(stale.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("stale public: %d cache %q", stale.Code, stale.Header().Get("Cache-Control"))
	}
	encodedStale := call(http.MethodGet, "/api/public/portal/harbour/%72oadmap", "not-a-session")
	if encodedStale.Code != http.StatusOK || encodedStale.Header().Get("Cache-Control") != cached || len(encodedStale.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("encoded stale: %d cache %q", encodedStale.Code, encodedStale.Header().Get("Cache-Control"))
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

// Every public declaration is exempt, including spellings the router accepts
// and routes this server does not mount. A URL check would set a cookie here.
func TestDeclaredPublicRoutesSkipSessionOnEverySpelling(t *testing.T) {
	_, mod, token := newPublicEdge(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, r.Pattern = policyMux.Handler(r)
		mod.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(w, r)
	})
	checked := 0
	for pattern, declaration := range authz.RoutePermissions {
		if declaration != authz.PublicRoute {
			continue
		}
		method, path := concretePublicPath(pattern)
		probe := httptest.NewRequest(method, path, nil)
		if _, pat := policyMux.Handler(probe); pat != pattern {
			t.Fatalf("concrete %s %s matched %q", method, path, pat)
		}
		methods := []string{method}
		if method == http.MethodGet {
			methods = append(methods, http.MethodHead)
		}
		sawEncoded := false
		for _, requestMethod := range methods {
			for _, spelling := range append([]string{path}, publicSpellings(path)...) {
				probe = httptest.NewRequest(requestMethod, spelling, nil)
				_, pat := policyMux.Handler(probe)
				if pat != pattern {
					continue
				}
				unescaped, err := url.PathUnescape(spelling)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(spelling, "%") && unescaped == path {
					sawEncoded = true
				}
				for _, cookie := range []string{"", token, "not-a-session"} {
					req := httptest.NewRequest(requestMethod, spelling, nil)
					if cookie != "" {
						req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
					}
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					if rec.Code == http.StatusUnauthorized {
						t.Fatalf("%s %s %s unauthorized", pattern, requestMethod, spelling)
					}
					if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
						t.Fatalf("%s %s %s set %v", pattern, requestMethod, spelling, got)
					}
				}
				checked++
			}
		}
		if !sawEncoded {
			t.Fatalf("%s: router accepted no encoded spelling", pattern)
		}
	}
	if checked == 0 {
		t.Fatal("no public declarations")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookieName+"=") {
		t.Fatalf("protected refresh missing, status %d", rec.Code)
	}
}

func newPublicEdge(t *testing.T) (http.Handler, *Module, string) {
	t.Helper()
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
	return handler, mod, token
}

var publicTestIP int

func nextPublicTestIP() string {
	publicTestIP++
	n := publicTestIP
	return fmt.Sprintf("10.%d.%d.%d:1800", (n>>16)&0xff, (n>>8)&0xff, n&0xff)
}

func concretePublicPath(pattern string) (string, string) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return http.MethodGet, pattern
	}
	path = routeValue.ReplaceAllStringFunc(path, func(name string) string {
		switch name {
		case "{tenantSlug}", "{publicTenant}":
			return "harbour"
		case "{wishKey}":
			return "PWS-1"
		case "{token}":
			return "public-token"
		default:
			return "sample"
		}
	})
	return method, path
}

func publicSpellings(path string) []string {
	slash := strings.LastIndex(path, "/")
	if slash < 0 {
		return nil
	}
	parent, seg := path[:slash], path[slash+1:]
	var out []string
	if enc := encodePathByte(seg); enc != seg {
		out = append(out, parent+"/"+enc)
	}
	if prev := strings.LastIndex(parent, "/"); prev > 0 {
		pseg := parent[prev+1:]
		if enc := encodePathByte(pseg); enc != pseg {
			out = append(out, parent[:prev]+"/"+enc+"/"+seg)
		}
	}
	out = append(out, parent+"//"+seg, parent+"/./"+seg, path+"/")
	if strings.Contains(path, "/harbour/") {
		out = append(out, strings.Replace(path, "/harbour/", "/%2e%2e/", 1))
	} else if strings.HasSuffix(path, "/harbour") {
		out = append(out, strings.TrimSuffix(path, "/harbour")+"/%2e%2e")
	}
	seen := map[string]bool{}
	unique := make([]string, 0, len(out))
	for _, spelling := range out {
		if spelling == "" || spelling == path || seen[spelling] {
			continue
		}
		seen[spelling] = true
		unique = append(unique, spelling)
	}
	return unique
}

func encodePathByte(segment string) string {
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return segment[:i] + fmt.Sprintf("%%%02X", c) + segment[i+1:]
		}
	}
	return segment
}

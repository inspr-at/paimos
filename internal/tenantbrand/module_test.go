// SPDX-License-Identifier: AGPL-3.0-only

package tenantbrand_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbrand"
)

func uid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type fixture struct {
	db                              *dbtest.DB
	mux                             *http.ServeMux
	admin, member, agent, foreigner tenant.Principal
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{db: dbtest.Open(t), mux: http.NewServeMux()}
	f.admin = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	f.member = tenant.Principal{ID: uid(), TenantID: f.admin.TenantID, Kind: tenant.Person}
	f.agent = tenant.Principal{ID: uid(), TenantID: f.admin.TenantID, Kind: tenant.Agent, Scopes: []string{"settings.manage"}}
	f.foreigner = tenant.Principal{ID: uid(), TenantID: uid(), Kind: tenant.Person}
	for _, id := range []string{f.admin.TenantID, f.foreigner.TenantID} {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.Admin, id, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,$2,'Brand Test')`, id, "b-"+id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []tenant.Principal{f.admin, f.member, f.agent, f.foreigner} {
		f.tx(t, p, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,$3,'brand')`, p.TenantID, p.ID, p.Kind)
			return err
		})
	}
	dbtest.BindRole(t, f.db, f.admin.TenantID, f.admin.ID, "admin")
	dbtest.BindRole(t, f.db, f.admin.TenantID, f.agent.ID, "admin")
	dbtest.BindRole(t, f.db, f.admin.TenantID, f.member.ID, "member")
	dbtest.BindRole(t, f.db, f.foreigner.TenantID, f.foreigner.ID, "admin")
	tenantbrand.New(f.db.App).Mount(f.mux)
	return f
}

func (f *fixture) tx(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) call(p tenant.Principal, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(tenant.WithPrincipal(context.Background(), p))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}

func settingsOf(t *testing.T, w *httptest.ResponseRecorder) tenantbrand.Settings {
	t.Helper()
	var s tenantbrand.Settings
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return s
}

func logoPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func name(v string) []byte { b, _ := json.Marshal(map[string]string{"short_name": v}); return b }

func TestBrandSettingsArePersonOnlyAudited(t *testing.T) {
	f := setup(t)

	// Nothing set: an empty form, and no brand in the session payload.
	s := settingsOf(t, f.call(f.admin, "GET", "/api/settings/brand", "", nil))
	if s.ShortName != "" || s.Logo != nil || s.LogoDark != nil {
		t.Fatalf("fresh brand %+v", s)
	}
	f.tx(t, f.admin, func(tx pgx.Tx) error {
		if b, err := tenantbrand.Load(t.Context(), tx); err != nil || b != nil {
			t.Fatalf("fresh session brand %+v %v", b, err)
		}
		return nil
	})

	// Agents never write the brand, even with the scope; members lack settings.manage.
	expect(t, f.call(f.agent, "PUT", "/api/settings/brand", "application/json", name("Acme")), 403)
	expect(t, f.call(f.agent, "PUT", "/api/settings/brand/logo/light", "image/png", logoPNG(t, 64, 64)), 403)
	expect(t, f.call(f.agent, "DELETE", "/api/settings/brand/logo/light", "", nil), 403)
	expect(t, f.call(f.agent, "GET", "/api/settings/brand", "", nil), 403)
	expect(t, f.call(f.member, "PUT", "/api/settings/brand", "application/json", name("Acme")), 403)
	expect(t, f.call(f.member, "PUT", "/api/settings/brand/logo/light", "image/png", logoPNG(t, 64, 64)), 403)
	expect(t, f.call(f.member, "GET", "/api/settings/brand", "", nil), 403)

	// Validation.
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand", "application/json", name(strings.Repeat("x", 33))), 400)
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand", "application/json", name("a‮b")), 400)
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand", "application/json", []byte(`{"short_name":"A","logo":"x"}`)), 400)
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand/logo/light", "image/png", logoPNG(t, 16, 16)), 400)
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand/logo/light", "image/svg+xml", logoPNG(t, 64, 64)), 400)
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand/logo/sepia", "image/png", logoPNG(t, 64, 64)), 404)
	tooLarge := func(w *httptest.ResponseRecorder) {
		t.Helper()
		expect(t, w, 413)
		var e struct{ Error, Code string }
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != "logo_too_large" || e.Error == "" {
			t.Fatalf("413 body %s", w.Body.String())
		}
	}
	tooLarge(f.call(f.admin, "PUT", "/api/settings/brand/logo/light", "image/svg+xml", append([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1">`), bytes.Repeat([]byte(" "), 300<<10)...)))
	// Under the limit as uploaded (171 KB), over it once rewritten (323 KB).
	grown := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` + strings.Repeat(`<circle/>`, 19000) + `</svg>`)
	tooLarge(f.call(f.admin, "PUT", "/api/settings/brand/logo/light", "image/svg+xml", grown))
	// The review's WebP: a 64×64 canvas around a 5000×64 frame.
	falseCanvas, _ := hex.DecodeString(tenantbrand.WebPFalseCanvas)
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand/logo/light", "image/webp", falseCanvas), 400)

	// The admin sets a name, a logo and a dark logo; each change is one event.
	s = settingsOf(t, f.call(f.admin, "PUT", "/api/settings/brand", "application/json", name("  Acme   Studio ")))
	if s.ShortName != "Acme Studio" {
		t.Fatalf("name %q", s.ShortName)
	}
	w := f.call(f.admin, "PUT", "/api/settings/brand/logo/light", "image/png", logoPNG(t, 160, 40))
	expect(t, w, 200)
	s = settingsOf(t, w)
	if s.Logo == nil || s.Logo.Width != 160 || s.Logo.Height != 40 || s.Logo.ContentType != "image/png" || !strings.HasPrefix(s.Logo.URL, "/api/brand/logo/light?v=") {
		t.Fatalf("logo %+v", s.Logo)
	}
	xss := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40" onload="alert(1)"><script>alert(2)</script><circle cx="20" cy="20" r="18" fill="#fff"/></svg>`
	w = f.call(f.admin, "PUT", "/api/settings/brand/logo/dark", "image/svg+xml", []byte(xss))
	expect(t, w, 400)
	if !strings.Contains(w.Body.String(), "SVG attribute onload is not supported") {
		t.Fatalf("missing refusal reason: %s", w.Body.String())
	}
	static := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40" role="img" aria-label="&lt;script&gt;" data-name="Mark"><!-- exported logo --><circle cx="20" cy="20" r="18" fill="#fff"/></svg>`
	w = f.call(f.admin, "PUT", "/api/settings/brand/logo/dark", "image/svg+xml", []byte(static))
	expect(t, w, 200)
	s = settingsOf(t, w)
	if s.LogoDark == nil || s.LogoDark.ContentType != "image/svg+xml" || !s.Cleaned || s.SVGCleanup == nil || s.SVGCleanup.RemovedAttributeCount != 3 {
		t.Fatalf("dark logo %+v cleaned %v", s.LogoDark, s.Cleaned)
	}
	// Refused uploads preserve the current logo and append no audit event.
	storedDark := f.call(f.member, "GET", s.LogoDark.URL, "", nil).Body.String()
	for _, bad := range []string{"role=", "aria-label", "data-name", "script", "exported logo"} {
		if strings.Contains(storedDark, bad) {
			t.Fatalf("removed metadata served: %s", storedDark)
		}
	}
	if got := settingsOf(t, f.call(f.admin, "GET", "/api/settings/brand", "", nil)); got.Cleaned || got.SVGCleanup != nil {
		t.Fatal("upload cleanup report persisted")
	}
	for _, feature := range []string{
		`<mask id="m"><rect width="40" height="40"/></mask>`,
		`<rect mask="inherit"/>`,
		`<rect style="clip-path:inherit"/>`,
		`<linearGradient id="g" href="#other"/>`,
		`<rect fill="url(#missing)"/>`,
		`<style>rect { fill: red }</style>`,
	} {
		body := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40">` + feature + `</svg>`)
		w = f.call(f.admin, "PUT", "/api/settings/brand/logo/dark", "image/svg+xml", body)
		expect(t, w, 400)
		var refused struct{ Error string }
		if err := json.Unmarshal(w.Body.Bytes(), &refused); err != nil || !strings.Contains(refused.Error, "SVG") {
			t.Fatalf("missing refusal reason: %s", w.Body.String())
		}
		if got := f.call(f.member, "GET", s.LogoDark.URL, "", nil).Body.String(); got != storedDark {
			t.Fatalf("refused SVG changed the stored logo: %s", got)
		}
	}
	// The same bytes again change nothing and append nothing.
	same := settingsOf(t, f.call(f.admin, "PUT", "/api/settings/brand/logo/dark", "image/svg+xml", []byte(static)))
	if !same.Cleaned || same.SVGCleanup == nil || same.SVGCleanup.RemovedAttributeCount != 3 {
		t.Fatalf("repeat upload lost cleanup report: %+v", same)
	}
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand", "application/json", name("Acme Studio")), 200)

	var types []string
	var last map[string]any
	f.tx(t, f.admin, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT type, actor_principal_id::text, after FROM events WHERE type=$1 ORDER BY id`, tenantbrand.EventType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ, actor string
			var after map[string]any
			if err := rows.Scan(&typ, &actor, &after); err != nil {
				return err
			}
			if actor != f.admin.ID {
				t.Fatalf("event actor %s", actor)
			}
			types, last = append(types, typ), after
		}
		return rows.Err()
	})
	if len(types) != 3 || last["short_name"] != "Acme Studio" || last["logo_dark"] == nil {
		t.Fatalf("events %v last %v", types, last)
	}

	// The session payload carries it, and the served SVG is the sanitized one.
	var session *tenantbrand.Public
	f.tx(t, f.member, func(tx pgx.Tx) error {
		var err error
		session, err = tenantbrand.Load(t.Context(), tx)
		return err
	})
	if session == nil || session.ShortName != "Acme Studio" || session.Logo == nil || session.LogoDark == nil || session.Logo.Width != 160 {
		t.Fatalf("session brand %+v", session)
	}
	w = f.call(f.member, "GET", session.LogoDark.URL, "", nil)
	expect(t, w, 200)
	h := w.Header()
	if h.Get("Content-Type") != "image/svg+xml" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "default-src 'none'; sandbox" || !strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Fatalf("headers %v", h)
	}
	if body := w.Body.String(); strings.Contains(body, "script") || strings.Contains(body, "onload") || !strings.Contains(body, "<circle") {
		t.Fatalf("served %s", body)
	}
	etag := h.Get("ETag")
	r := httptest.NewRequest("GET", session.LogoDark.URL, nil).WithContext(tenant.WithPrincipal(context.Background(), f.member))
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	expect(t, w, 304)
	if w = f.call(f.member, "GET", "/api/brand/logo/light", "", nil); w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("unversioned logo %d %v", w.Code, w.Header())
	}

	// Tenant RLS: another workspace sees none of it.
	expect(t, f.call(f.foreigner, "GET", "/api/brand/logo/light", "", nil), 404)
	if s := settingsOf(t, f.call(f.foreigner, "GET", "/api/settings/brand", "", nil)); s.ShortName != "" || s.Logo != nil {
		t.Fatalf("foreign tenant sees %+v", s)
	}
	f.tx(t, f.foreigner, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM tenant_brand_logos`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("foreign logos %d %v", n, err)
		}
		sp, err := tx.Begin(t.Context())
		if err != nil {
			return err
		}
		defer func() { _ = sp.Rollback(t.Context()) }()
		if _, err := sp.Exec(t.Context(), `INSERT INTO tenant_brand (tenant_id, short_name, updated_by_principal_id) VALUES ($1, 'x', $2)`, f.admin.TenantID, f.foreigner.ID); err == nil {
			t.Fatal("RLS let a foreign tenant write another brand")
		}
		return nil
	})

	// Removing the dark logo falls back to the light one; removing both leaves the name.
	s = settingsOf(t, f.call(f.admin, "DELETE", "/api/settings/brand/logo/dark", "", nil))
	if s.LogoDark != nil || s.Logo == nil {
		t.Fatalf("after delete %+v", s)
	}
	expect(t, f.call(f.member, "GET", "/api/brand/logo/dark", "", nil), 404)
	expect(t, f.call(f.admin, "DELETE", "/api/settings/brand/logo/light", "", nil), 200)
	expect(t, f.call(f.admin, "PUT", "/api/settings/brand", "application/json", name("")), 200)
	f.tx(t, f.admin, func(tx pgx.Tx) error {
		if b, err := tenantbrand.Load(t.Context(), tx); err != nil || b != nil {
			t.Fatalf("cleared brand %+v %v", b, err)
		}
		return nil
	})
}

// SPDX-License-Identifier: AGPL-3.0-only

package public

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/business/quotes"
	"github.com/inspr-at/paimos/internal/business/quotes/confirmation"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/quotepdf"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestLinkManagementRequiresAdmin(t *testing.T) {
	m := &Module{}
	mux := http.NewServeMux()
	m.Mount(mux)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/quotes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/versions/1/public-link"},
		{http.MethodPost, "/api/quotes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/versions/1/public-link"},
		{http.MethodPost, "/api/quotes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/versions/1/public-link/revoke"},
	} {
		for _, principal := range []*tenant.Principal{nil, {TenantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Kind: tenant.Person, Roles: []string{"member"}}} {
			request := httptest.NewRequest(route.method, route.path, nil)
			if principal != nil {
				request = request.WithContext(tenant.WithPrincipal(request.Context(), *principal))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, request)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d", route.method, route.path, rec.Code)
			}
		}
	}
}

func TestPublicSelectorResolverObeysForceRLS(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	selector := strings.Repeat("s", 32)
	if err := db.InTenant(dbtest.Seed(ctx), database.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'selector-rls','Selector RLS')`, tenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO quote_public_tenant_selectors(tenant_id,selector) VALUES($1::uuid,$2)`, tenantID, selector)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var resolved *string
	if err := database.App.QueryRow(ctx, `SELECT aeon_resolve_quote_public_tenant($1)::text`, selector).Scan(&resolved); err != nil || resolved != nil {
		t.Fatalf("resolver escaped RLS without tenant transaction: %v, %v", err, resolved)
	}
	for _, supplied := range []string{"", strings.Repeat("x", 32)} {
		err := db.InTenant(dbtest.Seed(ctx), database.App, zeroTenant, func(tx pgx.Tx) error {
			if supplied != "" {
				if _, err := tx.Exec(ctx, `SELECT set_config('aeon.public_quote_selector',$1,true)`, supplied); err != nil {
					return err
				}
			}
			return tx.QueryRow(ctx, `SELECT aeon_resolve_quote_public_tenant($1)::text`, selector).Scan(&resolved)
		})
		if err != nil || resolved != nil {
			t.Fatalf("resolver accepted mismatched selector %q: %v, %v", supplied, err, resolved)
		}
	}
	if err := db.InTenant(dbtest.Seed(ctx), database.App, zeroTenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.public_quote_selector',$1,true)`, selector); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT aeon_resolve_quote_public_tenant($1)::text`, selector).Scan(&resolved)
	}); err != nil || resolved == nil || *resolved != tenantID {
		t.Fatalf("matching sentinel lookup = %v, %v", resolved, err)
	}
}

func TestPublicRateLimitCoversReadPDFAndAcceptanceAcrossTokens(t *testing.T) {
	database := dbtest.Open(t)
	for _, operation := range []struct {
		method, suffix string
		limit          int
	}{
		{http.MethodGet, "", 120},
		{http.MethodGet, "/pdf", 20},
		{http.MethodPost, "/accept", 10},
	} {
		m := &Module{pool: database.App}
		mux := http.NewServeMux()
		m.Mount(mux)
		started := time.Now()
		for i := 0; i <= operation.limit; i++ {
			token := strings.Repeat("A", 42) + string(rune('A'+i%2))
			req := httptest.NewRequest(operation.method, "/api/public/quotes/invalid/"+token+operation.suffix, nil)
			req.RemoteAddr = "192.0.2.5:43210"
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			elapsed := time.Since(started)
			if i == operation.limit && rec.Code != http.StatusTooManyRequests {
				t.Fatalf("%s%s request %d: got %d", operation.method, operation.suffix, i, rec.Code)
			}
			if i == operation.limit {
				// The first stored attempt and the denial's database clock read
				// both fall inside this measured interval. The remaining window,
				// rounded up to seconds, cannot be shorter than 60s - elapsed.
				minRetry := max(1, int((publicLimitWindow-elapsed+time.Second-1)/time.Second))
				retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
				if err != nil || retry < minRetry || retry > int(publicLimitWindow/time.Second) {
					t.Fatalf("%s%s request %d: Retry-After = %q, want %d..60 after %s", operation.method, operation.suffix, i, rec.Header().Get("Retry-After"), minRetry, elapsed)
				}
			}
			if i < operation.limit && rec.Code == http.StatusTooManyRequests {
				t.Fatalf("%s%s request %d limited early", operation.method, operation.suffix, i)
			}
		}
	}
}

type fixture struct {
	t            *testing.T
	pool         *dbtest.DB
	mux          *http.ServeMux
	reg          *plugins.Registry
	store        attachments.Store
	tenantID     string
	admin        tenant.Principal
	customer     tenant.Principal
	org, contact string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "link-key")
	if err := os.WriteFile(keyFile, []byte("synthetic-link-key-for-tests-0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_LINK_KEY_FILE", keyFile)
	database := dbtest.Open(t)
	f := &fixture{t: t, pool: database, mux: http.NewServeMux(), tenantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", store: attachments.Store{FilesDir: t.TempDir()}}
	reg := plugins.NewRegistry()
	for _, id := range []string{"business_costs", "business_crm"} {
		p := plugins.Plugin{Manifest: plugins.Manifest{ID: id, Version: "1", Owner: "aeon", Permissions: []string{fence.PermNodesContribute}}}
		sum, err := plugins.Digest(p)
		if err != nil {
			t.Fatal(err)
		}
		p.Manifest.DigestSHA256 = sum
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	qp, err := quotes.ManifestPlugin()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(qp); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	f.reg = reg
	ctx := t.Context()
	err = db.InTenant(dbtest.Seed(ctx), database.App, f.tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'public-test','Public test')`, f.tenantID); err != nil {
			return err
		}
		f.admin = tenant.Principal{TenantID: f.tenantID, Kind: tenant.Person, Roles: []string{"admin"}}
		f.customer = tenant.Principal{TenantID: f.tenantID, Kind: tenant.Person, Roles: []string{"customer"}}
		for _, p := range []*tenant.Principal{&f.admin, &f.customer} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Synthetic user',$2) RETURNING id::text`, f.tenantID, p.Roles).Scan(&p.ID); err != nil {
				return err
			}
			if err := dbtest.BindLegacyTx(ctx, tx, f.tenantID, p.ID); err != nil {
				return err
			}
		}
		var orgKind, contactKind string
		for _, k := range []struct {
			slug, prefix string
			target       *string
		}{{"organisation", "ORG", &orgKind}, {"contact", "CON", &contactKind}, {"quote", "QUO", new(string)}} {
			if err := tx.QueryRow(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1::uuid,$2,$2,$3,$2) RETURNING id::text`, f.tenantID, k.slug, k.prefix).Scan(k.target); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) VALUES($1::uuid,'ORG-1',$2::uuid,'Customer') RETURNING id::text`, f.tenantID, orgKind).Scan(&f.org); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) VALUES($1::uuid,'CON-1',$2::uuid,'Contact') RETURNING id::text`, f.tenantID, contactKind).Scan(&f.contact); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'contact_for')`, f.tenantID, f.contact, f.org); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm_contact_principals(tenant_id,contact_node_id,principal_id,bound_by_principal_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, f.tenantID, f.contact, f.customer.ID, f.admin.ID); err != nil {
			return err
		}
		for _, id := range []string{"business_costs", "business_crm", quotes.PluginID} {
			p, _ := reg.Lookup(id)
			if _, err := tx.Exec(ctx, `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id) VALUES($1::uuid,$2,$3,$4,$5,true,$6,$7::uuid)`, f.tenantID, id, p.Manifest.Version, p.Manifest.DigestSHA256, p.Manifest.Owner, p.Manifest.Permissions, f.admin.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	quoteModule, err := quotes.New(database.App, reg)
	if err != nil {
		t.Fatal(err)
	}
	quoteModule.Mount(f.mux)
	publicModule, err := NewWithStore(database.App, reg, nil, f.store, "https://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	publicModule.Mount(f.mux)
	events.New(database.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	status, body := f.call(&f.admin, "PATCH", "/api/quotes/settings", `{"expected_revision":0,"numbering_time_zone":"Europe/Vienna","default_currency":"EUR","sender":{"company":"Example Sender","street":"Example Street 1","postal_code":"0000","city":"Example City","country":"AT","email":"sender@example.test"},"defaults":{"intro":"","blocks":[],"accept_text":"","vat_note":""},"layout":{},"smtp_confirmation_enabled":false}`)
	if status != 200 {
		t.Fatalf("settings %d %s", status, body)
	}
	return f
}
func (f *fixture) call(p *tenant.Principal, method, path, body string) (int, string) {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != nil {
		req = req.WithContext(tenant.WithPrincipal(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
func object(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func (f *fixture) issued() (string, string, string) {
	f.t.Helper()
	status, body := f.call(&f.admin, "POST", "/api/quotes", fmt.Sprintf(`{"title":"Synthetic offer","customer_org_node_id":%q}`, f.org))
	if status != 201 {
		f.t.Fatalf("create %d %s", status, body)
	}
	id := object(f.t, body)["quote_node_id"].(string)
	status, body = f.call(&f.admin, "GET", "/api/quotes/"+id+"/draft", "")
	if status != 200 {
		f.t.Fatalf("draft %d %s", status, body)
	}
	doc := object(f.t, body)["document"].(map[string]any)
	recipient := doc["recipient"].(map[string]any)
	recipient["address"] = "Sample Lane 2"
	recipient["email"] = "customer@example.invalid"
	recipient["contact_node_id"] = f.contact
	doc["positions"] = []any{map[string]any{"id": "11111111-1111-4111-8111-111111111111", "pricing_source": "manual", "short_text": "Service", "long_text": "Synthetic", "quantity": "1.00", "unit_label": "item", "unit_price_cents": 100, "total_cents": 0, "currency": "EUR"}}
	patch, _ := json.Marshal(map[string]any{"client_session_id": "22222222-2222-4222-8222-222222222222", "mutation_id": "33333333-3333-4333-8333-333333333333", "writer_version": 1, "document": doc})
	req := httptest.NewRequest("PATCH", "/api/quotes/"+id+"/draft", strings.NewReader(string(patch)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"qd-1"`)
	req = req.WithContext(tenant.WithPrincipal(req.Context(), f.admin))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		f.t.Fatalf("patch %d %s", rec.Code, rec.Body.String())
	}
	saved := object(f.t, rec.Body.String())
	status, body = f.call(&f.admin, "GET", "/api/quotes/"+id, "")
	if status != 200 {
		f.t.Fatalf("quote %d %s", status, body)
	}
	revision := object(f.t, body)["revision"].(float64)
	status, body = f.call(&f.admin, "POST", "/api/quotes/"+id+"/finalize", fmt.Sprintf(`{"expected_quote_revision":%.0f,"expected_draft_revision":2,"expected_document_sha256":%q}`, revision, saved["document_sha256"]))
	if status != 200 {
		f.t.Fatalf("finalize %d %s", status, body)
	}
	status, body = f.call(&f.admin, "GET", "/api/quotes/"+id+"/versions/1", "")
	if status != 200 {
		f.t.Fatalf("version %d %s", status, body)
	}
	digest := object(f.t, body)["content_sha256"].(string)
	status, body = f.call(&f.admin, "GET", "/api/quotes/"+id+"/versions/1/public-link", "")
	if status != 200 {
		f.t.Fatalf("link %d %s", status, body)
	}
	return id, digest, object(f.t, body)["path"].(string)
}

func TestPublicQuoteRoutesSendStrictHeaders(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{
		"/api/public/quotes/not-a-tenant/not-a-token",
		"/api/public/quotes/not-a-tenant/not-a-token/pdf",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status %d %s", path, rec.Code, rec.Body.String())
		}
		assertPublicHeaders(t, rec)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/public/quotes/not-a-tenant/not-a-token/accept", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("accept status %d %s", rec.Code, rec.Body.String())
	}
	assertPublicHeaders(t, rec)
	_, _, path := f.issued()
	req = httptest.NewRequest(http.MethodGet, "/api/public/quotes/"+strings.TrimPrefix(path, "/offers/"), nil)
	rec = httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issued read %d %s", rec.Code, rec.Body.String())
	}
	assertPublicHeaders(t, rec)
}

func assertPublicHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; frame-ancestors 'none'" {
		t.Fatalf("csp %q", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("X-Robots-Tag") != "noindex, nofollow, noarchive" {
		t.Fatalf("cache %q referrer %q robots %q", rec.Header().Get("Cache-Control"), rec.Header().Get("Referrer-Policy"), rec.Header().Get("X-Robots-Tag"))
	}
}

func TestPublicLinkReadDoesNotRotateCapability(t *testing.T) {
	f := newFixture(t)
	id, _, path := f.issued()
	url := "/api/quotes/" + id + "/versions/1/public-link"
	status, body := f.call(&f.admin, "GET", url, "")
	if status != 200 || strings.Contains(body, `"token"`) || !strings.Contains(body, path) {
		t.Fatalf("stable admin copy unavailable: %d %s", status, body)
	}
	status, _ = f.call(&f.admin, "POST", url, "")
	if status != 409 {
		t.Fatalf("existing capability rotated: %d", status)
	}
	status, _ = f.call(nil, "GET", "/api/public/quotes/"+strings.TrimPrefix(path, "/offers/"), "")
	if status != 200 {
		t.Fatalf("original capability stopped working: %d", status)
	}
	var retained, leaked int
	var ciphertext []byte
	token := path[strings.LastIndex(path, "/")+1:]
	err := db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT t.ciphertext,(SELECT count(*) FROM events WHERE node_id=$1::uuid AND after::text LIKE '%' || $2 || '%') FROM quote_public_link_tokens t JOIN quote_public_links l ON l.tenant_id=t.tenant_id AND l.id=t.link_id WHERE l.quote_node_id=$1::uuid`, id, token).Scan(&ciphertext, &leaked)
	})
	if err != nil || len(ciphertext) == 0 || strings.Contains(string(ciphertext), token) || leaked != 0 {
		t.Fatalf("capability ciphertext or event privacy: bytes=%d leaked=%d err=%v", len(ciphertext), leaked, err)
	}
	var plaintextColumns int
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_name='quote_public_link_tokens' AND column_name='token'`).Scan(&plaintextColumns)
	})
	if err != nil || plaintextColumns != 0 {
		t.Fatalf("plaintext token column survives: %d %v", plaintextColumns, err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, zeroTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM quote_public_link_tokens`).Scan(&retained)
	})
	if err != nil || retained != 0 {
		t.Fatalf("capability crossed tenant boundary: count=%d err=%v", retained, err)
	}
}

func TestPublicLinkWithoutKeyIsShownOnceAndRevocationDeletes(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.issued()
	url := "/api/quotes/" + id + "/versions/1/public-link"
	if status, _ := f.call(&f.admin, "POST", url+"/revoke", ""); status != 200 {
		t.Fatalf("revoke first link: %d", status)
	}
	t.Setenv("AEON_LINK_KEY_FILE", "")
	t.Setenv("AEON_MESSAGING_KEY_FILE", "")
	module, err := NewWithStore(f.pool.App, f.reg, nil, f.store, "https://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	module.Mount(mux)
	call := func(method, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req = req.WithContext(tenant.WithPrincipal(req.Context(), f.admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, object(t, rec.Body.String())
	}
	status, created := call("POST", "")
	if status != 201 || created["path"] == nil || created["copy_unavailable_reason"] != "key_not_configured" {
		t.Fatalf("one-time creation: %d %v", status, created)
	}
	path := created["path"].(string)
	status, info := call("GET", "")
	if status != 200 || info["path"] != nil || info["copy_unavailable_reason"] != "key_not_configured" {
		t.Fatalf("re-copy was not refused: %d %v", status, info)
	}
	if status, _ := f.call(nil, "GET", "/api/public/quotes/"+strings.TrimPrefix(path, "/offers/"), ""); status != 200 {
		t.Fatalf("one-time capability no longer verifies: %d", status)
	}
	status, _ = call("POST", "")
	if status != 409 {
		t.Fatalf("active capability rotated: %d", status)
	}
	status, body := func() (int, map[string]any) {
		req := httptest.NewRequest("POST", url+"/revoke", nil).WithContext(tenant.WithPrincipal(t.Context(), f.admin))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, object(t, rec.Body.String())
	}()
	if status != 200 || body["revoked_at"] == nil {
		t.Fatalf("revoke one-time link: %d %v", status, body)
	}
	var retained int
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM quote_public_link_tokens`).Scan(&retained)
	})
	if err != nil || retained != 0 {
		t.Fatalf("revoked vault row remains: %d %v", retained, err)
	}
}

func TestPlaintextVaultMigrationEncryptsExistingLink(t *testing.T) {
	f := newFixture(t)
	id, _, path := f.issued()
	token := path[strings.LastIndex(path, "/")+1:]
	// Recreate the pre-0770 vault schema in this isolated test database.
	_, err := f.pool.Admin.Exec(t.Context(), `ALTER TABLE quote_public_link_tokens DROP CONSTRAINT quote_public_link_tokens_ciphertext_required`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Admin.Exec(t.Context(), `ALTER TABLE quote_public_link_tokens ADD COLUMN token text`)
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE quote_public_link_tokens SET token=$1`, token)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Admin.Exec(t.Context(), `ALTER TABLE quote_public_link_tokens DROP COLUMN ciphertext`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Admin.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version IN ('0770_quote_link_ciphertext.sql','0771_quote_link_drop_plaintext.sql')`)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := db.Open(t.Context(), f.pool.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	status, body := f.call(&f.admin, "GET", "/api/quotes/"+id+"/versions/1/public-link", "")
	if status != 200 || object(t, body)["path"] != path {
		t.Fatalf("migrated link cannot be re-copied: %d %s", status, body)
	}
	var plaintextColumns int
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_name='quote_public_link_tokens' AND column_name='token'`).Scan(&plaintextColumns)
	})
	if err != nil || plaintextColumns != 0 {
		t.Fatalf("plaintext column survives migration: %d %v", plaintextColumns, err)
	}
}
func acceptanceBody(digest, mutation string) string {
	return fmt.Sprintf(`{"version":1,"expected_content_sha256":%q,"client_mutation_id":%q,"name":"Customer Example","company":"Example Co","note":"Approved","confirm":true}`, digest, mutation)
}

func TestPublicSelectorUnderForcedRLS(t *testing.T) {
	f := newFixture(t)
	var superuser, bypassRLS, forcedRLS bool
	err := db.InTenant(dbtest.Seed(t.Context()), f.pool.App, zeroTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT r.rolsuper,r.rolbypassrls,c.relforcerowsecurity
			FROM pg_roles r CROSS JOIN pg_class c
			WHERE r.rolname=current_user AND c.oid='quote_public_tenant_selectors'::regclass`).Scan(&superuser, &bypassRLS, &forcedRLS)
	})
	if err != nil || superuser || bypassRLS || !forcedRLS {
		t.Fatalf("public lookup requires a forced-RLS service role: super=%t bypass=%t forced=%t err=%v", superuser, bypassRLS, forcedRLS, err)
	}
	quoteID, digest, path := f.issued()
	api := "/api/public/quotes/" + strings.TrimPrefix(path, "/offers/")
	status, body := f.call(nil, "GET", api, "")
	if status != http.StatusOK || object(t, body)["content_sha256"] != digest {
		t.Fatalf("anonymous quote projection %d %s", status, body)
	}
	status, _ = f.call(nil, "GET", api+"/pdf", "")
	// This fixture has no print assets; 503 means the capability resolved and
	// rendering was reached. The image smoke checks the actual PDF bytes.
	if status != http.StatusServiceUnavailable {
		t.Fatalf("anonymous PDF did not reach renderer: %d", status)
	}
	status, _ = f.call(nil, "GET", strings.Replace(api, strings.Split(strings.TrimPrefix(path, "/offers/"), "/")[0], "unmatched-selector-1234567890", 1), "")
	if status != http.StatusNotFound {
		t.Fatalf("unknown selector exposed quote: %d", status)
	}
	status, _ = f.call(&f.admin, "POST", "/api/quotes/"+quoteID+"/versions/1/public-link/revoke", "")
	if status != http.StatusOK {
		t.Fatalf("revoke %d", status)
	}
	status, _ = f.call(nil, "GET", api, "")
	if status != http.StatusNotFound {
		t.Fatalf("revoked link readable: %d", status)
	}
	var retained int
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM quote_public_link_tokens t JOIN quote_public_links l ON l.tenant_id=t.tenant_id AND l.id=t.link_id WHERE l.quote_node_id=$1::uuid`, quoteID).Scan(&retained)
	})
	if err != nil || retained != 0 {
		t.Fatalf("revoked capability retained: count=%d err=%v", retained, err)
	}
}

func TestPublicCapabilityReplayPrivacyAndDecisionRace(t *testing.T) {
	f := newFixture(t)
	id, digest, path := f.issued()
	selectorAndToken := strings.TrimPrefix(path, "/offers/")
	api := "/api/public/quotes/" + selectorAndToken
	status, body := f.call(nil, "GET", api, "")
	if status != 200 || strings.Contains(body, id) || strings.Contains(body, f.admin.ID) || !strings.Contains(body, `"acceptable":true`) {
		t.Fatalf("scoped read %d %s", status, body)
	}
	mutation := "44444444-4444-4444-8444-444444444444"
	status, body = f.call(nil, "POST", api+"/accept", acceptanceBody(digest, mutation))
	if status != 201 {
		t.Fatalf("public accept %d %s", status, body)
	}
	status, _ = f.call(nil, "POST", api+"/accept", acceptanceBody(digest, mutation))
	if status != 200 {
		t.Fatalf("exact replay %d", status)
	}
	status, _ = f.call(nil, "POST", api+"/accept", strings.Replace(acceptanceBody(digest, mutation), "Approved", "Changed", 1))
	if status != 409 {
		t.Fatalf("divergent replay %d", status)
	}
	status, _ = f.call(&f.customer, "POST", "/api/quotes/"+id+"/versions/1/accept", fmt.Sprintf(`{"expected_content_sha256":%q}`, digest))
	if status != 409 {
		t.Fatalf("authenticated channel won after public acceptance: %d", status)
	}
	var decisions, jobs, receipts int
	var recipientFrozen bool
	err := db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM quote_decisions WHERE quote_node_id=$1::uuid),(SELECT count(*) FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid),(SELECT count(*) FROM quote_public_acceptances WHERE quote_node_id=$1::uuid),(SELECT j.recipient_snapshot=s.recipient FROM quote_confirmation_jobs j JOIN quote_version_snapshots s ON s.tenant_id=j.tenant_id AND s.quote_node_id=j.quote_node_id AND s.version=j.version WHERE j.quote_node_id=$1::uuid)`, id).Scan(&decisions, &jobs, &receipts, &recipientFrozen)
	})
	if err != nil || decisions != 1 || jobs != 1 || receipts != 1 || !recipientFrozen {
		t.Fatalf("decision projection %d/%d/%d, recipient frozen %t: %v", decisions, jobs, receipts, recipientFrozen, err)
	}
	if _, err := os.Stat("../../../../web/dist/quote-print.html"); err == nil && quotepdf.Available() {
		confirmationModule, err := confirmation.New(f.pool.App, f.reg, os.DirFS("../../../../web/dist"), f.store)
		if err != nil {
			t.Fatal(err)
		}
		plugin, err := confirmation.ManifestPlugin(confirmationModule)
		if err != nil || plugin.Manifest.DigestSHA256 == "" || plugin.Jobs == nil {
			t.Fatalf("confirmation manifest: %v", err)
		}
		confirmationModule.Mount(f.mux)
		processed, err := confirmationModule.ProcessNext(t.Context(), f.tenantID)
		if err != nil || !processed {
			t.Fatalf("receipt processing %v: %v", processed, err)
		}
		var receiptHash, jobState string
		err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT r.file_sha256,j.state FROM quote_confirmation_receipts r JOIN quote_confirmation_jobs j ON j.tenant_id=r.tenant_id AND j.quote_node_id=r.quote_node_id AND j.version=r.version WHERE r.quote_node_id=$1::uuid AND r.version=1`, id).Scan(&receiptHash, &jobState)
		})
		if err != nil || jobState != "ready" || len(receiptHash) != 64 {
			var state, safeError string
			_ = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT state,last_safe_error FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid AND version=1`, id).Scan(&state, &safeError)
			})
			t.Fatalf("receipt binding %q %q: %v (job %q: %q)", receiptHash, jobState, err, state, safeError)
		}
		file, err := f.store.Open(f.tenantID, receiptHash, "original")
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		status, body = f.call(nil, "GET", api+"/pdf", "")
		if status != 200 || !strings.HasPrefix(body, "%PDF-") {
			t.Fatalf("public accepted receipt download %d", status)
		}
		status, body = f.call(&f.admin, "GET", "/api/quotes/"+id+"/versions/1/confirmation/receipt", "")
		if status != 200 || !strings.HasPrefix(body, "%PDF-") {
			t.Fatalf("admin accepted receipt download %d", status)
		}
		processed, err = confirmationModule.ProcessNext(t.Context(), f.tenantID)
		if err != nil || processed {
			t.Fatalf("receipt rendered twice %v: %v", processed, err)
		}
		// Model an ambiguous future transport result with a durable event. The
		// disabled adapter cannot emit this state in the private instance.
		err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
			_, err := events.Append(t.Context(), tx, f.admin, events.Change{NodeID: &id, Type: "quote.confirmation_uncertain", After: map[string]any{"version": 1, "state": "uncertain"}})
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `UPDATE quote_confirmation_jobs SET state='uncertain' WHERE quote_node_id=$1::uuid AND version=1`, id)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		retryPath := "/api/quotes/" + id + "/versions/1/confirmation/retry"
		status, _ = f.call(&f.admin, "POST", retryPath, `{"acknowledge_uncertain":false}`)
		if status != 409 {
			t.Fatalf("uncertain retry without acknowledgement %d", status)
		}
		status, body = f.call(&f.admin, "POST", retryPath, `{"acknowledge_uncertain":true}`)
		if status != 200 || !strings.Contains(body, `"state":"ready"`) || !strings.Contains(body, receiptHash) {
			t.Fatalf("acknowledged uncertainty %d %s", status, body)
		}
	}
	status, _ = f.call(&f.admin, "POST", "/api/quotes/"+id+"/versions/1/public-link/revoke", "")
	if status != 200 {
		t.Fatalf("revoke %d", status)
	}
	status, _ = f.call(nil, "GET", api, "")
	if status != 404 {
		t.Fatalf("revoked link readable %d", status)
	}
	// A second offer tests simultaneous channels against the same quote row.
	id2, digest2, path2 := f.issued()
	api2 := "/api/public/quotes/" + strings.TrimPrefix(path2, "/offers/")
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		statuses[0], _ = f.call(nil, "POST", api2+"/accept", acceptanceBody(digest2, "55555555-5555-4555-8555-555555555555"))
	}()
	go func() {
		defer wg.Done()
		statuses[1], _ = f.call(&f.customer, "POST", "/api/quotes/"+id2+"/versions/1/accept", fmt.Sprintf(`{"expected_content_sha256":%q}`, digest2))
	}()
	wg.Wait()
	if !((statuses[0] == 201 && statuses[1] == 409) || (statuses[1] == 201 && statuses[0] == 409)) {
		t.Fatalf("two-channel decision race: %v", statuses)
	}
	if _, err := os.Stat("../../../../web/dist/quote-print.html"); err == nil {
		unavailable, err := confirmation.New(f.pool.App, f.reg, nil, f.store)
		if err != nil {
			t.Fatal(err)
		}
		if !quotepdf.Available() {
			// The first accepted quote still has a pending receipt when the
			// renderer is unavailable; fail that job before testing id2.
			unavailable.Mount(f.mux)
			if processed, err := unavailable.ProcessNext(t.Context(), f.tenantID); err != nil || !processed {
				t.Fatalf("first unavailable render transition %v: %v", processed, err)
			}
		}
		processed, err := unavailable.ProcessNext(t.Context(), f.tenantID)
		if err != nil || !processed {
			t.Fatalf("failed render transition %v: %v", processed, err)
		}
		var state string
		err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT state FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid AND version=1`, id2).Scan(&state)
		})
		if err != nil || state != "failed" {
			t.Fatalf("failed job state %q: %v", state, err)
		}
		err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
			_, err := events.Append(t.Context(), tx, f.admin, events.Change{NodeID: &id2, Type: "quote.confirmation_retry_limit_fixture", After: map[string]any{"attempts": 5}})
			if err != nil {
				return err
			}
			_, err = tx.Exec(t.Context(), `UPDATE quote_confirmation_jobs SET attempts=5,next_attempt_at=clock_timestamp()-interval '1 minute' WHERE quote_node_id=$1::uuid AND version=1`, id2)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		processed, err = unavailable.ProcessNext(t.Context(), f.tenantID)
		if err != nil || processed {
			t.Fatalf("exhausted job retried automatically %v: %v", processed, err)
		}
		status, _ := f.call(&f.admin, "POST", "/api/quotes/"+id2+"/versions/1/confirmation/retry", `{"acknowledge_uncertain":false}`)
		if status != 200 {
			t.Fatalf("failed job retry %d", status)
		}
		err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
			var attempts int
			if err := tx.QueryRow(t.Context(), `SELECT attempts FROM quote_confirmation_jobs WHERE quote_node_id=$1::uuid AND version=1`, id2).Scan(&attempts); err != nil {
				return err
			}
			if attempts != 0 {
				return fmt.Errorf("manual retry did not reset attempt window: %d", attempts)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		available, err := confirmation.New(f.pool.App, f.reg, os.DirFS("../../../../web/dist"), f.store)
		if err != nil {
			t.Fatal(err)
		}
		processed, err = available.ProcessNext(t.Context(), f.tenantID)
		if err != nil || !processed {
			t.Fatalf("retry render %v: %v", processed, err)
		}
	}
}

func TestAcceptanceNoticesFilterToOfferCreator(t *testing.T) {
	f := newFixture(t)
	id, digest, path := f.issued()
	api := "/api/public/quotes/" + strings.TrimPrefix(path, "/offers/")
	status, body := f.call(nil, "POST", api+"/accept", acceptanceBody(digest, "77777777-7777-4777-8777-777777777777"))
	if status != 201 {
		t.Fatalf("accept %d %s", status, body)
	}
	module, err := confirmation.New(f.pool.App, f.reg, nil, f.store)
	if err != nil {
		t.Fatal(err)
	}
	module.Mount(f.mux)
	var other tenant.Principal
	other = tenant.Principal{TenantID: f.tenantID, Kind: tenant.Person, Roles: []string{"admin"}}
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Other creator',$2) RETURNING id::text`, f.tenantID, other.Roles).Scan(&other.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, f.pool, f.tenantID, other.ID)
	status, body = f.call(&f.admin, "GET", "/api/quotes/acceptances?created_by_me=true", "")
	if status != 200 || !strings.Contains(body, id) {
		t.Fatalf("creator notices %d %s", status, body)
	}
	status, body = f.call(&other, "GET", "/api/quotes/acceptances?created_by_me=true", "")
	if status != 200 || strings.Contains(body, id) {
		t.Fatalf("other creator notices %d %s", status, body)
	}
	status, body = f.call(&other, "GET", "/api/quotes/acceptances", "")
	if status != 200 || !strings.Contains(body, id) {
		t.Fatalf("general notices %d %s", status, body)
	}
}

func TestAcceptRejectsCrossSiteAndInvalidToken(t *testing.T) {
	f := newFixture(t)
	_, digest, path := f.issued()
	api := "/api/public/quotes/" + strings.TrimPrefix(path, "/offers/")
	req := httptest.NewRequest("POST", api+"/accept", strings.NewReader(acceptanceBody(digest, "44444444-4444-4444-8444-444444444444")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://elsewhere.invalid")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("cross-site request %d", rec.Code)
	}
	status, _ := f.call(nil, "GET", api+"wrong", "")
	if status != 404 {
		t.Fatalf("invalid token %d", status)
	}
}

func TestPublicAcceptanceHTTPRateLimit(t *testing.T) {
	f := newFixture(t)
	_, _, path := f.issued()
	api := "/api/public/quotes/" + strings.TrimPrefix(path, "/offers/") + "/accept"
	for attempt := 0; attempt < 11; attempt++ {
		mutation := fmt.Sprintf("44444444-4444-4444-8444-%012d", attempt)
		status, body := f.call(nil, "POST", api, acceptanceBody(strings.Repeat("b", 64), mutation))
		if attempt < 10 && status != 409 {
			t.Fatalf("attempt %d: expected stale acceptance, got %d %s", attempt, status, body)
		}
		if attempt == 10 && status != 429 {
			t.Fatalf("attempt %d: expected rate limit, got %d %s", attempt, status, body)
		}
	}
}

func TestExpiredReadableAndLinkCreationUndo(t *testing.T) {
	f := newFixture(t)
	id, digest, path := f.issued()
	selector := strings.Split(strings.TrimPrefix(path, "/offers/"), "/")[0]
	var originalEvent int64
	err := db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT issued_event_id FROM quote_public_links WHERE quote_node_id=$1::uuid`, id).Scan(&originalEvent)
	})
	if err != nil {
		t.Fatal(err)
	}
	status, _ := f.call(&f.admin, "POST", fmt.Sprintf("/api/events/%d/undo", originalEvent), "")
	if status != 201 {
		t.Fatalf("link creation undo %d", status)
	}
	status, _ = f.call(nil, "GET", "/api/public/quotes/"+strings.TrimPrefix(path, "/offers/"), "")
	if status != 404 {
		t.Fatalf("undone link readable %d", status)
	}
	// Insert a historical, already expired capability to prove that expiry is
	// a decision fence while the document remains readable.
	token, err := randomURL(32)
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.pool.App, f.tenantID, func(tx pgx.Tx) error {
		event, err := events.Append(t.Context(), tx, f.admin, events.Change{NodeID: &id, Type: "quote.public_link_created", After: map[string]any{"version": 1, "target_content_sha256": digest}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO quote_public_links(tenant_id,quote_node_id,version,token_sha256,target_content_sha256,issued_at,expires_at,issued_by_principal_id,issued_event_id) VALUES($1::uuid,$2::uuid,1,$3,$4,clock_timestamp()-interval '3 days',clock_timestamp()-interval '1 day',$5::uuid,$6)`, f.tenantID, id, hash(token), digest, f.admin.ID, event.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	api := "/api/public/quotes/" + selector + "/" + token
	status, body := f.call(nil, "GET", api, "")
	if status != 200 || !strings.Contains(body, `"acceptable":false`) {
		t.Fatalf("expired read %d %s", status, body)
	}
	status, _ = f.call(nil, "POST", api+"/accept", acceptanceBody(digest, "66666666-6666-4666-8666-666666666666"))
	if status != 409 {
		t.Fatalf("expired acceptance %d", status)
	}
}

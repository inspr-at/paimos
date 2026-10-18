// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

const previewApp = "https://app.example.com"
const previewOrigin = "https://preview.example.net"

func testLease(p tenant.Principal) func(*http.Request) (func(context.Context) (tenant.Principal, error), error) {
	return func(*http.Request) (func(context.Context) (tenant.Principal, error), error) {
		return func(context.Context) (tenant.Principal, error) { return p, nil }, nil
	}
}

func TestSandboxOrigin(t *testing.T) {
	for _, origin := range []string{"", previewApp, "https://other.example.com", "http://preview.example.net", "https://127.0.0.1", "https://localhost", "https://preview.local", "https://example.net:443", "https://example.net:", "https://example.net/", "https://example.net#", "https://example.net?", "https://example.net?x=1", "https://user@example.net", "https://example.net#x", "https://EXAMPLE.NET", "https://example.net.", "https://example.net;script-src", "https://foo.github.io", "https://[::1]"} {
		if got := NewSandbox(previewApp, origin, testLease(tenant.Principal{})).Origin(); got != "" {
			t.Errorf("unsafe origin accepted: %q", origin)
		}
	}
	if NewSandbox(previewApp, previewOrigin, nil).Origin() != "" {
		t.Fatal("missing verifier enabled sandbox")
	}
	if NewSandbox("http://app.example.com", previewOrigin, testLease(tenant.Principal{})).Origin() != "" {
		t.Fatal("insecure app enabled sandbox")
	}
	if NewSandbox(previewApp, previewOrigin, testLease(tenant.Principal{})).Origin() != previewOrigin {
		t.Fatal("safe origin disabled")
	}
}

func TestSandboxHostBoundary(t *testing.T) {
	m := New(nil, Store{})
	m.Sandbox = NewSandbox(previewApp, previewOrigin, testLease(tenant.Principal{}))
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "app-session=fixture")
		w.WriteHeader(204)
	})
	h := m.WrapSandbox(app)
	for _, path := range []string{"/", "/api/auth/login", "/api/me", "/sw.js", "/preview/unknown", "/preview/../api/auth/login"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", previewOrigin+path, nil))
		if w.Code != 404 || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("sandbox path reached app: %s", path)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || w.Header().Get("Cache-Control") != "no-store, private" {
			t.Fatal("sandbox error lacks protections")
		}
	}
	for _, host := range []string{"https://alias.example.org", "https://preview.example.net:443"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", host+"/api/auth/login", nil))
		if w.Code != 421 || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("unknown host reached app")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", previewApp+"/api/me", nil))
	if w.Code != 204 {
		t.Fatal("app host refused")
	}
	for _, appURL := range []string{"", "invalid", "http://app.example.com", previewApp + "/"} {
		for _, origin := range []string{previewOrigin, previewOrigin + "/", "http://preview.example.net"} {
			m.Sandbox = NewSandbox(appURL, origin, testLease(tenant.Principal{}))
			for _, host := range []string{previewOrigin, previewOrigin + ":443", previewOrigin + ".", strings.ToUpper(previewOrigin)} {
				w = httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", host+"/api/auth/login", nil))
				if w.Code != 421 || w.Header().Get("Set-Cookie") != "" {
					t.Fatal("disabled sandbox became an app alias")
				}
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", previewApp+"/api/me", nil))
			if w.Code != 204 {
				t.Fatal("disabled sandbox changed app routing")
			}
		}
	}
	// An unconfigured feature must not change existing health-check host routing.
	m.Sandbox = NewSandbox(previewApp, "", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/health", nil))
	if w.Code != 204 {
		t.Fatal("unconfigured preview changed app routing")
	}
}

func TestHTMLStorageAndInertThumbnail(t *testing.T) {
	d, p, node := setup(t)
	s := Store{FilesDir: t.TempDir()}
	tid := p.TenantID
	doc := `<html><head><title>Local fragment</title><script>fetch('http://169.254.169.254/');while(true){}</script><style>@import 'http://localhost/';</style></head><body><h1>Hello</h1><img src="http://10.0.0.1/"><svg onload="fetch('/api/me')"></svg><p>Inert text.</p></body></html>`
	a, err := s.StageNamed(t.Context(), tid, strings.NewReader(doc), "fragment.html")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, variant := range []string{"original", "thumb"} {
		if f, err := s.Open(tid, a.SHA256, variant); err == nil {
			f.Close()
			t.Fatalf("HTML %s became visible before publication", variant)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if !isHTML(a.ContentType) || a.Image {
		t.Fatalf("HTML storage failed: %+v", a.Prepared)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		if err := Publish(t.Context(), tx, OwnerAttachment, a); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,$3,'fragment.html',$4,$5,$6)`, tid, node, a.SHA256, a.ContentType, a.Size, p.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Open(tid, a.SHA256, "original")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != doc {
		t.Fatal("original modified")
	}
	f, err = s.Open(tid, a.SHA256, "thumb")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil || img.Bounds().Dx() != 320 || img.Bounds().Dy() != 200 {
		t.Fatal("invalid inert thumbnail")
	}
	// Script/CSS payloads are not part of the rendition, including external
	// resource URLs. Execution and egress cannot occur in this pure renderer.
	one, err := htmlTextThumbnail(t.Context(), strings.NewReader(`<html><script>while(true){}</script><h1>Hello</h1></html>`))
	if err != nil {
		t.Fatal(err)
	}
	two, err := htmlTextThumbnail(t.Context(), strings.NewReader(`<html><script>fetch('http://localhost')</script><h1>Hello</h1></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("active content affected thumbnail")
	}
	fragment, err := s.StageNamed(t.Context(), tid, strings.NewReader("<section>fragment</section>"), "fragment.HTML")
	if err != nil {
		t.Fatal(err)
	}
	defer fragment.Close()
	if !isHTML(fragment.ContentType) {
		t.Fatal("named HTML fragment not accepted")
	}
	plain, err := s.StageNamed(t.Context(), tid, strings.NewReader("<section>fragment</section>"), "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if isHTML(plain.ContentType) {
		t.Fatal("plain file promoted to HTML")
	}
	for _, body := range []string{"<!doctype html><html>\xff</html>", "<html>\x00</html>", `<?xml version="1.0"?><svg onload="alert(1)"/>`} {
		if _, err := s.StageNamed(t.Context(), tid, strings.NewReader(body), "x.html"); err == nil {
			t.Fatal("invalid UTF-8 or XML MIME accepted as HTML")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := htmlTextThumbnail(ctx, strings.NewReader(doc)); err == nil {
		t.Fatal("cancelled rendition continued")
	}
	reader := &countingReader{Reader: strings.NewReader("<html><script>" + strings.Repeat("x", 128<<10) + "</script></html>")}
	if _, err := htmlTextThumbnail(t.Context(), reader); err != nil {
		t.Fatal(err)
	}
	if reader.n > 64<<10 {
		t.Fatal("thumbnail read beyond input bound")
	}
	// Both parts of a committed HTML attachment survive the orphan grace age.
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, variant := range []string{"original", "thumb"} {
		path, err := s.path(tid, a.SHA256, variant)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	report, err := GC(t.Context(), d.App, s, tid, true)
	if err != nil || report.Candidates != 0 || report.Removed != 0 {
		t.Fatalf("GC deleted live HTML bytes: %+v %v", report, err)
	}
}

type countingReader struct {
	io.Reader
	n int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += n
	return n, err
}

func TestHTMLPreviewCapabilities(t *testing.T) {
	d, p, node := setup(t)
	m := New(d.App, Store{FilesDir: t.TempDir()})
	m.Sandbox = NewSandbox(previewApp, previewOrigin, testLease(p))
	now := time.Now()
	m.Sandbox.now = func() time.Time { return now }
	mux := http.NewServeMux()
	m.Mount(mux)
	h := m.WrapSandbox(mux)
	body := []byte("<!doctype html><html><button onclick=\"this.textContent='yes'\">Try</button></html>")
	ct, data := multipartFile(t, "fragment.html", body)
	upload := request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, data)
	if upload.Code != 201 {
		t.Fatalf("upload status %d", upload.Code)
	}
	var items []Attachment
	if err := json.Unmarshal(upload.Body.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatal("invalid upload result")
	}
	a := items[0]
	if a.ThumbnailKind != "html-text" {
		t.Fatal("thumbnail metadata missing")
	}
	mint := func(actor tenant.Principal, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", previewApp+"/api/attachments/"+a.ID+"/preview", nil)
		r.Header.Set("Origin", origin)
		r = r.WithContext(tenant.WithPrincipal(t.Context(), actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	issue := func() string {
		t.Helper()
		w := mint(p, previewApp)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("mint status %d", w.Code)
		}
		var out struct {
			Available bool
			URL       string
			ExpiresAt time.Time `json:"expires_at"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Available || !out.ExpiresAt.Equal(now.Add(previewTTL)) {
			t.Fatal("invalid preview grant")
		}
		return out.URL
	}
	load := func(url string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	preview := issue()
	w := load(preview, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("preview status %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "no-store, private" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("unsafe response headers")
	}
	if w.Header().Get("Content-Security-Policy") != sandboxCSP+"; frame-ancestors "+previewApp {
		t.Fatal("sandbox policy missing")
	}
	for _, headers := range []map[string]string{{"Cookie": "fixture=1"}, {"Authorization": "Bearer fixture"}, {"Service-Worker": "script"}} {
		if load(preview, headers).Code != 404 {
			t.Fatal("sandbox accepted ambient credential or service worker")
		}
	}
	for _, altered := range []string{preview + "?token=x", preview + "/thumb", previewOrigin + "/preview/" + strings.Repeat("x", 43)} {
		if load(altered, nil).Code != 404 {
			t.Fatal("forged or widened capability accepted")
		}
	}
	for _, origin := range []string{"null", "https://other.example.org", ""} {
		if mint(p, origin).Code != 403 {
			t.Fatal("cross-origin capability mint allowed")
		}
	}
	// Original HTML still downloads under a script-denying CSP on the app.
	w = request(t, mux, p, "GET", "/api/attachments/"+a.ID+"/content", "", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatal("original HTML executed on app origin")
	}
	w = request(t, mux, p, "GET", "/api/attachments/"+a.ID+"/content?variant=thumb", "", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatal("inert thumbnail unavailable")
	}
	if request(t, mux, p, "GET", "/api/attachments/"+a.ID+"/content?variant=preview", "", nil).Code != 404 {
		t.Fatal("HTML executable preview on app origin")
	}
	for i := 1; i < maxPrincipalGrants; i++ {
		issue()
	}
	if mint(p, previewApp).Code != 429 {
		t.Fatal("per-person capability bound ignored")
	}
	now = now.Add(previewTTL)
	if load(preview, nil).Code != 404 {
		t.Fatal("expired grant accepted")
	}
	preview = issue()
	if _, err := d.Admin.Exec(t.Context(), `UPDATE attachments SET deleted_at=now(),updated_at=now() WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if load(preview, nil).Code != 404 {
		t.Fatal("deleted attachment still accessible")
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE attachments SET deleted_at=NULL WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if load(preview, nil).Code != 404 {
		t.Fatal("undo resurrected capability")
	}
	preview = issue()
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, node); err != nil {
		t.Fatal(err)
	}
	if load(preview, nil).Code != 404 {
		t.Fatal("deleted node still accessible")
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, node); err != nil {
		t.Fatal(err)
	}
	preview = issue()
	if _, err := d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if load(preview, nil).Code != 404 {
		t.Fatal("revoked permission still accessible")
	}
	// Restart drops all capabilities, including otherwise valid ones.
	m.Sandbox = NewSandbox(previewApp, previewOrigin, testLease(p))
	if load(preview, nil).Code != 404 {
		t.Fatal("restart retained capability")
	}
}

func TestHTMLPreviewProjectIsolationAndRaces(t *testing.T) {
	d, p, node := setup(t)
	var project, other string
	for i, target := range []*string{&project, &other} {
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, p.TenantID, []string{"PA-1", "PB-1"}[i]).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, project, node); err != nil {
		t.Fatal(err)
	}
	s := Store{FilesDir: t.TempDir()}
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		blob, err := s.Put(t.Context(), tx, p.TenantID, OwnerAttachment, strings.NewReader("<html>Bound content</html>"))
		if err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,$3,'fragment.html',$4,$5,$6) RETURNING id::text`, p.TenantID, node, blob.SHA256, blob.ContentType, blob.Size, p.ID).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(d.App, s)
	a, proj, err := m.previewAttachment(t.Context(), p, id)
	if err != nil || proj != project {
		t.Fatalf("read bound project: %v", err)
	}
	// Drop workspace membership; allow only project A.
	if _, err := d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, p.TenantID, p.ID, project); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.previewAttachment(t.Context(), p, id); err != nil {
		t.Fatalf("project guest refused: %v", err)
	}
	foreign := tenant.Principal{Kind: tenant.Person}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('preview-other','Other') RETURNING id::text`).Scan(&foreign.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other reader',ARRAY['member']) RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='member'`, foreign.TenantID, foreign.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.previewAttachment(t.Context(), foreign, id); err == nil {
		t.Fatal("second tenant read content")
	}
	// Follow the tree's child-first deletion invariant.
	for _, deleted := range []string{node, project} {
		if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := m.previewAttachment(t.Context(), p, id); err == nil {
		t.Fatal("deleted project still readable")
	}
	for _, restored := range []string{project, node} {
		if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, restored); err != nil {
			t.Fatal(err)
		}
	}
	// A deterministic barrier revokes project access while redemption is
	// paused in the session verifier. The subsequent RLS/authz read must deny.
	entered, resume := make(chan struct{}), make(chan struct{})
	m.Sandbox = NewSandbox(previewApp, previewOrigin, testLease(p))
	token := strings.Repeat("A", 43)
	m.Sandbox.grants[sha256.Sum256([]byte(token))] = previewGrant{principal: p, attachment: a, project: project, expires: time.Now().Add(time.Minute), lease: func(context.Context) (tenant.Principal, error) { close(entered); <-resume; return p, nil }}
	w := httptest.NewRecorder()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.WrapSandbox(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", previewOrigin+"/preview/"+token, nil))
	}()
	<-entered
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, other, node); err != nil {
		close(resume)
		wg.Wait()
		t.Fatal(err)
	}
	close(resume)
	wg.Wait()
	if w.Code != 404 {
		t.Fatal("project move during redemption disclosed bytes")
	}
	if _, _, err := m.previewAttachment(t.Context(), p, id); err == nil {
		t.Fatal("unauthorized same-tenant project read")
	}
	// Digest corruption never serves mutable replacement bytes.
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, project, node); err != nil {
		t.Fatal(err)
	}
	m.Sandbox.grants[sha256.Sum256([]byte(token))] = previewGrant{principal: p, attachment: a, project: project, expires: time.Now().Add(time.Minute), lease: func(context.Context) (tenant.Principal, error) { return p, nil }}
	path, _ := s.path(p.TenantID, a.SHA256, "original")
	if err := writeAtomic(path, strings.NewReader(strings.Repeat("x", int(a.Size)))); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	m.WrapSandbox(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", previewOrigin+"/preview/"+token, nil))
	if w.Code != 404 {
		t.Fatal("digest mismatch served")
	}
}

func TestHTMLPreviewDisabledAndSessionRevoked(t *testing.T) {
	d, p, node := setup(t)
	m := New(d.App, Store{FilesDir: t.TempDir()})
	mux := http.NewServeMux()
	m.Mount(mux)
	ct, data := multipartFile(t, "x.html", []byte("<html>local</html>"))
	w := request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, data)
	var items []Attachment
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &items) != nil {
		t.Fatal("upload failed")
	}
	w = request(t, mux, p, "POST", "/api/attachments/"+items[0].ID+"/preview", "", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"available":false}` {
		t.Fatal("missing origin did not fail closed")
	}
	m.Sandbox = NewSandbox(previewApp, previewOrigin, testLease(p))
	a, project, err := m.previewAttachment(t.Context(), p, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	m.Sandbox.grants[sha256.Sum256([]byte(token))] = previewGrant{principal: p, attachment: a, project: project, expires: time.Now().Add(time.Minute), lease: func(context.Context) (tenant.Principal, error) { return tenant.Principal{}, pgx.ErrNoRows }}
	w = httptest.NewRecorder()
	m.WrapSandbox(mux).ServeHTTP(w, httptest.NewRequest("GET", previewOrigin+"/preview/"+token, nil))
	if w.Code != 404 {
		t.Fatal("revoked session kept preview")
	}
}

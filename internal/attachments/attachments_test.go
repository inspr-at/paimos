// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

func setup(t *testing.T) (*dbtest.DB, tenant.Principal, string) {
	t.Helper()
	d := dbtest.Open(t)
	var p tenant.Principal
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('att','Attachments') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Tester',ARRAY['member']) RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		var kind, node string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug='work'`, p.TenantID).Scan(&kind); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) VALUES($1,'ATT-1',$2,'Ticket') RETURNING id::text`, p.TenantID, kind).Scan(&node); err != nil {
			return err
		}
		p.Name = node
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, p.TenantID, p.ID)
	p.Kind = tenant.Person
	p.Roles = []string{"member"}
	return d, p, p.Name
}
func request(t *testing.T, h http.Handler, p tenant.Principal, method, path, ct string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body).WithContext(tenant.WithPrincipal(t.Context(), p))
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAttachmentRoutesRejectCustomersAndAgents(t *testing.T) {
	mux := http.NewServeMux()
	New(nil, Store{}).Mount(mux)
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/nodes/" + id + "/attachments"},
		{"POST", "/api/nodes/" + id + "/attachments"},
		{"PATCH", "/api/attachments/" + id},
		{"DELETE", "/api/attachments/" + id},
		{"GET", "/api/attachments/" + id + "/content"},
	} {
		for _, actor := range []tenant.Principal{
			{ID: id, TenantID: id, Kind: tenant.Person, Roles: []string{"customer"}},
			{ID: id, TenantID: id, Kind: tenant.Agent, Roles: []string{"admin"}},
		} {
			rec := request(t, mux, actor, route.method, route.path, "", nil)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: got %d", route.method, route.path, actor.Kind, rec.Code)
			}
		}
	}
}
func fixturePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 640, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 640; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 20, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestManifestConstructor(t *testing.T) {
	if _, err := plugins.Builtin(Plugin); err != nil {
		t.Fatal(err)
	}
}
func multipartFile(t *testing.T, name string, body []byte) (string, *bytes.Buffer) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	p, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), &b
}
func TestUploadDedupeVariantsETagIsolationUndoAndOps(t *testing.T) {
	d, p, node := setup(t)
	store := Store{FilesDir: t.TempDir(), MaxSize: 1 << 20}
	m := New(d.App, store)
	mux := http.NewServeMux()
	m.Mount(mux)
	events.New(d.App, events.WithUndoHandlers(UndoHandlers())).Mount(mux)
	body := fixturePNG(t)
	ct, b := multipartFile(t, "../image.png", body)
	w := request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, b)
	if w.Code != 201 {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	var items []Attachment
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatalf("upload JSON %v %s", err, w.Body.String())
	}
	a := items[0]
	if a.Name != "image.png" || a.Width == nil || *a.Width != 640 || a.Height == nil || *a.Height != 400 {
		t.Fatalf("attachment %+v", a)
	}
	for _, v := range []string{"original", "thumb", "preview"} {
		f, err := store.Open(p.TenantID, a.SHA256, v)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if v == "thumb" && cfg.Width > 320 {
			t.Fatalf("thumb width %d", cfg.Width)
		}
	}
	ct, b = multipartFile(t, "copy.png", body)
	w = request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, b)
	if w.Code != 201 {
		t.Fatalf("dedupe upload %d %s", w.Code, w.Body.String())
	}
	var copyItems []Attachment
	_ = json.Unmarshal(w.Body.Bytes(), &copyItems)
	if len(copyItems) != 1 || copyItems[0].SHA256 != a.SHA256 || copyItems[0].ID == a.ID {
		t.Fatal("dedupe metadata/content mismatch")
	}
	w = request(t, mux, p, "GET", "/api/attachments/"+a.ID+"/content?variant=thumb", "", nil)
	if w.Code != 200 || w.Header().Get("ETag") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("content %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); csp != "" {
		t.Fatalf("image content CSP %q", csp)
	}
	etag := w.Header().Get("ETag")
	r := httptest.NewRequest("GET", "/api/attachments/"+a.ID+"/content?variant=thumb", nil).WithContext(tenant.WithPrincipal(t.Context(), p))
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 304 {
		t.Fatalf("cache %d", w.Code)
	}
	var foreign tenant.Principal
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('other','Other') RETURNING id::text`).Scan(&foreign.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, foreign.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other',ARRAY['member']) RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID)
	}); err != nil {
		t.Fatal(err)
	}
	foreign.Kind = tenant.Person
	foreign.Roles = []string{"member"}
	dbtest.BindLegacy(t, d, foreign.TenantID, foreign.ID)
	w = request(t, mux, foreign, "GET", "/api/attachments/"+a.ID+"/content", "", nil)
	if w.Code != 404 {
		t.Fatalf("cross tenant %d", w.Code)
	}
	w = request(t, mux, p, "DELETE", "/api/attachments/"+a.ID, "", nil)
	if w.Code != 204 {
		t.Fatalf("delete %d %s", w.Code, w.Body.String())
	}
	w = request(t, mux, p, "GET", "/api/attachments/"+a.ID+"/content", "", nil)
	if w.Code != 404 {
		t.Fatalf("deleted content %d", w.Code)
	}
	var eventID int64
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE tenant_id=$1 AND type='attachment.removed'`, p.TenantID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	w = request(t, mux, p, "POST", "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", "", nil)
	if w.Code != 201 {
		t.Fatalf("undo %d %s", w.Code, w.Body.String())
	}
	w = request(t, mux, p, "GET", "/api/attachments/"+a.ID+"/content", "", nil)
	if w.Code != 200 {
		t.Fatalf("restored %d", w.Code)
	}
	report, err := Verify(t.Context(), d.App, store, p.TenantID)
	if err != nil || report.Checked != 1 || len(report.Issues) != 0 {
		t.Fatalf("verify %+v %v", report, err)
	}
	thumbPath, _ := store.path(p.TenantID, a.SHA256, "thumb")
	if err := os.WriteFile(thumbPath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = Verify(t.Context(), d.App, store, p.TenantID)
	if err != nil || len(report.Issues) != 1 || report.Issues[0].Problem != "corrupt" || report.Issues[0].Variant != "thumb" {
		t.Fatalf("corrupt verify %+v %v", report, err)
	}
	orphan := strings.Repeat("a", 64)
	orphanPath, _ := store.path(p.TenantID, orphan, "original")
	if err := os.MkdirAll(filepath.Dir(orphanPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(orphanPath, old, old); err != nil {
		t.Fatal(err)
	}
	gc, err := GC(t.Context(), d.App, store, p.TenantID, false)
	if err != nil || gc.Candidates != 1 || gc.Removed != 0 {
		t.Fatalf("dry GC %+v %v", gc, err)
	}
	gc, err = GC(t.Context(), d.App, store, p.TenantID, true)
	if err != nil || gc.Removed != 1 {
		t.Fatalf("GC %+v %v", gc, err)
	}
}
func TestLimitSniffAndPatchPrecondition(t *testing.T) {
	d, p, node := setup(t)
	store := Store{FilesDir: t.TempDir(), MaxSize: 100}
	m := New(d.App, store)
	mux := http.NewServeMux()
	m.Mount(mux)
	ct, b := multipartFile(t, "oversized.txt", bytes.Repeat([]byte("x"), 101))
	w := request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, b)
	if w.Code != 413 {
		t.Fatalf("limit %d %s", w.Code, w.Body.String())
	}
	// A file named .png that isn't one is kept as an opaque binary: the type
	// comes from sniffing, never from the name, and it is served as a download.
	ct, b = multipartFile(t, "fake.png", []byte{0, 1, 2, 3, 4})
	w = request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, b)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"content_type":"application/octet-stream"`) {
		t.Fatalf("sniff %d %s", w.Code, w.Body.String())
	}
	var fake []Attachment
	if err := json.Unmarshal(w.Body.Bytes(), &fake); err != nil || len(fake) != 1 {
		t.Fatalf("sniff body %v %s", err, w.Body.String())
	}
	w = request(t, mux, p, "GET", "/api/attachments/"+fake[0].ID+"/content", "", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatalf("opaque download %d disposition %q csp %q", w.Code, w.Header().Get("Content-Disposition"), w.Header().Get("Content-Security-Policy"))
	}
	ct, b = multipartFile(t, "active.pdf", []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\n"))
	w = request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, b)
	if w.Code != 201 {
		t.Fatalf("pdf upload %d %s", w.Code, w.Body.String())
	}
	var pdf []Attachment
	if err := json.Unmarshal(w.Body.Bytes(), &pdf); err != nil || len(pdf) != 1 {
		t.Fatalf("pdf body %v %s", err, w.Body.String())
	}
	w = request(t, mux, p, "GET", "/api/attachments/"+pdf[0].ID+"/content", "", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("pdf served without download isolation: %d %q", w.Code, w.Header().Get("Content-Disposition"))
	}
	ct, b = multipartFile(t, "note.txt", []byte("hello"))
	w = request(t, mux, p, "POST", "/api/nodes/"+node+"/attachments", ct, b)
	if w.Code != 201 {
		t.Fatalf("text %d %s", w.Code, w.Body.String())
	}
	var items []Attachment
	_ = json.Unmarshal(w.Body.Bytes(), &items)
	id := items[0].ID
	r := httptest.NewRequest("PATCH", "/api/attachments/"+id, strings.NewReader(`{"caption":"new"}`)).WithContext(tenant.WithPrincipal(t.Context(), p))
	r.Header.Set("If-Unmodified-Since", time.Now().Add(-time.Hour).Format(time.RFC3339Nano))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 412 {
		t.Fatalf("precondition %d %s", w.Code, w.Body.String())
	}
	w = request(t, mux, p, "PATCH", "/api/attachments/"+id, "application/json", strings.NewReader(`{"caption":"new","position":"2.5"}`))
	if w.Code != 200 {
		t.Fatalf("patch %d %s", w.Code, w.Body.String())
	}
}

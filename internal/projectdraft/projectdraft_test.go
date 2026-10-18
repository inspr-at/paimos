// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fixture struct {
	d               *dbtest.DB
	m               *Module
	handler         http.Handler
	tenant          string
	a, b, foreign   tenant.Principal
	files           attachments.Store
	payloadForLeaks []string
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.Open(t)
	f := fixture{d: d, files: attachments.Store{FilesDir: t.TempDir()}}
	tenants := [2]string{}
	for i := range tenants {
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Test') RETURNING id::text`, fmt.Sprint("t", i)).Scan(&tenants[i]); err != nil {
			t.Fatal(err)
		}
	}
	f.tenant = tenants[0]
	person := func(tenantID, name string) tenant.Principal {
		p := tenant.Principal{TenantID: tenantID, Kind: tenant.Person, Name: name, Roles: []string{"member"}}
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person',$2,'{member}') RETURNING id::text`, tenantID, name).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindLegacy(t, d, tenantID, p.ID)
		return p
	}
	f.a, f.b, f.foreign = person(tenants[0], "Lena Huber"), person(tenants[0], "Jonas Berger"), person(tenants[1], "Otto Other")
	f.m = New(Options{Pool: d.App, TenantID: f.tenant, Store: f.files, MacKey: bytes.Repeat([]byte{7}, 32)})
	f.handler = (&httpapi.Server{Pool: d.App, Modules: []httpapi.Module{f.m}}).Handler()
	return f
}

func handoffDoc() map[string]any {
	doc := validDoc()
	doc["summary"] = "Bäckerei mit [Link](https://evil.example) und # Überschrift"
	doc["open_points"] = []any{"Online-Zahlung schon zum Start oder erst später?", "Tagesliste für die Backstube: Ausdruck oder Tablet?", "Abholerinnerung per SMS oder per E-Mail?"}
	return doc
}

func (f fixture) post(t *testing.T, contentType, body, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/import/project-draft", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	// A session cookie must not matter: the public route never reads it.
	r.Header.Set("Cookie", "aeon_session=not-a-session")
	if remote != "" {
		r.RemoteAddr = remote
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f fixture) handoff(t *testing.T, doc map[string]any) string {
	t.Helper()
	w := f.post(t, "application/x-www-form-urlencoded", url.Values{"payload": {string(encodeDoc(t, doc))}}.Encode(), "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("handoff status %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	fragment, ok := strings.CutPrefix(location, "/new-project#")
	values, err := url.ParseQuery(fragment)
	capability := values.Get("draft")
	if !ok || err != nil || !capabilityPattern.MatchString(capability) || values.Get("lang") != "de" || len(values) != 2 {
		t.Fatalf("Location %q is not the assistant with a fragment capability", location)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("handoff headers %v", w.Header())
	}
	return capability
}

func (f fixture) call(t *testing.T, p tenant.Principal, method, path, capability string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	if body == nil {
		raw = nil
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set(DraftHeader, capability)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return out
}

func (f fixture) scalar(t *testing.T, q string, args ...any) string {
	t.Helper()
	var out string
	if err := f.d.Admin.QueryRow(t.Context(), q, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return out
}

func createBody(owner string, points ...int) map[string]any {
	return map[string]any{"name": "Vorbestell-App für die Bäckerei", "key": "VAFDB", "owner_id": owner, "target_date": "2026-11-30", "open_points": points}
}

// Risk: a public endpoint that stores conversations must refuse oversize,
// unknown-version and malformed handoffs with a page, and bound how fast and
// how much anyone can store, before reading more than it must.
func TestHandoffRefusals(t *testing.T) {
	f := setup(t)
	form := func(doc map[string]any) string {
		return url.Values{"payload": {string(encodeDoc(t, doc))}}.Encode()
	}
	page := func(w *httptest.ResponseRecorder, status int, text string) {
		t.Helper()
		expect(t, w, status)
		if !strings.Contains(w.Body.String(), text) || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("page for %d lacks %q or its policy: %s", status, text, w.Body.String())
		}
	}
	newer := handoffDoc()
	newer["version"] = 2
	page(f.post(t, "application/x-www-form-urlencoded", form(newer), "192.0.2.10:1"), http.StatusUnprocessableEntity, "newer START")
	broken := handoffDoc()
	broken["destination"] = "ppm"
	page(f.post(t, "application/x-www-form-urlencoded", form(broken), "192.0.2.10:1"), http.StatusBadRequest, "could not be read")
	page(f.post(t, "application/json", string(encodeDoc(t, handoffDoc())), "192.0.2.10:1"), http.StatusUnsupportedMediaType, "could not be read")
	page(f.post(t, "application/x-www-form-urlencoded", "other=1", "192.0.2.10:1"), http.StatusBadRequest, "could not be read")
	big := handoffDoc()
	big["summary"] = strings.Repeat("x", MaxPayloadBytes)
	page(f.post(t, "application/x-www-form-urlencoded", form(big), "192.0.2.11:1"), http.StatusRequestEntityTooLarge, "too large")
	if got := f.scalar(t, `SELECT count(*)::text FROM import_project_drafts`); got != "0" {
		t.Fatalf("refused handoffs stored %s drafts", got)
	}

	// The client limit counts attempts per address, before the body is read.
	f.m.perMinute = 2
	for i := 0; i < 2; i++ {
		expect(t, f.post(t, "application/x-www-form-urlencoded", form(handoffDoc()), "198.51.100.5:1"), http.StatusSeeOther)
	}
	limited := f.post(t, "application/x-www-form-urlencoded", form(handoffDoc()), "198.51.100.5:1")
	page(limited, http.StatusTooManyRequests, "Too many handoffs")
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("rate limit without Retry-After")
	}
	expect(t, f.post(t, "application/x-www-form-urlencoded", form(handoffDoc()), "198.51.100.6:1"), http.StatusSeeOther)

	// Live drafts are capped in count and bytes, whoever posts them.
	f.m.perMinute, f.m.liveDrafts = 20, 3
	page(f.post(t, "application/x-www-form-urlencoded", form(handoffDoc()), "198.51.100.7:1"), http.StatusTooManyRequests, "zu viele")
	f.m.liveDrafts, f.m.liveBytes = 20, 1
	page(f.post(t, "application/x-www-form-urlencoded", form(handoffDoc()), "198.51.100.8:1"), http.StatusTooManyRequests, "zu viele")
	if got := f.scalar(t, `SELECT count(*)::text FROM import_project_drafts`); got != "3" {
		t.Fatalf("caps let %s drafts in, want 3", got)
	}

	off := New(Options{Pool: f.d.App, MacKey: bytes.Repeat([]byte{7}, 32)})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/import/project-draft", strings.NewReader(form(handoffDoc())))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	(&httpapi.Server{Pool: f.d.App, Modules: []httpapi.Module{off}}).Handler().ServeHTTP(w, r)
	page(w, http.StatusServiceUnavailable, "not available")
}

// Risk: a draft that outlives its expiry, or one that is only deleted when
// someone happens to post again.
func TestDraftExpiry(t *testing.T) {
	f := setup(t)
	expired, live := f.handoff(t, handoffDoc()), f.handoff(t, handoffDoc())
	digest, _ := capabilityDigest(expired)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE import_project_drafts SET expires_at=clock_timestamp()-interval '1 second' WHERE capability_digest=$1`, digest); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(t, f.a, http.MethodGet, "/api/import/project-draft", expired, nil), http.StatusNotFound)
	expect(t, f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", expired, createBody(f.a.ID)), http.StatusNotFound)
	deleted, err := f.m.Sweep(t.Context())
	if err != nil || deleted != 1 {
		t.Fatalf("sweep deleted %d, %v; want exactly the expired draft", deleted, err)
	}
	expect(t, f.call(t, f.a, http.MethodGet, "/api/import/project-draft", live, nil), http.StatusOK)
}

// Risk: opening the assistant consumes the draft, a second person or tenant
// can take it, a retry creates twice, or the import skips a guard.
func TestCreateFromDraft(t *testing.T) {
	f := setup(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	capability := f.handoff(t, handoffDoc())

	for range 2 {
		view := decodeBody[View](t, expectOK(t, f.call(t, f.a, http.MethodGet, "/api/import/project-draft", capability, nil)))
		if view.Status != "ready" || view.Language != "de" || view.Source.Host != "start.example.com" || len(view.OpenPoints) != 3 || view.Messages != 2 {
			t.Fatalf("view %+v", view)
		}
	}
	if got := f.scalar(t, `SELECT (consumed_at IS NULL AND payload IS NOT NULL)::text FROM import_project_drafts`); got != "true" {
		t.Fatal("reading consumed the draft")
	}
	expect(t, f.call(t, f.foreign, http.MethodGet, "/api/import/project-draft", capability, nil), http.StatusNotFound)
	expect(t, f.call(t, tenant.Principal{TenantID: f.tenant, ID: f.a.ID, Kind: tenant.Agent}, http.MethodGet, "/api/import/project-draft", capability, nil), http.StatusForbidden)
	expect(t, f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, createBody("00000000-0000-4000-8000-000000000001", 0)), http.StatusUnprocessableEntity)
	expect(t, f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, createBody(f.b.ID, 7)), http.StatusUnprocessableEntity)

	w := f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, createBody(f.b.ID, 0, 2))
	expect(t, w, http.StatusCreated)
	made := decodeBody[Created](t, w)
	var project struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Fields struct {
			Key      string `json:"project_key"`
			Assignee string `json:"assignee"`
			EndDate  string `json:"end_date"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(made.Project, &project); err != nil || made.Tickets != 2 || made.Replayed || project.Fields.Key != "VAFDB" || project.Fields.Assignee != f.b.ID || project.Fields.EndDate != "2026-11-30" {
		t.Fatalf("created %+v / %+v (%v)", made, project, err)
	}
	work := f.scalar(t, `SELECT string_agg(title, '|' ORDER BY key) FROM nodes WHERE parent_id=$1 AND kind_id=(SELECT id FROM node_kinds WHERE tenant_id=$2 AND slug='work')`, project.ID, f.tenant)
	if work != "Online-Zahlung schon zum Start oder erst später?|Abholerinnerung per SMS oder per E-Mail?" {
		t.Fatalf("work items %q", work)
	}
	body := f.scalar(t, `SELECT n.title||'|'||n.body FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='memory' AND n.fields->>'slug'='from-start-conversation'`, project.ID)
	if !strings.HasPrefix(body, "Aus dem START-Gespräch|") || !strings.Contains(body, `\[Link\](https://evil.example) und # Überschrift`) || !strings.Contains(body, "## Was noch offen ist") {
		t.Fatalf("knowledge entry %q", body)
	}
	if got := f.scalar(t, `SELECT name||'|'||content_type FROM attachments WHERE node_id=$1`, project.ID); got != "start-conversation-start.example.com.txt|text/plain; charset=utf-8" {
		t.Fatalf("transcript attachment %q", got)
	}
	if got := f.scalar(t, `SELECT string_agg(type, ',' ORDER BY id) FROM events WHERE tenant_id=$1 AND metadata->>'imported_from'='start'`, f.tenant); got != "node.created,node.created,node.created,knowledge.created,attachment.added" {
		t.Fatalf("events %q", got)
	}
	if got := f.scalar(t, `SELECT (payload IS NULL AND consumed_by=$1::uuid)::text FROM import_project_drafts`, f.a.ID); got != "true" {
		t.Fatal("draft not consumed by its creator, or payload kept")
	}

	// The second person gets the one gone answer, for reading and creating.
	expect(t, f.call(t, f.b, http.MethodGet, "/api/import/project-draft", capability, nil), http.StatusNotFound)
	expect(t, f.call(t, f.b, http.MethodPost, "/api/import/project-draft/create", capability, createBody(f.b.ID)), http.StatusNotFound)
	// A retry by the creator answers with the same project, never a second one.
	retry := f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, createBody(f.a.ID, 1))
	expect(t, retry, http.StatusOK)
	again := decodeBody[Created](t, retry)
	if !again.Replayed || again.Tickets != 2 || !strings.Contains(string(again.Project), project.ID) {
		t.Fatalf("retry %+v", again)
	}
	if got := f.scalar(t, `SELECT count(*)::text FROM nodes WHERE fields->>'project_key'='VAFDB'`); got != "1" {
		t.Fatalf("%s projects for one draft", got)
	}
	view := decodeBody[View](t, expectOK(t, f.call(t, f.a, http.MethodGet, "/api/import/project-draft", capability, nil)))
	if view.Status != "created" || !strings.Contains(string(view.Project), project.ID) {
		t.Fatalf("creator's view after create %+v", view)
	}

	// Neither the capability nor anything from the conversation reaches a log.
	for _, secret := range []string{capability, "Vorbestell", "Bäckerei", "Online-Zahlung", "0f4c2a1e-7d1b-4c55-9a0e-2b8f6d3c1a90"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log contains %q", secret)
		}
	}
	if !strings.Contains(logs.String(), `"path":"POST /import/project-draft"`) {
		t.Fatalf("request log missing the route pattern: %s", logs.String())
	}
}

func expectOK(t *testing.T, w *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	expect(t, w, http.StatusOK)
	return w
}

// Risk: a create that fails late leaves a half project behind, or uses up the
// draft. Every write is one transaction; a refused key leaves the draft too.
func TestCreateRollsBackAsOneTransaction(t *testing.T) {
	f := setup(t)
	capability := f.handoff(t, handoffDoc())
	for _, stmt := range []string{
		`CREATE FUNCTION fail_import_attachment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected attachment failure'; END $$`,
		`CREATE TRIGGER fail_import_attachment BEFORE INSERT ON attachments FOR EACH ROW EXECUTE FUNCTION fail_import_attachment()`,
	} {
		if _, err := f.d.Admin.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, createBody(f.a.ID, 0, 1, 2)), http.StatusInternalServerError)
	for q, want := range map[string]string{
		`SELECT count(*)::text FROM nodes WHERE key LIKE 'VAFDB-%' OR fields->>'project_key'='VAFDB'`: "0",
		`SELECT count(*)::text FROM events WHERE metadata->>'imported_from'='start'`:                  "0",
		`SELECT (consumed_at IS NULL AND payload IS NOT NULL)::text FROM import_project_drafts`:       "true",
	} {
		if got := f.scalar(t, q); got != want {
			t.Fatalf("after the failed create %s = %s, want %s", q, got, want)
		}
	}
	if _, err := f.d.Admin.Exec(t.Context(), `DROP TRIGGER fail_import_attachment ON attachments`); err != nil {
		t.Fatal(err)
	}
	// A key another project holds is refused before anything is written.
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,fields) SELECT $1,'VAFDB-1',id,'Elsewhere','{"project_key":"VAFDB"}' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, f.tenant)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, createBody(f.a.ID, 0)), http.StatusConflict)
	ok := createBody(f.a.ID, 0)
	ok["key"] = "BAECK"
	expect(t, f.call(t, f.a, http.MethodPost, "/api/import/project-draft/create", capability, ok), http.StatusCreated)
}

// Risk: two people create from one draft at the same moment and both get a
// project. The first create holds the draft row; the second waits on the
// access-change fence and then gets the one gone answer.
func TestConcurrentCreatesConsumeOnce(t *testing.T) {
	f := setup(t)
	capability := f.handoff(t, handoffDoc())
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(q string) bool {
		return strings.Contains(q, "FOR NO KEY UPDATE") && strings.Contains(q, "import_project_drafts")
	})
	held := New(Options{Pool: pool, TenantID: f.tenant, Store: f.files, MacKey: bytes.Repeat([]byte{7}, 32)})
	heldHandler := (&httpapi.Server{Pool: pool, Modules: []httpapi.Module{held}}).Handler()
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/api/import/project-draft/create", bytes.NewReader(mustJSON(createBody(f.a.ID, 0))))
		r.Header.Set(DraftHeader, capability)
		r = r.WithContext(tenant.WithPrincipal(context.WithoutCancel(ctx), f.a))
		w := httptest.NewRecorder()
		heldHandler.ServeHTTP(w, r)
		first <- w
	}()
	holder := barrier.Wait(t, ctx)
	second, done := make(chan *httptest.ResponseRecorder, 1), make(chan struct{})
	go func() {
		second <- f.call(t, f.b, http.MethodPost, "/api/import/project-draft/create", capability, createBody(f.b.ID, 0))
		close(done)
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, holder, done); lock == "" {
		t.Fatal("the second create finished while the first held the draft")
	}
	barrier.Release()
	expect(t, dbtest.Await(t, ctx, first), http.StatusCreated)
	expect(t, dbtest.Await(t, ctx, second), http.StatusNotFound)
	if got := f.scalar(t, `SELECT count(*)::text FROM nodes WHERE fields->>'project_key'='VAFDB'`); got != "1" {
		t.Fatalf("%s projects from one draft", got)
	}
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

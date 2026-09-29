// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestPresentationInputNormalize(t *testing.T) {
	ok := releasehistory.PresentationInput{ThemeEN: "  The version stays visible ", HeadlineEN: "The menu names the update.", IntroDE: "Zeile eins.\nZeile zwei."}
	got, err := ok.Normalize()
	if err != nil || got.ThemeEN != "The version stays visible" {
		t.Fatalf("normalize: %+v %v", got, err)
	}
	neg := -1
	for name, in := range map[string]releasehistory.PresentationInput{
		"missing theme":     {HeadlineEN: "x"},
		"missing headline":  {ThemeEN: "x"},
		"blank headline":    {ThemeEN: "x", HeadlineEN: "   "},
		"long theme":        {ThemeEN: strings.Repeat("a", releasehistory.MaxTheme+1), HeadlineEN: "x"},
		"long intro":        {ThemeEN: "x", HeadlineEN: "x", IntroEN: strings.Repeat("ä", releasehistory.MaxIntro+1)},
		"two-line headline": {ThemeEN: "x", HeadlineEN: "one\ntwo"},
		"control":           {ThemeEN: "x\x07", HeadlineEN: "x"},
		"negative revision": {ThemeEN: "x", HeadlineEN: "x", ExpectedRevision: &neg},
	} {
		if _, err := in.Normalize(); !errors.Is(err, releasehistory.ErrPresentationInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Exactly at the limit, counted in characters, not bytes.
	if _, err := (releasehistory.PresentationInput{ThemeEN: strings.Repeat("ü", releasehistory.MaxTheme), HeadlineEN: "x"}).Normalize(); err != nil {
		t.Fatal(err)
	}
}

type presentationFixture struct {
	db       *dbtest.DB
	admin    tenant.Principal
	member   tenant.Principal
	stranger tenant.Principal
	project  string
}

func newPresentationFixture(t *testing.T) presentationFixture {
	t.Helper()
	database := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	f := presentationFixture{db: database}
	mk := func(slug string) (tenantID, admin, member, project string) {
		tenantID, err := tenantbootstrap.Create(ctx, database.App, slug, slug)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.InTenant(ctx, database.App, tenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Admin') RETURNING id::text`, tenantID).Scan(&admin); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Member') RETURNING id::text`, tenantID).Scan(&member); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1,id,'PRJ-1','Aeon','{"project_key":"AEON"}'::jsonb FROM node_kinds WHERE slug='project' RETURNING id::text`, tenantID).Scan(&project)
		}); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, database, tenantID, admin, "admin")
		dbtest.BindRole(t, database, tenantID, member, "member")
		return
	}
	tenantID, admin, member, project := mk("present-a")
	f.admin = tenant.Principal{ID: admin, TenantID: tenantID, Kind: tenant.Person}
	f.member = tenant.Principal{ID: member, TenantID: tenantID, Kind: tenant.Person}
	f.project = project
	otherTenant, otherAdmin, _, _ := mk("present-b")
	f.stranger = tenant.Principal{ID: otherAdmin, TenantID: otherTenant, Kind: tenant.Person}
	return f
}

func (f presentationFixture) events(t *testing.T, tenantID, kind string) int {
	t.Helper()
	var n int
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type=$1`, kind).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPresentationAPIWritesEventsAndIsServedPerTenant(t *testing.T) {
	f := newPresentationFixture(t)
	const version = "260929082208.0.0"
	history := releasehistory.History{Schema: releasehistory.Schema, Releases: []releasehistory.Release{{Version: version, Tag: "v" + version, State: releasehistory.StatePublished, Headline: "stable102", Tickets: []string{}, Changes: []releasehistory.Change{}}}}
	mux := http.NewServeMux()
	releasehistory.NewWith(history, version).WithBackfills(f.db.App, "AEON").Mount(mux)
	call := func(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(tenant.WithPrincipal(t.Context(), p)))
		return w
	}
	presentationOf := func(p tenant.Principal) *releasehistory.Presentation {
		t.Helper()
		w := call(p, "GET", "/api/releases/v"+version, "")
		if w.Code != http.StatusOK {
			t.Fatalf("get: %d %s", w.Code, w.Body)
		}
		var rel releasehistory.Release
		if err := json.Unmarshal(w.Body.Bytes(), &rel); err != nil {
			t.Fatal(err)
		}
		return rel.Presentation
	}
	body := `{"theme_en":"The version stays visible","theme_de":"Die Version bleibt sichtbar","headline_en":"The menu names the update and what it is doing.","headline_de":"Das Menü nennt das Update.","intro_en":"Under the version, a status line stays.","intro_de":""}`

	if w := call(f.member, "PUT", "/api/releases/"+version+"/presentation", body); w.Code != http.StatusForbidden {
		t.Fatalf("member: %d %s", w.Code, w.Body)
	}
	if presentationOf(f.admin) != nil {
		t.Fatal("presentation before any write")
	}
	w := call(f.admin, "PUT", "/api/releases/"+version+"/presentation", body)
	var change releasehistory.PresentationChange
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &change) != nil || !change.Changed || change.Before != nil || change.After == nil || change.After.Revision != 1 {
		t.Fatalf("first write: %d %s", w.Code, w.Body)
	}
	got := presentationOf(f.admin)
	if got == nil || got.ThemeEN != "The version stays visible" || got.HeadlineDE != "Das Menü nennt das Update." || got.Revision != 1 {
		t.Fatalf("served: %+v", got)
	}
	// The list carries it too; members read it, other tenants never see it.
	if presentationOf(f.member) == nil {
		t.Fatal("member cannot read the presentation")
	}
	if presentationOf(f.stranger) != nil {
		t.Fatal("presentation leaked into another tenant")
	}
	lw := call(f.admin, "GET", "/api/releases", "")
	if !strings.Contains(lw.Body.String(), `"presentation":{"theme_en":"The version stays visible"`) {
		t.Fatalf("list lacks presentation: %s", lw.Body)
	}
	if n := f.events(t, f.admin.TenantID, releasehistory.PresentationSetEvent); n != 1 {
		t.Fatalf("events after first write: %d", n)
	}

	// Identical text: nothing changes, no event.
	if w := call(f.admin, "PUT", "/api/releases/v"+version+"/presentation", body); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":false`) {
		t.Fatalf("same write: %d %s", w.Code, w.Body)
	}
	if n := f.events(t, f.admin.TenantID, releasehistory.PresentationSetEvent); n != 1 {
		t.Fatalf("no-op wrote an event: %d", n)
	}
	// A stale guard conflicts; the right one edits and bumps the revision.
	edit := strings.Replace(body, `"intro_de":""`, `"intro_de":"Unter der Version bleibt eine Statuszeile.","expected_revision":%d`, 1)
	if w := call(f.admin, "PUT", "/api/releases/"+version+"/presentation", strings.Replace(edit, "%d", "0", 1)); w.Code != http.StatusConflict {
		t.Fatalf("stale guard: %d %s", w.Code, w.Body)
	}
	if w := call(f.admin, "PUT", "/api/releases/"+version+"/presentation", strings.Replace(edit, "%d", "1", 1)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"revision":2`) {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if n := f.events(t, f.admin.TenantID, releasehistory.PresentationSetEvent); n != 2 {
		t.Fatalf("events after edit: %d", n)
	}
	var before, after map[string]any
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT before,after FROM events WHERE type=$1 ORDER BY id DESC LIMIT 1`, releasehistory.PresentationSetEvent).Scan(&before, &after)
	}); err != nil {
		t.Fatal(err)
	}
	if before["intro_de"] != "" || after["intro_de"] != "Unter der Version bleibt eine Statuszeile." || after["version"] != version {
		t.Fatalf("event before/after: %v / %v", before, after)
	}

	// Bad input and bad versions.
	for path, want := range map[string]int{
		"/api/releases/" + version + "/presentation": http.StatusUnprocessableEntity,
		"/api/releases/2609/presentation":            http.StatusBadRequest,
	} {
		if w := call(f.admin, "PUT", path, `{"theme_en":"","headline_en":"x"}`); w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := call(f.admin, "PUT", "/api/releases/"+version+"/presentation", `{"theme_en":"x","headline_en":"x","extra":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}

	// Clearing records one event; clearing again records none.
	if w := call(f.member, "DELETE", "/api/releases/"+version+"/presentation", ""); w.Code != http.StatusForbidden {
		t.Fatalf("member delete: %d", w.Code)
	}
	if w := call(f.admin, "DELETE", "/api/releases/"+version+"/presentation", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	if w := call(f.admin, "DELETE", "/api/releases/"+version+"/presentation", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":false`) {
		t.Fatalf("clear again: %d %s", w.Code, w.Body)
	}
	if n := f.events(t, f.admin.TenantID, releasehistory.PresentationClearedEvent); n != 1 {
		t.Fatalf("clear events: %d", n)
	}
	if presentationOf(f.admin) != nil {
		t.Fatal("presentation survived clear")
	}
}

func TestPresentationRowsAreTenantIsolated(t *testing.T) {
	f := newPresentationFixture(t)
	ctx := tenant.WithPrincipal(dbtest.Seed(t.Context()), f.admin)
	if err := db.InTenant(ctx, f.db.App, f.admin.TenantID, func(tx pgx.Tx) error {
		_, err := releasehistory.SavePresentation(ctx, tx, f.admin, f.project, "260101120000.0.0", releasehistory.PresentationInput{ThemeEN: "t", HeadlineEN: "h"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Another tenant's transaction neither sees nor can write that project's rows.
	sctx := tenant.WithPrincipal(dbtest.Seed(t.Context()), f.stranger)
	err := db.InTenant(sctx, f.db.App, f.stranger.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(sctx, `SELECT count(*) FROM release_presentations`).Scan(&n); err != nil || n != 0 {
			t.Errorf("stranger sees %d rows (%v)", n, err)
		}
		_, err := tx.Exec(sctx, `INSERT INTO release_presentations(tenant_id,project_node_id,version,theme_en,headline_en,updated_by) VALUES($1,$2,'260101130000.0.0','t','h',$3)`, f.admin.TenantID, f.project, f.stranger.ID)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant insert succeeded")
	}
}

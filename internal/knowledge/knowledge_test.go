// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestKnowledgeStreamCarriesAuthorizedProjectForArchiveDeleteAndUndo(t *testing.T) {
	f := setup(t)
	entry := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "stream-note", "title": "Stream note"})
	w := call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"status": "archived"})
	expect(t, w, 200)
	archived := decode[Entry](t, w)
	w = call(t, f, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", *archived.EventID), nil)
	expect(t, w, 201)
	restored := decode[events.Event](t, w)
	w = call(t, f, f.a, "DELETE", "/api/knowledge/"+entry.ID, nil)
	expect(t, w, 200)
	deleted := decode[struct {
		EventID int64 `json:"event_id"`
	}](t, w)
	w = call(t, f, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", deleted.EventID), nil)
	expect(t, w, 201)
	undeleted := decode[events.Event](t, w)
	// Another tenant's Knowledge event cannot enter this reader's stream.
	createEntry(t, f, f.foreign, map[string]any{"project_id": f.elsewhere, "type": "runbook", "slug": "foreign-note", "title": "Foreign note"})
	wants := []struct {
		id     int64
		change string
		undoOf *int64
	}{
		{*entry.EventID, "created", nil}, {*archived.EventID, "updated", nil},
		{restored.ID, "updated", archived.EventID}, {deleted.EventID, "deleted", nil}, {undeleted.ID, "created", &deleted.EventID},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handler.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.b)))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream?after="+strconv.FormatInt(*entry.EventID-1, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("stream status %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	for _, want := range wants {
		found := false
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "" {
				continue
			} // stream.ready carries no resource event
			if event.ID != want.id || len(event.NodeChanges) != 1 {
				t.Fatalf("missing Knowledge event/project summary: id=%d type=%s changes=%+v; want %d", event.ID, event.Type, event.NodeChanges, want.id)
			}
			change := event.NodeChanges[0]
			if change.ID != entry.ID || change.ProjectID == nil || *change.ProjectID != f.project || change.Change != want.change || (event.UndoOf == nil) != (want.undoOf == nil) || want.undoOf != nil && *event.UndoOf != *want.undoOf {
				t.Fatalf("wrong authorized Knowledge summary: changes=%+v undo_of=%v", event.NodeChanges, event.UndoOf)
			}
			found = true
			break
		}
		if !found {
			t.Fatalf("stream missed event %d: %v", want.id, scanner.Err())
		}
	}
}

type fixture struct {
	db        *dbtest.DB
	handler   http.Handler
	a, b      tenant.Principal // two people in tenant one
	viewer    tenant.Principal
	foreign   tenant.Principal // tenant two
	project   string
	other     string // a second project in tenant one
	ticket    string
	elsewhere string // a project in tenant two
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.Open(t)
	f := fixture{db: d}
	tenants := [2]string{}
	for i := range tenants {
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Test') RETURNING id::text`, fmt.Sprint("t", i)).Scan(&tenants[i]); err != nil {
			t.Fatal(err)
		}
	}
	principal := func(tenantID, name string, roles ...string) tenant.Principal {
		p := tenant.Principal{TenantID: tenantID, Kind: tenant.Person, Name: name, Roles: roles}
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person',$2,$3) RETURNING id::text`, tenantID, name, roles).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindLegacy(t, d, tenantID, p.ID)
		return p
	}
	f.a = principal(tenants[0], "Markus Barta", "member")
	f.b = principal(tenants[0], "Mira Holm", "member")
	f.viewer = principal(tenants[0], "Vera Viewer", "viewer")
	dbtest.BindRole(t, d, tenants[0], f.viewer.ID, "viewer")
	f.foreign = principal(tenants[1], "Otto Other", "member")
	node := func(tenantID, key, kind, title string, parent *string) string {
		var id string
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id)
			  SELECT $1,$2,id,$3,$5::uuid FROM node_kinds WHERE tenant_id=$1 AND slug=$4 RETURNING id::text`, tenantID, key, title, kind, parent).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.project = node(tenants[0], "PRJ-1", "project", "Pharos", nil)
	f.other = node(tenants[0], "PRJ-2", "project", "Glint", nil)
	f.ticket = node(tenants[0], "PHAROS-7", "work", "Rotate the fleet keys", &f.project)
	f.elsewhere = node(tenants[1], "PRJ-1", "project", "Theirs", nil)
	f.handler = (&httpapi.Server{Pool: d.App, Modules: []httpapi.Module{New(d.App), events.New(d.App, events.WithUndoHandlers(UndoHandlers()))}}).Handler()
	return f
}

func call(t *testing.T, f fixture, p tenant.Principal, method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else if s, ok := body.(string); ok {
		reader = strings.NewReader(s)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	r := httptest.NewRequest(method, path, reader)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
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

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return out
}

func createEntry(t *testing.T, f fixture, p tenant.Principal, body map[string]any) Entry {
	t.Helper()
	if _, ok := body["project_id"]; !ok {
		body["project_id"] = f.project
	}
	w := call(t, f, p, "POST", "/api/knowledge", body)
	expect(t, w, 201)
	return decode[Entry](t, w)
}

func TestCreateReadListAndIsolation(t *testing.T) {
	f := setup(t)
	deploy := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "deploy-flow", "title": "Deploy flow", "key_prefix": "PHAROS",
		"body": "# Deploy flow\n\nBuild the image, then **roll out** to `csb1`.\n\n## Rollback\n\nPin the previous tag."})
	if deploy.Key != "PHAROS-8" || deploy.Type != "runbook" || deploy.Kind != "runbook" || deploy.Slug != "deploy-flow" || deploy.Status != "active" || deploy.State != "backlog" {
		t.Fatalf("created %+v", deploy.Item)
	}
	if deploy.Project == nil || deploy.Project.ID != f.project || deploy.Author == nil || deploy.Author.Name != "Markus Barta" || deploy.UpdatedBy == nil || deploy.EventID == nil {
		t.Fatalf("created entry context %+v", deploy)
	}
	if deploy.Excerpt != "Build the image, then roll out to csb1. Rollback: Pin the previous tag." {
		t.Fatalf("excerpt %q", deploy.Excerpt)
	}
	// external-system needs its kind: created on first use, with kind.created.
	ext := createEntry(t, f, f.b, map[string]any{"type": "external-system", "slug": "hetzner", "title": "Hetzner Cloud", "status": "proposed",
		"metadata": map[string]any{"url": "https://console.hetzner.cloud", "purpose": "Provisioning"}})
	if ext.Type != "external-system" || ext.Kind != "external_system" || ext.Status != "proposed" || ext.Metadata["purpose"] != "Provisioning" || !strings.HasPrefix(ext.Key, "EXT-") {
		t.Fatalf("external system %+v", ext)
	}
	createEntry(t, f, f.a, map[string]any{"type": "guideline", "slug": "no-edge-accents", "title": "No coloured edge accents", "status": "archived", "body": "Use a tint instead."})
	createEntry(t, f, f.a, map[string]any{"project_id": f.other, "type": "memory", "slug": "deploy-flow", "title": "Glint deploys by hand"})

	// The same slug in another type or project is fine; in the same project and type it is not.
	w := call(t, f, f.a, "POST", "/api/knowledge", map[string]any{"project_id": f.project, "type": "runbook", "slug": "deploy-flow", "title": "Again"})
	expect(t, w, 409)
	conflict := decode[struct {
		Code     string `json:"code"`
		Conflict Item   `json:"conflict"`
	}](t, w)
	if conflict.Code != "slug_taken" || conflict.Conflict.ID != deploy.ID {
		t.Fatalf("conflict %+v", conflict)
	}
	for _, bad := range []map[string]any{
		{"type": "runbook", "slug": "Deploy", "title": "x"},
		{"type": "runbook", "slug": "9lives", "title": "x"},
		{"type": "runbook", "slug": strings.Repeat("a", 65), "title": "x"},
		{"type": "memory", "slug": "stale", "title": "x"},
		{"type": "runbook", "slug": "ok", "title": "  "},
		{"type": "external-system", "slug": "ok", "title": "x", "metadata": map[string]any{"url": "javascript:alert(1)"}},
	} {
		bad["project_id"] = f.project
		expect(t, call(t, f, f.a, "POST", "/api/knowledge", bad), 422)
	}
	expect(t, call(t, f, f.a, "POST", "/api/knowledge", map[string]any{"project_id": f.project, "type": "recipe", "slug": "x", "title": "x"}), 400)
	expect(t, call(t, f, f.a, "POST", "/api/knowledge", map[string]any{"project_id": f.ticket, "type": "runbook", "slug": "x", "title": "x"}), 404)
	expect(t, call(t, f, f.viewer, "POST", "/api/knowledge", map[string]any{"project_id": f.project, "type": "runbook", "slug": "x", "title": "x"}), 403)

	w = call(t, f, f.a, "GET", "/api/knowledge?project_id="+f.project, nil)
	expect(t, w, 200)
	page := decode[ListPage](t, w)
	if page.Total != 3 || len(page.Items) != 3 || page.Counts["type"]["runbook"] != 1 || page.Counts["status"]["archived"] != 1 || page.Counts["status"]["proposed"] != 1 {
		t.Fatalf("project page %+v", page)
	}
	w = call(t, f, f.a, "GET", "/api/knowledge?type=runbook,external-system&status=active,proposed&sort=slug", nil)
	expect(t, w, 200)
	page = decode[ListPage](t, w)
	if len(page.Items) != 2 || page.Items[0].Slug != "deploy-flow" || page.Items[1].Slug != "hetzner" || page.Counts["type"]["memory"] != 1 {
		t.Fatalf("filtered page %+v", page)
	}
	// Search: body words, slug, and a window around the match.
	w = call(t, f, f.a, "GET", "/api/knowledge?q=previous+tag", nil)
	page = decode[ListPage](t, w)
	if len(page.Items) != 1 || page.Items[0].ID != deploy.ID || !strings.Contains(page.Items[0].Excerpt, "previous tag") {
		t.Fatalf("search page %+v", page)
	}
	w = call(t, f, f.a, "GET", "/api/knowledge?q=deploy-flow", nil)
	page = decode[ListPage](t, w)
	if len(page.Items) < 2 || page.Items[0].Slug != "deploy-flow" {
		t.Fatalf("slug search %+v", page)
	}
	w = call(t, f, f.a, "GET", "/api/knowledge/"+deploy.ID, nil)
	expect(t, w, 200)
	if got := decode[Entry](t, w); got.Body == "" || got.EventID != nil {
		t.Fatalf("get %+v", got)
	}
	w = call(t, f, f.a, "GET", "/api/knowledge/resolve?project_id="+f.project+"&type=runbook&slug=deploy-flow", nil)
	expect(t, w, 200)
	if got := decode[Entry](t, w); got.ID != deploy.ID || got.RenamedFrom != "" {
		t.Fatalf("resolve %+v", got)
	}

	// Another tenant sees nothing and cannot write into this project.
	w = call(t, f, f.foreign, "GET", "/api/knowledge", nil)
	if page := decode[ListPage](t, w); len(page.Items) != 0 {
		t.Fatalf("foreign list %+v", page)
	}
	expect(t, call(t, f, f.foreign, "GET", "/api/knowledge/"+deploy.ID, nil), 404)
	expect(t, call(t, f, f.foreign, "PATCH", "/api/knowledge/"+deploy.ID, map[string]any{"title": "Mine"}), 404)
	expect(t, call(t, f, f.foreign, "POST", "/api/knowledge", map[string]any{"project_id": f.project, "type": "runbook", "slug": "x", "title": "x"}), 404)

	// Every write is one event with complete node snapshots.
	var count int
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='knowledge.created' AND before IS NULL AND after ? 'kind_id' AND after ? 'fields'`).Scan(&count)
	})
	if err != nil || count != 4 {
		t.Fatalf("created events %d %v", count, err)
	}
}

func TestUpdateConflictRenameAndUndo(t *testing.T) {
	f := setup(t)
	entry := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "deploy-flow", "title": "Deploy flow", "body": "v1"})
	stamp := entry.UpdatedAt.Format(time.RFC3339Nano)

	// Mira saves first; Markus's save with the old stamp is refused and sees hers.
	w := call(t, f, f.b, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"body": "v2 by Mira"}, "If-Unmodified-Since", stamp)
	expect(t, w, 200)
	mira := decode[Entry](t, w)
	if mira.Body != "v2 by Mira" || mira.UpdatedBy == nil || mira.UpdatedBy.Name != "Mira Holm" || mira.EventID == nil {
		t.Fatalf("mira %+v", mira)
	}
	w = call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"body": "v2 by Markus"}, "If-Unmodified-Since", stamp)
	expect(t, w, 412)
	stale := decode[struct {
		Code  string `json:"code"`
		Entry Entry  `json:"entry"`
	}](t, w)
	if stale.Code != "stale" || stale.Entry.Body != "v2 by Mira" || stale.Entry.UpdatedBy.Name != "Mira Holm" {
		t.Fatalf("stale %+v", stale)
	}

	// A no-op write appends nothing.
	w = call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"title": "Deploy flow", "status": "active"})
	expect(t, w, 200)
	if got := decode[Entry](t, w); got.EventID != nil || !got.UpdatedAt.Equal(mira.UpdatedAt) {
		t.Fatalf("no-op %+v", got)
	}

	// Rename the slug; the old slug still resolves, reporting the rename.
	createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "rollback", "title": "Rollback"})
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"slug": "rollback"}), 409)
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"slug": "Bad Slug"}), 422)
	expect(t, call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"project_id": f.other}), 400)
	w = call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"slug": "ship-it", "status": "archived", "metadata": map[string]any{"related_agents": []string{"camy"}}})
	expect(t, w, 200)
	renamed := decode[Entry](t, w)
	if renamed.Slug != "ship-it" || renamed.Status != "archived" || renamed.State != "cancelled" || renamed.EventID == nil {
		t.Fatalf("renamed %+v", renamed)
	}
	w = call(t, f, f.a, "GET", "/api/knowledge/resolve?project_id="+f.project+"&type=runbook&slug=deploy-flow", nil)
	expect(t, w, 200)
	if got := decode[Entry](t, w); got.ID != entry.ID || got.Slug != "ship-it" || got.RenamedFrom != "deploy-flow" {
		t.Fatalf("resolve after rename %+v", got)
	}
	expect(t, call(t, f, f.a, "GET", "/api/knowledge/resolve?project_id="+f.other+"&type=runbook&slug=deploy-flow", nil), 404)

	// Mira cannot undo Markus's change; he can, once.
	undo := fmt.Sprintf("/api/events/%d/undo", *renamed.EventID)
	expect(t, call(t, f, f.b, "POST", undo, nil), 403)
	expect(t, call(t, f, f.a, "POST", undo, nil), 201)
	expect(t, call(t, f, f.a, "POST", undo, nil), 409)
	w = call(t, f, f.a, "GET", "/api/knowledge/"+entry.ID, nil)
	back := decode[Entry](t, w)
	if back.Slug != "deploy-flow" || back.Status != "active" || back.Body != "v2 by Mira" || len(back.Metadata) != 0 {
		t.Fatalf("after undo %+v", back)
	}
	// Mira's older edit cannot be undone over the later rename and its undo.
	expect(t, call(t, f, f.b, "POST", fmt.Sprintf("/api/events/%d/undo", *mira.EventID), nil), 409)

	// Undo of a rename refuses when the old slug was taken meanwhile.
	w = call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"slug": "ship-it"})
	moved := decode[Entry](t, w)
	createEntry(t, f, f.b, map[string]any{"type": "runbook", "slug": "deploy-flow", "title": "A new deploy flow"})
	expect(t, call(t, f, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", *moved.EventID), nil), 409)

	// Viewers read but never write or undo.
	expect(t, call(t, f, f.viewer, "GET", "/api/knowledge/"+entry.ID, nil), 200)
	expect(t, call(t, f, f.viewer, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"title": "x"}), 403)
}

func TestDeleteUndoAndCreateUndo(t *testing.T) {
	f := setup(t)
	entry := createEntry(t, f, f.a, map[string]any{"type": "guideline", "slug": "tone", "title": "Tone", "body": "Plain words."})
	stale := "2020-01-01T00:00:00Z"
	expect(t, call(t, f, f.a, "DELETE", "/api/knowledge/"+entry.ID, nil, "If-Unmodified-Since", stale), 412)
	w := call(t, f, f.a, "DELETE", "/api/knowledge/"+entry.ID, nil, "If-Unmodified-Since", entry.UpdatedAt.Format(time.RFC3339Nano))
	expect(t, w, 200)
	gone := decode[struct {
		EventID int64 `json:"event_id"`
	}](t, w)
	expect(t, call(t, f, f.a, "GET", "/api/knowledge/"+entry.ID, nil), 404)
	// While deleted, its slug is free; undo then refuses rather than duplicating it.
	blocker := createEntry(t, f, f.b, map[string]any{"type": "guideline", "slug": "tone", "title": "Tone, again"})
	expect(t, call(t, f, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", gone.EventID), nil), 409)
	// Undo of Mira's create removes hers (nobody changed it), then the delete undoes.
	expect(t, call(t, f, f.b, "POST", fmt.Sprintf("/api/events/%d/undo", *blocker.EventID), nil), 201)
	expect(t, call(t, f, f.b, "GET", "/api/knowledge/"+blocker.ID, nil), 404)
	expect(t, call(t, f, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", gone.EventID), nil), 201)
	w = call(t, f, f.a, "GET", "/api/knowledge/"+entry.ID, nil)
	expect(t, w, 200)
	if got := decode[Entry](t, w); got.Body != "Plain words." || got.Slug != "tone" {
		t.Fatalf("restored %+v", got)
	}
	// An entry changed after its creation is not removed by undoing the create.
	w = call(t, f, f.a, "PATCH", "/api/knowledge/"+entry.ID, map[string]any{"body": "Plainer words."})
	expect(t, w, 200)
	expect(t, call(t, f, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", *entry.EventID), nil), 409)
}

func TestLinksAuthorAndImportedEntries(t *testing.T) {
	f := setup(t)
	entry := createEntry(t, f, f.a, map[string]any{"type": "memory", "slug": "keys-rotate-monthly", "title": "Keys rotate monthly"})
	other := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "rotate-keys", "title": "Rotate keys"})
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'cites'),($1,least($2,$4::uuid),greatest($2,$4::uuid),'relates')`,
			f.a.TenantID, entry.ID, f.ticket, other.ID); err != nil {
			return err
		}
		// A classic import: the node and its creator arrive through import events.
		var id string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,body,fields,parent_id,state)
		  SELECT $1,'PHAROS-90',id,'Imported memory','From classic.',jsonb_build_object('slug','from-classic','created_by',$3::text),$2,'backlog'
		  FROM node_kinds WHERE tenant_id=$1 AND slug='memory' RETURNING id::text`, f.a.TenantID, f.project, f.b.ID).Scan(&id); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, f.a, events.Change{NodeID: &id, Type: "import.node_created", After: map[string]any{"id": id}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, f, f.a, "GET", "/api/knowledge/"+entry.ID, nil)
	got := decode[Entry](t, w)
	if len(got.Links) != 2 || got.LinkCount != 2 {
		t.Fatalf("links %+v", got.Links)
	}
	var ticket, runbook *Link
	for i := range got.Links {
		if got.Links[i].Node.ID == f.ticket {
			ticket = &got.Links[i]
		} else {
			runbook = &got.Links[i]
		}
	}
	if ticket == nil || ticket.Type != "cites" || ticket.Direction != "out" || ticket.Node.Key != "PHAROS-7" || ticket.Node.Kind != "work" || ticket.Node.Slug != "" || *ticket.Node.ProjectID != f.project {
		t.Fatalf("ticket link %+v", ticket)
	}
	if runbook == nil || runbook.Node.Type != "runbook" || runbook.Node.Slug != "rotate-keys" {
		t.Fatalf("knowledge link %+v", runbook)
	}
	w = call(t, f, f.a, "GET", "/api/knowledge?project_id="+f.project+"&type=memory&sort=title", nil)
	page := decode[ListPage](t, w)
	if len(page.Items) != 2 || page.Items[0].Title != "Imported memory" || !page.Items[0].Imported || page.Items[0].UpdatedBy != nil {
		t.Fatalf("imported %+v", page.Items)
	}
	w = call(t, f, f.a, "GET", "/api/knowledge/resolve?project_id="+f.project+"&type=memory&slug=from-classic", nil)
	imported := decode[Entry](t, w)
	if imported.Author == nil || imported.Author.Name != "Mira Holm" {
		t.Fatalf("imported author %+v", imported.Author)
	}
}

func TestExcerpt(t *testing.T) {
	body := "# Deploy flow\n\n```sh\nmake image\n```\n\n- [x] Build **the** image with `make`\n- See [the guide](https://x.example) for external_system notes\n\n| a | b |\n|---|---|\n| 1 | 2 |"
	if got := excerpt(body, "Deploy flow", nil, false); got != "Build the image with make, See the guide for external_system notes, a b 1 2" {
		t.Fatalf("excerpt %q", got)
	}
	long := strings.Repeat("alpha beta gamma ", 30) + "needle here " + strings.Repeat("delta ", 40)
	got := excerpt(long, "", []string{"needle"}, false)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !strings.Contains(got, "needle here") || len([]rune(got)) > excerptRunes+2 {
		t.Fatalf("window %q", got)
	}
	if got := excerpt("tail of a sentence and then more words", "", nil, true); got != "…of a sentence and then more words" {
		t.Fatalf("cut %q", got)
	}
	if got := excerpt("# ADR-001 · Aeon foundation\n\nStatus: accepted.", "ADR-001 · Aeon foundation (accepted)", nil, false); got != "Status: accepted." {
		t.Fatalf("a heading that starts the title is dropped: %q", got)
	}
	if got := excerpt("Aeon is deployed by Pharos.", "Aeon", nil, false); got != "Aeon is deployed by Pharos." {
		t.Fatalf("a sentence that starts with the title keeps it: %q", got)
	}
	if statusOf("Cancelled") != statusArchived || statusOf("open") != statusActive || statusOf("proposed") != statusProposed {
		t.Fatal("status classes")
	}
	if slugProblem(specs[0], "ok_slug-2") != "" || slugProblem(specs[2], "needs-review") == "" {
		t.Fatal("slug rules")
	}
}

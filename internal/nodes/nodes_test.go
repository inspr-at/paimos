// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

var (
	adminPool *pgxpool.Pool
	appPool   *pgxpool.Pool
	testDB    *dbtest.DB
	setupOnce sync.Once
	setupErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testDB != nil {
		if err := testDB.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "dbtest cleanup: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func useDB(t *testing.T) {
	t.Helper()
	setupOnce.Do(func() { setupErr = setupDB() })
	if setupErr != nil {
		t.Fatalf("database: %v", setupErr)
	}
}

func setupDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	handle, err := dbtest.New(ctx)
	if err != nil {
		return err
	}
	testDB = handle
	adminPool = handle.Admin
	appPool = handle.App
	return nil
}

func reset(t *testing.T) {
	t.Helper()
	useDB(t)
	if _, err := adminPool.Exec(t.Context(), `TRUNCATE TABLE tenants CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
}

func newPrincipal(t *testing.T, slug string) tenant.Principal {
	t.Helper()
	reset(t)
	return addPrincipal(t, slug)
}

func addPrincipal(t *testing.T, slug string) tenant.Principal {
	t.Helper()
	var tenantID string
	if err := appPool.QueryRow(t.Context(), `
		INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, slug).Scan(&tenantID); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			INSERT INTO principals (tenant_id, kind, name, roles)
			VALUES ($1, 'person', $2, $3)
			RETURNING id::text`, tenantID, slug, []string{"admin"}).Scan(&id)
	})
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	dbtest.BindLegacy(t, testDB, tenantID, id)
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Person, Name: slug, Roles: []string{"admin"}}
}

type countingWriter struct{ n int }

func (c *countingWriter) WriteEvent(context.Context, pgx.Tx, Event) error {
	c.n++
	return nil
}

func callAs(t *testing.T, mod httpapi.Module, p *tenant.Principal, method, path, body string) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	mod.Mount(mux)
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if p != nil {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), *p))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.Bytes()
}

func call(t *testing.T, p *tenant.Principal, method, path, body string) (int, []byte) {
	t.Helper()
	return callAs(t, New(appPool, nil), p, method, path, body)
}

func TestSchemaAndTagMutationsRequirePersonAdmin(t *testing.T) {
	admin := newPrincipal(t, "schema-role-test")
	makePerson := func(name, role string) tenant.Principal {
		p := tenant.Principal{TenantID: admin.TenantID, Kind: tenant.Person, Name: name, Roles: []string{role}}
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person',$2,$3) RETURNING id::text`, admin.TenantID, name, p.Roles).Scan(&p.ID)
		})
		if err != nil {
			t.Fatal(err)
		}
		dbtest.BindLegacy(t, testDB, admin.TenantID, p.ID)
		return p
	}
	member := makePerson("member", "member")
	super := makePerson("super", "super_admin")
	definition := `{"slug":"custom","label":"Custom","short_prefix":"CUS","icon":"circle","field_schema":{}}`
	if status, _ := call(t, &member, http.MethodPost, "/api/kinds", definition); status != http.StatusForbidden {
		t.Fatalf("member kind create: %d", status)
	}
	status, body := call(t, &super, http.MethodPost, "/api/kinds", definition)
	kind := decode[kindJSON](t, status, body, http.StatusCreated)
	if status, _ := call(t, &member, http.MethodPatch, "/api/kinds/"+kind.ID, `{"label":"Changed"}`); status != http.StatusForbidden {
		t.Fatalf("member kind update: %d", status)
	}
	if status, _ := call(t, &member, http.MethodDelete, "/api/kinds/"+kind.ID, ""); status != http.StatusForbidden {
		t.Fatalf("member kind delete: %d", status)
	}
	status, body = call(t, &member, http.MethodPost, "/api/nodes", fmt.Sprintf(`{"kind_id":%q,"title":"Member node"}`, kind.ID))
	decode[nodeJSON](t, status, body, http.StatusCreated)
	tag := createTestTag(t, admin, "Editable tag")
	if status, _ := call(t, &member, http.MethodPatch, "/api/tags/"+tag.ID, `{"name":"Changed"}`); status != http.StatusForbidden {
		t.Fatalf("member tag rename: %d", status)
	}
	if status, body := call(t, &member, http.MethodPatch, "/api/tags/"+tag.ID, `{"color":"green"}`); status != http.StatusOK {
		t.Fatalf("member tag color: %d %s", status, body)
	}
	if status, _ := call(t, &member, http.MethodDelete, "/api/tags/"+tag.ID, ""); status != http.StatusForbidden {
		t.Fatalf("member tag delete: %d", status)
	}
}

func decode[T any](t *testing.T, status int, body []byte, want int) T {
	t.Helper()
	if status != want {
		t.Fatalf("status %d, want %d: %s", status, want, body)
	}
	var out T
	if want == http.StatusNoContent {
		return out
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("json: %v body %s", err, body)
	}
	return out
}

func kindBySlug(t *testing.T, p tenant.Principal, slug string) kindJSON {
	t.Helper()
	status, body := call(t, &p, http.MethodGet, "/api/kinds", "")
	page := decode[struct {
		Items []kindJSON `json:"items"`
	}](t, status, body, http.StatusOK)
	for _, kind := range page.Items {
		if kind.Slug == slug {
			return kind
		}
	}
	t.Fatalf("kind %s not found", slug)
	return kindJSON{}
}

type storedEvent struct {
	Type   string
	NodeID *string
	Before *string
	After  *string
	Actor  string
}

func tenantEvents(t *testing.T, tenantID string) []storedEvent {
	t.Helper()
	var out []storedEvent
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `
			SELECT type, node_id::text, before::text, after::text, actor_principal_id::text
			FROM events ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ev storedEvent
			if err := rows.Scan(&ev.Type, &ev.NodeID, &ev.Before, &ev.After, &ev.Actor); err != nil {
				return err
			}
			out = append(out, ev)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return out
}

func TestUnauthorized(t *testing.T) {
	useDB(t)
	status, body := call(t, nil, http.MethodGet, "/api/nodes", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status %d %s", status, body)
	}
}

func TestKindCRUD(t *testing.T) {
	p := newPrincipal(t, "kinds")
	status, body := call(t, &p, http.MethodGet, "/api/kinds", "")
	page := decode[struct {
		Items []kindJSON `json:"items"`
	}](t, status, body, http.StatusOK)
	if len(page.Items) != 12 {
		t.Fatalf("starter kinds: %d", len(page.Items))
	}
	if page.Items[0].AllowedChildKinds != nil {
		t.Fatalf("starter allowed children: %#v", page.Items[0].AllowedChildKinds)
	}

	status, body = call(t, &p, http.MethodPost, "/api/kinds", `{
		"slug":"bug","label":"Bug","short_prefix":"BUG","icon":"bug",
		"allowed_child_kinds":[],"field_schema":{"type":"object"}
	}`)
	created := decode[kindJSON](t, status, body, http.StatusCreated)
	if created.Slug != "bug" || len(created.AllowedChildKinds) != 0 {
		t.Fatalf("created %#v", created)
	}

	status, body = call(t, &p, http.MethodPost, "/api/kinds", `{
		"slug":"bug","label":"Bug","short_prefix":"BUG","icon":"bug","field_schema":{}
	}`)
	if status != http.StatusConflict {
		t.Fatalf("duplicate slug %d %s", status, body)
	}

	status, body = call(t, &p, http.MethodPatch, "/api/kinds/"+created.ID, `{"slug":"nope","label":"Nope"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("slug patch %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodPatch, "/api/kinds/"+created.ID, `{"label":"Defect","short_prefix":"DEF"}`)
	patched := decode[kindJSON](t, status, body, http.StatusOK)
	if patched.Label != "Defect" || patched.ShortPrefix != "DEF" || patched.Slug != "bug" {
		t.Fatalf("patched %#v", patched)
	}

	status, _ = call(t, &p, http.MethodDelete, "/api/kinds/"+created.ID, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete %d", status)
	}
	status, _ = call(t, &p, http.MethodGet, "/api/kinds/"+created.ID, "")
	if status != http.StatusNotFound {
		t.Fatalf("get deleted %d", status)
	}
	ev := tenantEvents(t, p.TenantID)
	want := []string{evKindCreated, evKindUpdated, evKindDeleted}
	if len(ev) != len(want) {
		t.Fatalf("events %#v", ev)
	}
	for i, typ := range want {
		if ev[i].Type != typ || ev[i].Actor != p.ID || ev[i].NodeID != nil {
			t.Fatalf("event %d %#v", i, ev[i])
		}
	}
	if ev[0].Before != nil || ev[0].After == nil || ev[2].Before == nil || ev[2].After != nil {
		t.Fatalf("kind snapshots %#v", ev)
	}
}

func TestNodeKeysFieldsAndEvents(t *testing.T) {
	p := newPrincipal(t, "nodes")
	project := kindBySlug(t, p, "project")
	status, body := call(t, &p, http.MethodPost, "/api/nodes", `{
		"kind_id":"`+project.ID+`","title":"Alpha","key":"PAI-123"
	}`)
	imported := decode[nodeJSON](t, status, body, http.StatusCreated)
	if imported.Key != "PAI-123" || imported.State != "open" || imported.ParentID != nil || imported.Position != "0" {
		t.Fatalf("imported %#v", imported)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes", `{
		"kind_id":"`+project.ID+`","title":"Beta","key_prefix":"PAI"
	}`)
	next := decode[nodeJSON](t, status, body, http.StatusCreated)
	if next.Key != "PAI-124" {
		t.Fatalf("counter %s", next.Key)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes", `{
		"kind_id":"`+project.ID+`","title":"Gamma"
	}`)
	generated := decode[nodeJSON](t, status, body, http.StatusCreated)
	if generated.Key != "PRJ-1" {
		t.Fatalf("kind prefix %s", generated.Key)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes", `{
		"kind_id":"`+project.ID+`","title":"Both","key":"PAI-9","key_prefix":"PAI"
	}`)
	if status != http.StatusBadRequest {
		t.Fatalf("both keys %d %s", status, body)
	}

	status, body = call(t, &p, http.MethodPost, "/api/kinds", `{
		"slug":"scored","label":"Scored","short_prefix":"SCR","icon":"score",
		"field_schema":{"type":"object","required":["points"],"additionalProperties":false,
			"properties":{"points":{"type":"integer","minimum":0}}}
	}`)
	scored := decode[kindJSON](t, status, body, http.StatusCreated)
	status, body = call(t, &p, http.MethodPost, "/api/nodes", `{
		"kind_id":"`+scored.ID+`","title":"Missing"
	}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("schema %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes", `{
		"kind_id":"`+scored.ID+`","title":"Valid","fields":{"points":2}
	}`)
	valid := decode[nodeJSON](t, status, body, http.StatusCreated)
	var points struct {
		Points int `json:"points"`
	}
	if err := json.Unmarshal(valid.Fields, &points); err != nil || points.Points != 2 {
		t.Fatalf("fields %s", valid.Fields)
	}

	status, body = call(t, &p, http.MethodPatch, "/api/nodes/"+imported.ID, `{"title":"Alpha 2","key":"NO-1"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("immutable key %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodPatch, "/api/nodes/"+imported.ID, `{"title":"Alpha 2","state":"done","fields":`+benefitFields+`}`)
	updated := decode[nodeJSON](t, status, body, http.StatusOK)
	if updated.Key != "PAI-123" || updated.Title != "Alpha 2" || updated.State != "done" {
		t.Fatalf("updated %#v", updated)
	}

	ev := tenantEvents(t, p.TenantID)
	var created int
	for _, event := range ev {
		if event.Actor != p.ID {
			t.Fatalf("actor %#v", event)
		}
		if event.Type == evNodeCreated {
			created++
			if event.Before != nil || event.After == nil || event.NodeID == nil {
				t.Fatalf("create snapshot %#v", event)
			}
		}
	}
	if created != 4 {
		t.Fatalf("created events %d in %#v", created, ev)
	}
}

func TestListCursorFiltersAndTree(t *testing.T) {
	p := newPrincipal(t, "tree")
	project := kindBySlug(t, p, "project")
	epic := kindBySlug(t, p, "epic")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Root"}`)
	other := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Other"}`)
	e2 := mustNode(t, p, `{"kind_id":"`+epic.ID+`","title":"E2","parent_id":"`+root.ID+`"}`)
	e1 := mustNode(t, p, `{"kind_id":"`+epic.ID+`","title":"E1","parent_id":"`+root.ID+`","before_id":"`+e2.ID+`"}`)
	task := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"Task","parent_id":"`+e1.ID+`","state":"done","fields":`+benefitFields+`}`)

	status, body := call(t, &p, http.MethodGet, "/api/nodes?parent_id="+root.ID, "")
	direct := decode[nodePage](t, status, body, http.StatusOK)
	if len(direct.Items) != 2 || direct.Items[0].ID != e1.ID || direct.Items[1].ID != e2.ID {
		t.Fatalf("children %#v", direct.Items)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?parent_id="+root.ID+"&include_descendants=true", "")
	deep := decode[nodePage](t, status, body, http.StatusOK)
	if len(deep.Items) != 3 {
		t.Fatalf("descendants %d", len(deep.Items))
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?parent_id="+root.ID+"&include_descendants=true&state=done", "")
	done := decode[nodePage](t, status, body, http.StatusOK)
	if len(done.Items) != 1 || done.Items[0].ID != task.ID {
		t.Fatalf("state filter %#v", done.Items)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?kind_id="+project.ID+"&sort=title&direction=asc&limit=1", "")
	first := decode[nodePage](t, status, body, http.StatusOK)
	if len(first.Items) != 1 || first.Items[0].ID != other.ID || first.NextCursor == nil {
		t.Fatalf("page %#v", first)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?kind_id="+project.ID+"&sort=title&direction=asc&limit=1&cursor="+*first.NextCursor, "")
	second := decode[nodePage](t, status, body, http.StatusOK)
	if len(second.Items) != 1 || second.Items[0].ID != root.ID || second.NextCursor != nil {
		t.Fatalf("next page %#v", second)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?sort=key&cursor="+*first.NextCursor, "")
	if status != http.StatusBadRequest {
		t.Fatalf("cursor mismatch %d %s", status, body)
	}

	status, body = call(t, &p, http.MethodGet, "/api/nodes/tree?root_id="+root.ID+"&limit=2", "")
	tree := decode[treePage](t, status, body, http.StatusOK)
	if len(tree.Items) != 2 || tree.Items[0].Node.ID != root.ID || tree.Items[0].Depth != 0 ||
		tree.Items[1].Node.ID != e1.ID || tree.Items[1].Depth != 1 || tree.NextCursor == nil {
		t.Fatalf("tree page %#v", tree.Items)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes/tree?root_id="+root.ID+"&limit=2&cursor="+*tree.NextCursor, "")
	rest := decode[treePage](t, status, body, http.StatusOK)
	if len(rest.Items) != 2 || rest.Items[0].Node.ID != task.ID || rest.Items[0].Depth != 2 ||
		rest.Items[1].Node.ID != e2.ID || rest.Items[1].Depth != 1 || rest.NextCursor != nil {
		t.Fatalf("tree rest %#v", rest.Items)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes/tree?root_id="+root.ID+"&max_depth=1", "")
	shallow := decode[treePage](t, status, body, http.StatusOK)
	if len(shallow.Items) != 3 {
		t.Fatalf("max depth %#v", shallow.Items)
	}
	status, _ = call(t, &p, http.MethodGet, "/api/nodes/tree?root_id="+task.ID+"xxx", "")
	if status != http.StatusBadRequest {
		t.Fatalf("bad root %d", status)
	}
	status, _ = call(t, &p, http.MethodGet, "/api/nodes/tree?root_id=00000000-0000-0000-0000-000000000099", "")
	if status != http.StatusNotFound {
		t.Fatalf("missing root %d", status)
	}
}

func TestMoveDeleteAndNarrowedKind(t *testing.T) {
	p := newPrincipal(t, "move")
	project := kindBySlug(t, p, "project")
	epic := kindBySlug(t, p, "epic")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Root"}`)
	leaf := mustNode(t, p, `{"kind_id":"`+epic.ID+`","title":"Leaf","parent_id":"`+root.ID+`"}`)
	child := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"Child","parent_id":"`+root.ID+`"}`)

	status, body := call(t, &p, http.MethodPost, "/api/nodes/"+root.ID+"/move", `{"parent_id":"`+child.ID+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("cycle %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodPatch, "/api/kinds/"+project.ID, `{"allowed_child_kinds":[]}`)
	if status != http.StatusOK {
		t.Fatalf("narrow %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes", `{
		"kind_id":"`+ticket.ID+`","title":"Blocked","parent_id":"`+root.ID+`"
	}`)
	if status != http.StatusConflict {
		t.Fatalf("disallowed child %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes/"+child.ID+"/move", `{"parent_id":"`+root.ID+`","before_id":"`+leaf.ID+`"}`)
	moved := decode[nodeJSON](t, status, body, http.StatusOK)
	if moved.ParentID == nil || *moved.ParentID != root.ID {
		t.Fatalf("reorder %#v", moved)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?parent_id="+root.ID, "")
	kids := decode[nodePage](t, status, body, http.StatusOK)
	if len(kids.Items) != 2 || kids.Items[0].ID != child.ID || kids.Items[1].ID != leaf.ID {
		t.Fatalf("order %#v", kids.Items)
	}

	status, body = call(t, &p, http.MethodDelete, "/api/nodes/"+root.ID, "")
	if status != http.StatusConflict {
		t.Fatalf("parent delete %d %s", status, body)
	}
	status, _ = call(t, &p, http.MethodDelete, "/api/nodes/"+leaf.ID, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete leaf %d", status)
	}
	status, _ = call(t, &p, http.MethodGet, "/api/nodes/"+leaf.ID, "")
	if status != http.StatusNotFound {
		t.Fatalf("deleted get %d", status)
	}
	status, body = call(t, &p, http.MethodPost, "/api/nodes", `{"kind_id":"`+epic.ID+`","title":"Reuse","key":"`+leaf.Key+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("key reuse %d %s", status, body)
	}
	status, _ = call(t, &p, http.MethodDelete, "/api/kinds/"+project.ID, "")
	if status != http.StatusConflict {
		t.Fatalf("kind in use %d", status)
	}

	var movedEvents, deletedEvents int
	for _, event := range tenantEvents(t, p.TenantID) {
		if event.Type == evNodeMoved {
			movedEvents++
			if event.Before == nil || event.After == nil {
				t.Fatalf("move snapshot %#v", event)
			}
		}
		if event.Type == evNodeDeleted {
			deletedEvents++
		}
	}
	if movedEvents != 1 || deletedEvents != 1 {
		t.Fatalf("move/delete events %d %d", movedEvents, deletedEvents)
	}
}

func TestTenantIsolation(t *testing.T) {
	a := newPrincipal(t, "iso-a")
	b := addPrincipal(t, "iso-b")
	project := kindBySlug(t, a, "project")
	node := mustNode(t, a, `{"kind_id":"`+project.ID+`","title":"Secret"}`)
	status, _ := call(t, &b, http.MethodGet, "/api/nodes/"+node.ID, "")
	if status != http.StatusNotFound {
		t.Fatalf("cross get %d", status)
	}
	status, body := call(t, &b, http.MethodGet, "/api/nodes", "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if len(page.Items) != 0 {
		t.Fatalf("cross list %#v", page.Items)
	}
	var nodes, events int
	if err := appPool.QueryRow(t.Context(), `SELECT count(*) FROM nodes`).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := appPool.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if nodes != 0 || events != 0 {
		t.Fatalf("unset tenant saw nodes=%d events=%d", nodes, events)
	}
}

func TestWriterSeam(t *testing.T) {
	p := newPrincipal(t, "writer")
	stub := &countingWriter{}
	status, body := callAs(t, New(appPool, stub), &p, http.MethodPost, "/api/kinds", `{
		"slug":"note","label":"Note","short_prefix":"NTE","icon":"note","field_schema":{}
	}`)
	if status != http.StatusCreated {
		t.Fatalf("stub create %d %s", status, body)
	}
	if stub.n != 1 {
		t.Fatalf("writer calls %d", stub.n)
	}
	if ev := tenantEvents(t, p.TenantID); len(ev) != 0 {
		t.Fatalf("stub wrote rows %#v", ev)
	}
}

func mustNode(t *testing.T, p tenant.Principal, body string) nodeJSON {
	t.Helper()
	status, raw := call(t, &p, http.MethodPost, "/api/nodes", body)
	return decode[nodeJSON](t, status, raw, http.StatusCreated)
}

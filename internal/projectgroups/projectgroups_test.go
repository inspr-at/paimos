// SPDX-License-Identifier: AGPL-3.0-only

package projectgroups

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

type world struct {
	t        *testing.T
	db       *dbtest.DB
	mux      *http.ServeMux
	admin    tenant.Principal
	member   tenant.Principal
	stranger tenant.Principal
	projects []string
	foreign  string
}

func setup(t *testing.T) *world {
	t.Helper()
	d := dbtest.Open(t)
	w := &world{t: t, db: d, mux: http.NewServeMux()}
	New(d.App).Mount(w.mux)
	events.New(d.App, events.WithUndoHandlers(UndoHandlers())).Mount(w.mux)
	w.admin = w.principal("groups", "Ada", "admin")
	w.member = w.principal(w.admin.TenantID, "Max", "member")
	for i, title := range []string{"Pharos", "Aeon", "Glint"} {
		w.projects = append(w.projects, w.project(w.admin.TenantID, fmt.Sprintf("PRJ-%d", i+1), title))
	}
	w.stranger = w.principal("groups-other", "Olga", "admin")
	w.foreign = w.project(w.stranger.TenantID, "PRJ-1", "Elsewhere")
	return w
}

// principal creates a person in tenantOrSlug (a tenant id, or a slug for a new tenant).
func (w *world) principal(tenantOrSlug, name, role string) tenant.Principal {
	w.t.Helper()
	ctx := w.t.Context()
	tenantID := tenantOrSlug
	if !validUUID(tenantOrSlug) {
		if err := w.db.Admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`, tenantOrSlug).Scan(&tenantID); err != nil {
			w.t.Fatal(err)
		}
	}
	var id string
	if err := w.db.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name, roles) VALUES ($1, 'person', $2, $3) RETURNING id::text`, tenantID, name, []string{role}).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	dbtest.BindLegacy(w.t, w.db, tenantID, id)
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Person, Name: name, Roles: []string{role}}
}

func (w *world) project(tenantID, key, title string) string {
	w.t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(w.t.Context()), w.db.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(w.t.Context(), `INSERT INTO nodes (tenant_id, key, kind_id, title, state) SELECT $1::uuid, $2, id, $3, 'active' FROM node_kinds WHERE slug = 'project' RETURNING id::text`, tenantID, key, title).Scan(&id)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) call(p tenant.Principal, method, path, body string) (int, []byte) {
	w.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func want[T any](t *testing.T, status int, body []byte, code int) T {
	t.Helper()
	if status != code {
		t.Fatalf("status %d, want %d: %s", status, code, body)
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func (w *world) groups(p tenant.Principal) []Group {
	w.t.Helper()
	status, body := w.call(p, http.MethodGet, "/api/project-groups", "")
	return want[struct{ Items []Group }](w.t, status, body, http.StatusOK).Items
}

func (w *world) undo(p tenant.Principal, id *int64) (int, []byte) {
	w.t.Helper()
	if id == nil {
		w.t.Fatal("no event to undo")
	}
	return w.call(p, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", *id), "")
}

func (w *world) eventTypes() []string {
	w.t.Helper()
	rows, err := w.db.Admin.Query(w.t.Context(), `SELECT type FROM events WHERE tenant_id = $1 AND type LIKE 'project_group.%' ORDER BY id`, w.admin.TenantID)
	if err != nil {
		w.t.Fatal(err)
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return types
}

func sorted(ids ...string) []string { out := slices.Clone(ids); slices.Sort(out); return out }

func TestSharedGroupsLifecycleWithEventsAndUndo(t *testing.T) {
	w := setup(t)
	pharos, aeon, glint := w.projects[0], w.projects[1], w.projects[2]

	// Everyone reads; nothing is shared yet.
	if got := w.groups(w.member); len(got) != 0 {
		t.Fatalf("empty list = %#v", got)
	}
	// Admins share a group with its projects.
	status, body := w.call(w.admin, http.MethodPost, "/api/project-groups", fmt.Sprintf(`{"name":"  Clients ","project_ids":[%q,%q]}`, pharos, strings.ToUpper(aeon)))
	clients := want[result](t, status, body, http.StatusCreated)
	if clients.Group == nil || clients.Group.Name != "Clients" || clients.Group.Position != 0 || !slices.Equal(clients.Group.ProjectIDs, sorted(pharos, aeon)) || clients.EventID == nil {
		t.Fatalf("created = %s", body)
	}
	// A second group takes Aeon over: a project is in one shared group only.
	status, body = w.call(w.admin, http.MethodPost, "/api/project-groups", fmt.Sprintf(`{"name":"Paused","project_ids":[%q,%q]}`, aeon, glint))
	paused := want[result](t, status, body, http.StatusCreated)
	if paused.Group.Position != 1 || !slices.Equal(paused.Group.ProjectIDs, sorted(aeon, glint)) {
		t.Fatalf("second group = %s", body)
	}
	list := w.groups(w.member)
	if len(list) != 2 || list[0].Name != "Clients" || !slices.Equal(list[0].ProjectIDs, []string{pharos}) || list[1].Name != "Paused" {
		t.Fatalf("members read = %#v", list)
	}
	// Undoing the second creation deletes it and gives Aeon back to Clients.
	status, body = w.undo(w.admin, paused.EventID)
	if status != http.StatusCreated {
		t.Fatalf("undo created: %d %s", status, body)
	}
	list = w.groups(w.admin)
	if len(list) != 1 || !slices.Equal(list[0].ProjectIDs, sorted(pharos, aeon)) {
		t.Fatalf("after undo created = %#v", list)
	}
	// An event is undone once.
	if status, _ := w.undo(w.admin, paused.EventID); status != http.StatusConflict {
		t.Fatalf("second undo = %d", status)
	}

	// Rename, then undo the rename.
	id := clients.Group.ID
	status, body = w.call(w.admin, http.MethodPatch, "/api/project-groups/"+id, `{"name":"Customers"}`)
	renamed := want[result](t, status, body, http.StatusOK)
	if renamed.Group.Name != "Customers" || renamed.EventID == nil {
		t.Fatalf("rename = %s", body)
	}
	// The same name again changes nothing and appends no event.
	status, body = w.call(w.admin, http.MethodPatch, "/api/project-groups/"+id, `{"name":"Customers"}`)
	if same := want[result](t, status, body, http.StatusOK); same.EventID != nil {
		t.Fatalf("no-op patch appended %d", *same.EventID)
	}
	if status, body := w.undo(w.admin, renamed.EventID); status != http.StatusCreated {
		t.Fatalf("undo rename: %d %s", status, body)
	}
	if got := w.groups(w.admin); got[0].Name != "Clients" {
		t.Fatalf("rename undone = %#v", got)
	}

	// Move Glint in, Aeon out, then undo the move.
	status, body = w.call(w.admin, http.MethodPost, "/api/project-groups/assign", fmt.Sprintf(`{"group_id":%q,"project_ids":[%q,%q]}`, id, glint, pharos))
	moved := want[result](t, status, body, http.StatusOK)
	if len(moved.Assignments) != 1 || moved.Assignments[0].ProjectID != glint || moved.EventID == nil {
		t.Fatalf("assign changes only Glint: %s", body)
	}
	status, body = w.call(w.admin, http.MethodPost, "/api/project-groups/assign", fmt.Sprintf(`{"group_id":null,"project_ids":[%q]}`, aeon))
	out := want[result](t, status, body, http.StatusOK)
	if got := w.groups(w.admin); !slices.Equal(got[0].ProjectIDs, sorted(pharos, glint)) {
		t.Fatalf("after moves = %#v", got)
	}
	if status, body := w.undo(w.admin, out.EventID); status != http.StatusCreated {
		t.Fatalf("undo assign: %d %s", status, body)
	}
	if got := w.groups(w.admin); !slices.Equal(got[0].ProjectIDs, sorted(pharos, aeon, glint)) {
		t.Fatalf("assign undone = %#v", got)
	}
	// A move that was changed again since cannot be undone.
	w.call(w.admin, http.MethodPost, "/api/project-groups/assign", fmt.Sprintf(`{"group_id":null,"project_ids":[%q]}`, glint))
	if status, _ := w.undo(w.admin, moved.EventID); status != http.StatusConflict {
		t.Fatalf("stale assign undo = %d", status)
	}

	// Delete, then undo: the group comes back under its id with its projects.
	status, body = w.call(w.admin, http.MethodDelete, "/api/project-groups/"+id, "")
	deleted := want[result](t, status, body, http.StatusOK)
	if got := w.groups(w.admin); len(got) != 0 {
		t.Fatalf("after delete = %#v", got)
	}
	if status, body := w.undo(w.admin, deleted.EventID); status != http.StatusCreated {
		t.Fatalf("undo delete: %d %s", status, body)
	}
	got := w.groups(w.member)
	if len(got) != 1 || got[0].ID != id || got[0].Name != "Clients" || !slices.Equal(got[0].ProjectIDs, sorted(pharos, aeon)) {
		t.Fatalf("delete undone = %#v", got)
	}

	wantTypes := []string{
		EventCreated, EventCreated, EventDeleted, // created twice, undo of the second
		EventUpdated, EventUpdated, // rename and its undo
		EventAssigned, EventAssigned, EventAssigned, EventAssigned, // two moves, an undo, one more
		EventDeleted, EventCreated, // delete and its undo
	}
	if types := w.eventTypes(); !slices.Equal(types, wantTypes) {
		t.Fatalf("events = %v", types)
	}
}

func TestSharedGroupsAreAdminWritesAndTenantScoped(t *testing.T) {
	w := setup(t)
	status, body := w.call(w.admin, http.MethodPost, "/api/project-groups", fmt.Sprintf(`{"name":"Clients","project_ids":[%q]}`, w.projects[0]))
	created := want[result](t, status, body, http.StatusCreated)
	id := created.Group.ID

	// Members read but never write, not even through undo.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/project-groups", `{"name":"Mine"}`},
		{http.MethodPatch, "/api/project-groups/" + id, `{"name":"Mine"}`},
		{http.MethodDelete, "/api/project-groups/" + id, ""},
		{http.MethodPost, "/api/project-groups/assign", fmt.Sprintf(`{"group_id":%q,"project_ids":[%q]}`, id, w.projects[1])},
	} {
		if status, body := w.call(w.member, tc.method, tc.path, tc.body); status != http.StatusForbidden {
			t.Fatalf("member %s %s = %d %s", tc.method, tc.path, status, body)
		}
	}
	if status, _ := w.undo(w.member, created.EventID); status != http.StatusForbidden {
		t.Fatalf("member undo = %d", status)
	}
	// Another tenant sees nothing, cannot change it, and cannot use its projects.
	if got := w.groups(w.stranger); len(got) != 0 {
		t.Fatalf("cross-tenant read = %#v", got)
	}
	if status, _ := w.call(w.stranger, http.MethodPatch, "/api/project-groups/"+id, `{"name":"Taken"}`); status != http.StatusNotFound {
		t.Fatalf("cross-tenant patch = %d", status)
	}
	if status, _ := w.call(w.stranger, http.MethodDelete, "/api/project-groups/"+id, ""); status != http.StatusNotFound {
		t.Fatalf("cross-tenant delete = %d", status)
	}
	if status, _ := w.call(w.admin, http.MethodPost, "/api/project-groups", fmt.Sprintf(`{"name":"Foreign","project_ids":[%q]}`, w.foreign)); status != http.StatusBadRequest {
		t.Fatalf("foreign project = %d", status)
	}
	if status, _ := w.undo(w.stranger, created.EventID); status != http.StatusNotFound {
		t.Fatalf("cross-tenant undo = %d", status)
	}
	var rows int
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.stranger.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM project_groups) + (SELECT count(*) FROM project_group_members)`).Scan(&rows)
	}); err != nil || rows != 0 {
		t.Fatalf("RLS leaked %d rows (%v)", rows, err)
	}

	// Validation.
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/api/project-groups", `{"name":"   "}`, http.StatusBadRequest},
		{http.MethodPost, "/api/project-groups", `{"name":"` + strings.Repeat("x", 61) + `"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/project-groups", `{"name":"a\u0007b"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/project-groups", `{"name":"clients"}`, http.StatusConflict},
		{http.MethodPost, "/api/project-groups", `{"name":"X","project_ids":["nope"]}`, http.StatusBadRequest},
		{http.MethodPost, "/api/project-groups", `{"name":"X","extra":1}`, http.StatusBadRequest},
		{http.MethodPost, "/api/project-groups", `{"name":"X","position":-1}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/project-groups/" + id, `{}`, http.StatusBadRequest},
		{http.MethodPatch, "/api/project-groups/not-a-uuid", `{"name":"X"}`, http.StatusNotFound},
		{http.MethodPost, "/api/project-groups/assign", `{"group_id":null,"project_ids":[]}`, http.StatusBadRequest},
		{http.MethodPost, "/api/project-groups/assign", fmt.Sprintf(`{"group_id":"00000000-0000-4000-8000-000000000000","project_ids":[%q]}`, w.projects[0]), http.StatusNotFound},
	} {
		if status, body := w.call(w.admin, tc.method, tc.path, tc.body); status != tc.status {
			t.Fatalf("%s %s %s = %d %s, want %d", tc.method, tc.path, tc.body, status, body, tc.status)
		}
	}
	// A rename onto another group's name conflicts; so does undoing a delete once the name is taken.
	w.call(w.admin, http.MethodPost, "/api/project-groups", `{"name":"Paused"}`)
	if status, _ := w.call(w.admin, http.MethodPatch, "/api/project-groups/"+id, `{"name":"PAUSED"}`); status != http.StatusConflict {
		t.Fatalf("rename onto a taken name = %d", status)
	}
	status, body = w.call(w.admin, http.MethodDelete, "/api/project-groups/"+id, "")
	gone := want[result](t, status, body, http.StatusOK)
	w.call(w.admin, http.MethodPost, "/api/project-groups", `{"name":"Clients"}`)
	if status, _ := w.undo(w.admin, gone.EventID); status != http.StatusConflict {
		t.Fatalf("undo delete onto a taken name = %d", status)
	}
	if status, _ := w.call(tenant.Principal{}, http.MethodGet, "/api/project-groups", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", status)
	}
}

func TestDeletedProjectsLeaveSharedGroups(t *testing.T) {
	w := setup(t)
	status, body := w.call(w.admin, http.MethodPost, "/api/project-groups", fmt.Sprintf(`{"name":"Clients","project_ids":[%q,%q]}`, w.projects[0], w.projects[1]))
	want[result](t, status, body, http.StatusCreated)
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at = now() WHERE id = $1::uuid`, w.projects[1])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.groups(w.admin); !slices.Equal(got[0].ProjectIDs, []string{w.projects[0]}) {
		t.Fatalf("deleted project still listed: %#v", got)
	}
	if status, _ := w.call(w.admin, http.MethodPost, "/api/project-groups/assign", fmt.Sprintf(`{"group_id":null,"project_ids":[%q]}`, w.projects[1])); status != http.StatusBadRequest {
		t.Fatalf("assigning a deleted project = %d", status)
	}
}

// Risk: an ordinary person loses project_groups.write after the initial admin
// check. The final transaction must refuse before touching groups or events.
func TestPersonWriteRechecksRevokedPermission(t *testing.T) {
	w := setup(t)
	var beforeAudit int
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, w.admin.TenantID).Scan(&beforeAudit); err != nil {
		t.Fatal(err)
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, w.db.App, func(sql string) bool { return sql == "commit" })
	mux := http.NewServeMux()
	New(pool).Mount(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/project-groups", strings.NewReader(`{"name":"Revoked"}`))
	req = req.WithContext(tenant.WithPrincipal(ctx, w.admin))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		done <- rec
	}()
	barrier.Wait(t, ctx) // The successful permission precheck has committed.
	revoke, err := w.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke.Rollback(ctx)
	if err := db.LockTenant(ctx, revoke, w.admin.TenantID); err != nil {
		t.Fatal(err)
	}
	tag, err := revoke.Exec(ctx, `UPDATE role_bindings b SET role_id=r.id FROM roles r WHERE b.tenant_id=$1 AND b.principal_id=$2 AND b.scope_type='workspace' AND r.tenant_id=b.tenant_id AND r.key='member'`, w.admin.TenantID, w.admin.ID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("demote person binding: %v (%d rows)", err, tag.RowsAffected())
	}
	if err := revoke.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	rec := dbtest.Await(t, ctx, done)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), errNotAdmin.msg) {
		t.Fatalf("revoked write: %d %s", rec.Code, rec.Body.String())
	}
	var groups, audit int
	if err := w.db.Admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM project_groups WHERE tenant_id=$1), (SELECT count(*) FROM events WHERE tenant_id=$1)`, w.admin.TenantID).Scan(&groups, &audit); err != nil {
		t.Fatal(err)
	}
	if groups != 0 || audit != beforeAudit {
		t.Fatalf("revoked write left %d groups and %d events", groups, audit)
	}
}

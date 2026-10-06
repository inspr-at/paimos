// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Project views: scoped listing, the full list shape, soft delete with restore,
// owner-only changes and undo through the events module.
func TestProjectViewsRoundTripSoftDeleteAndUndo(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tenantID, ownerID, otherID, projectA, projectB, ticket string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('views-scope', 'Scope') RETURNING id::text`).Scan(&tenantID))
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', 'owner') RETURNING id::text`, tenantID).Scan(&ownerID))
	must(d.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', 'other') RETURNING id::text`, tenantID).Scan(&otherID))
	dbtest.BindRole(t, d, tenantID, ownerID, "member")
	dbtest.BindRole(t, d, tenantID, otherID, "member")
	must(db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
		insert := func(slug, key string, parent *string) (id string) {
			must(tx.QueryRow(ctx, `INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, position)
				SELECT $1::uuid, $2, id, $2, $4::uuid, 0 FROM node_kinds WHERE tenant_id = $1::uuid AND slug = $3 RETURNING id::text`, tenantID, key, slug, parent).Scan(&id))
			return id
		}
		projectA = insert("project", "PRJ-1", nil)
		projectB = insert("project", "PRJ-2", nil)
		ticket = insert("work", "TKT-1", &projectA)
		return nil
	}))

	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	events.New(d.App, events.WithUndoHandlers(UndoHandlers())).Mount(mux)
	request := func(principalID, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: principalID, TenantID: tenantID, Kind: tenant.Person, Roles: []string{"member"}}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, status int) savedView {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want %d body=%s", w.Code, status, w.Body.String())
		}
		var v savedView
		must(json.Unmarshal(w.Body.Bytes(), &v))
		return v
	}
	list := func(principalID, project string) []savedView {
		t.Helper()
		w := request(principalID, http.MethodGet, "/api/views?project_id="+project, "")
		if w.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
		}
		var out struct{ Items []savedView }
		must(json.Unmarshal(w.Body.Bytes(), &out))
		return out.Items
	}

	// The full list shape round-trips; sort is optional and defaults.
	mine := decode(request(ownerID, http.MethodPost, "/api/views", `{"name":"  My open work ","project_id":"`+projectA+`","filters":{"assignee":["me"],"status":["!done"]},"sort_keys":["state","-updated_at"],"group_by":"assignee","columns":["status","assignee"]}`), http.StatusCreated)
	if mine.Name != "My open work" || mine.ProjectID == nil || *mine.ProjectID != projectA || strings.Join(mine.SortKeys, ",") != "state,-updated_at" || mine.GroupBy != "assignee" || mine.Sort.Field != "position" || mine.Shared {
		t.Fatalf("created = %#v", mine)
	}
	shared := decode(request(ownerID, http.MethodPost, "/api/views", `{"name":"Team bugs","project_id":"`+projectA+`","filters":{"tag":["bug"]},"columns":[],"shared":true}`), http.StatusCreated)
	decode(request(ownerID, http.MethodPost, "/api/views", `{"name":"Elsewhere","project_id":"`+projectB+`","filters":{},"columns":[]}`), http.StatusCreated)

	// Validation: a project id must name a project; sort keys and names are bounded.
	for _, body := range []string{
		`{"name":"x","project_id":"` + ticket + `","filters":{},"columns":[]}`,
		`{"name":"x","filters":{},"columns":[],"sort_keys":["state","-state"]}`,
		`{"name":"x","filters":{},"columns":[],"sort_keys":["DROP TABLE"]}`,
		`{"name":"` + strings.Repeat("n", 81) + `","filters":{},"columns":[]}`,
		`{"name":"x","filters":{},"columns":["bad column"]}`,
		`{"name":"x","filters":{},"columns":[],"group_by":"Status"}`,
	} {
		if w := request(ownerID, http.MethodPost, "/api/views", body); w.Code != http.StatusBadRequest {
			t.Fatalf("POST %.60s status=%d body=%s", body, w.Code, w.Body.String())
		}
	}

	// Scoped listing: own and shared views of that project, in creation order.
	if got := list(ownerID, projectA); len(got) != 2 || got[0].ID != mine.ID || got[1].ID != shared.ID {
		t.Fatalf("owner list = %#v", got)
	}
	if got := list(otherID, projectA); len(got) != 1 || got[0].ID != shared.ID {
		t.Fatalf("other list = %#v", got)
	}

	// Another person can read a shared view but neither change nor delete it.
	if w := request(otherID, http.MethodPatch, "/api/views/"+shared.ID, `{"name":"Mine now"}`); w.Code != http.StatusForbidden {
		t.Fatalf("foreign patch status=%d", w.Code)
	}
	if w := request(otherID, http.MethodDelete, "/api/views/"+mine.ID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("foreign private delete status=%d", w.Code)
	}

	// Patch keeps what it does not name.
	renamed := decode(request(ownerID, http.MethodPatch, "/api/views/"+mine.ID, `{"name":"Mine","sort_keys":["-priority"]}`), http.StatusOK)
	if renamed.Name != "Mine" || strings.Join(renamed.SortKeys, ",") != "-priority" || renamed.GroupBy != "assignee" || string(renamed.Filters) != string(mine.Filters) {
		t.Fatalf("renamed = %#v", renamed)
	}

	// Soft delete: gone from reads, restorable with its id, only once.
	if w := request(ownerID, http.MethodDelete, "/api/views/"+mine.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request(ownerID, http.MethodGet, "/api/views/"+mine.ID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("deleted read status=%d", w.Code)
	}
	if w := request(ownerID, http.MethodPatch, "/api/views/"+mine.ID, `{"name":"Ghost"}`); w.Code != http.StatusNotFound {
		t.Fatalf("deleted patch status=%d", w.Code)
	}
	if got := list(ownerID, projectA); len(got) != 1 {
		t.Fatalf("list after delete = %#v", got)
	}
	back := decode(request(ownerID, http.MethodPost, "/api/views/"+mine.ID+"/restore", ""), http.StatusOK)
	if back.ID != mine.ID || back.DeletedAt != nil || back.Name != "Mine" {
		t.Fatalf("restored = %#v", back)
	}
	if w := request(ownerID, http.MethodPost, "/api/views/"+mine.ID+"/restore", ""); w.Code != http.StatusConflict {
		t.Fatalf("second restore status=%d", w.Code)
	}

	// Undo through the events module: the rename goes back, then a delete is undone.
	var renameEvent int64
	must(d.Admin.QueryRow(ctx, `SELECT id FROM events WHERE tenant_id=$1 AND type='view.updated' ORDER BY id DESC LIMIT 1`, tenantID).Scan(&renameEvent))
	if w := request(otherID, http.MethodPost, "/api/events/"+itoa(renameEvent)+"/undo", ""); w.Code != http.StatusForbidden {
		t.Fatalf("foreign undo status=%d body=%s", w.Code, w.Body.String())
	}
	// The view changed since the rename (deleted and restored), so the rename cannot be undone.
	if w := request(ownerID, http.MethodPost, "/api/events/"+itoa(renameEvent)+"/undo", ""); w.Code != http.StatusConflict {
		t.Fatalf("stale undo status=%d body=%s", w.Code, w.Body.String())
	}
	decode(request(ownerID, http.MethodPatch, "/api/views/"+mine.ID, `{"shared":true}`), http.StatusOK)
	must(d.Admin.QueryRow(ctx, `SELECT id FROM events WHERE tenant_id=$1 AND type='view.updated' ORDER BY id DESC LIMIT 1`, tenantID).Scan(&renameEvent))
	if w := request(ownerID, http.MethodPost, "/api/events/"+itoa(renameEvent)+"/undo", ""); w.Code != http.StatusCreated {
		t.Fatalf("undo update status=%d body=%s", w.Code, w.Body.String())
	}
	if got := decode(request(ownerID, http.MethodGet, "/api/views/"+mine.ID, ""), http.StatusOK); got.Shared {
		t.Fatalf("undo left it shared: %#v", got)
	}
	if w := request(ownerID, http.MethodDelete, "/api/views/"+shared.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete shared status=%d", w.Code)
	}
	var deleteEvent int64
	must(d.Admin.QueryRow(ctx, `SELECT id FROM events WHERE tenant_id=$1 AND type='view.deleted' ORDER BY id DESC LIMIT 1`, tenantID).Scan(&deleteEvent))
	if w := request(ownerID, http.MethodPost, "/api/events/"+itoa(deleteEvent)+"/undo", ""); w.Code != http.StatusCreated {
		t.Fatalf("undo delete status=%d body=%s", w.Code, w.Body.String())
	}
	if got := list(otherID, projectA); len(got) != 1 || got[0].ID != shared.ID {
		t.Fatalf("other list after undo = %#v", got)
	}
	if got := list(ownerID, projectA); len(got) != 2 {
		t.Fatalf("owner list after undo = %#v", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

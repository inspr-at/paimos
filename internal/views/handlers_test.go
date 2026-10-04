// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestViewsShareReadOwnerWriteAndAppendEvents(t *testing.T) {
	db := dbtest.Open(t)
	ctx := t.Context()
	var tenantID, ownerID, otherID, foreignTenantID, foreignOwnerID, foreignViewID string
	if err := db.Admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('views-test', 'Views') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', 'owner') RETURNING id::text`, tenantID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := db.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'agent', 'other') RETURNING id::text`, tenantID).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	// Views outside a project are workspace rows (ADR-003 P2): members see them.
	dbtest.BindRole(t, db, tenantID, ownerID, "member")
	dbtest.BindRole(t, db, tenantID, otherID, "member")
	if err := db.Admin.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('views-foreign', 'Foreign') RETURNING id::text`).Scan(&foreignTenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.Admin.QueryRow(ctx, `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', 'foreign') RETURNING id::text`, foreignTenantID).Scan(&foreignOwnerID); err != nil {
		t.Fatal(err)
	}
	if err := db.Admin.QueryRow(ctx, `
		INSERT INTO saved_views (tenant_id, owner_principal_id, name)
		VALUES ($1, $2, 'Foreign view') RETURNING id::text`, foreignTenantID, foreignOwnerID).Scan(&foreignViewID); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	var baseline int
	if err := db.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tenantID).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	New(db.App).Mount(mux)
	request := func(principalID, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: principalID, TenantID: tenantID}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	created := request(ownerID, http.MethodPost, "/api/views", `{"name":"Shared","filters":{"state":"open"},"sort":{"field":"position","direction":"asc"},"columns":["key","title"],"shared":true}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var view savedView
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ID == "" || view.OwnerPrincipal != ownerID || !view.Shared {
		t.Fatalf("created view = %#v", view)
	}

	read := request(otherID, http.MethodGet, "/api/views/"+view.ID, "")
	if read.Code != http.StatusOK {
		t.Fatalf("shared read status=%d body=%s", read.Code, read.Body.String())
	}
	foreign := request(ownerID, http.MethodGet, "/api/views/"+foreignViewID, "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant view status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	patch := request(otherID, http.MethodPatch, "/api/views/"+view.ID, `{"name":"Stolen"}`)
	if patch.Code != http.StatusForbidden {
		t.Fatalf("non-owner patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	var eventCount int
	if err := db.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tenantID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != baseline+1 {
		t.Fatalf("events after create and rejected patch = %d, want %d", eventCount, baseline+1)
	}

	updated := request(ownerID, http.MethodPatch, "/api/views/"+view.ID, `{"name":"Renamed"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("owner patch status=%d body=%s", updated.Code, updated.Body.String())
	}
	deleted := request(ownerID, http.MethodDelete, "/api/views/"+view.ID, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("owner delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if err := db.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1`, tenantID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != baseline+3 {
		t.Fatalf("events after create/update/delete = %d, want %d", eventCount, baseline+3)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenantbrand"
)

// /api/me gives a project-only member their workspace's brand, so the logo it
// links must load for them too, through the real session and permission
// middleware, while tenant row-level security still decides whose logo it is.
func TestBrandLogoReachesProjectOnlyMembers(t *testing.T) {
	reset(t)
	m := newMod(t, Config{})
	tenantA := insertTenant(t, "brand-a", "Brand A")
	tenantB := insertTenant(t, "brand-b", "Brand B")

	const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := testInTenant(t.Context(), appPool, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO tenant_brand_logos(tenant_id,variant,content_type,content,sha256,width,height,uploaded_by_principal_id)
			VALUES($1::uuid,'light','image/png','\x89504e47'::bytea,$2,64,64,gen_random_uuid())`, tenantA, shaA)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	projectOnly := func(tid, key, projectKey string) string {
		t.Helper()
		personID, identityID := signinPerson(t, tid, key, key, key+"@example.com", key+"@example.com", "guest")
		if err := testInTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
			var projectID string
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
				SELECT $1::uuid,id,$2,$2 FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'
				RETURNING nodes.id::text`, tid, projectKey).Scan(&projectID); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
				SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='guest'`, tid, personID, projectID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		token, err := m.startSession(t.Context(), identityID, tid, personID)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	insider := projectOnly(tenantA, "insider", "BRA-1")
	outsider := projectOnly(tenantB, "outsider", "BRB-1")
	_, strangerIdentity := signinPerson(t, tenantA, "stranger", "stranger", "stranger@example.com", "stranger@example.com", "guest")
	strangerID := ""
	if err := testInTenant(t.Context(), appPool, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM principals WHERE identity_id=$1::uuid`, strangerIdentity).Scan(&strangerID)
	}); err != nil {
		t.Fatal(err)
	}
	unbound, err := m.startSession(t.Context(), strangerIdentity, tenantA, strangerID)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	tenantbrand.New(appPool).Mount(mux)
	secured := m.Middleware(mux)
	get := func(path, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		_, req.Pattern = mux.Handler(req)
		if token != "" {
			req.Header.Set("Cookie", "aeon_session="+token)
		}
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, req)
		return w
	}

	if w := get("/api/brand/logo/light", insider); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("project-only member of the workspace: %d %s", w.Code, w.Body.String())
	}
	// Another workspace's project-only member is decided by their own tenant's rows.
	if w := get("/api/brand/logo/light", outsider); w.Code != http.StatusNotFound {
		t.Fatalf("project-only member of another workspace: %d %s", w.Code, w.Body.String())
	}
	// No binding at all keeps the denial; so does no session.
	if w := get("/api/brand/logo/light", unbound); w.Code != http.StatusForbidden {
		t.Fatalf("member without any binding: %d", w.Code)
	}
	if w := get("/api/brand/logo/light", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	// The settings routes stay workspace-only for a project-only member.
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		path := "/api/settings/brand"
		if method != http.MethodGet {
			path += "/logo/light"
		}
		req := httptest.NewRequest(method, path, nil)
		_, req.Pattern = mux.Handler(req)
		req.Header.Set("Cookie", "aeon_session="+insider)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s for a project-only member: %d", method, path, w.Code)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a shared file exposes another attachment in its otherwise hidden project.
func TestNodeGrantAttachmentIsolation(t *testing.T) {
	d, p, shared := setup(t)
	ctx := t.Context()
	var project, sibling string
	files := make([]string, 2)
	err := db.InTenant(dbtest.Seed(ctx), d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PRJ-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, p.TenantID).Scan(&project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id=$1 WHERE id=$2`, project, shared); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'ATT-2','Sibling',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, p.TenantID, project).Scan(&sibling); err != nil {
			return err
		}
		for i, node := range []string{shared, sibling} {
			if err := tx.QueryRow(ctx, `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,repeat('a',64),'file','text/plain',4,$3) RETURNING id::text`, p.TenantID, node, p.ID).Scan(&files[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	store := Store{FilesDir: t.TempDir()}
	path, err := store.path(p.TenantID, strings.Repeat("a", 64), "original")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := (&httpapi.Server{Pool: d.App, Modules: []httpapi.Module{New(d.App, store)}}).Handler()
	for _, id := range files {
		if w := request(t, mux, p, "GET", "/api/attachments/"+id+"/content", "", nil); w.Code != 200 || w.Body.String() != "test" {
			t.Fatalf("fixture content missing: %d %s", w.Code, w.Body.String())
		}
	}
	if _, err := d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'file_reader','Files') RETURNING id::text`, p.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'attachments.read')`, p.TenantID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, p.TenantID, p.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'node',$3 FROM roles WHERE tenant_id=$1 AND key='viewer'`, p.TenantID, p.ID, shared); err != nil {
		t.Fatal(err)
	}
	for i, id := range files {
		for _, suffix := range []string{"", "/content"} {
			want := 200
			if i == 1 {
				want = 404
			}
			if w := request(t, mux, p, "GET", "/api/attachments/"+id+suffix, "", nil); w.Code != want {
				t.Fatalf("attachment %d %s: status %d want %d: %s", i, suffix, w.Code, want, w.Body.String())
			}
		}
	}
}

func TestMetadataLookupHonorsNodeVisibilityAndDeletion(t *testing.T) {
	d, p, node := setup(t)
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO attachments(tenant_id,node_id,sha256,name,content_type,size,created_by) VALUES($1,$2,repeat('a',64),'file','text/plain',4,$3) RETURNING id::text`, p.TenantID, node, p.ID).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := (&httpapi.Server{Pool: d.App, Modules: []httpapi.Module{New(d.App, Store{})}}).Handler()
	w := request(t, mux, p, "GET", "/api/attachments/"+id, "", nil)
	if w.Code != 200 {
		t.Fatalf("metadata status %d: %s", w.Code, w.Body.String())
	}
	foreign := tenant.Principal{Kind: tenant.Person, Roles: []string{"member"}}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('metadata-other','Other') RETURNING id::text`).Scan(&foreign.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other',ARRAY['member']) RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, foreign.TenantID, foreign.ID)
	if w := request(t, mux, foreign, "GET", "/api/attachments/"+id, "", nil); w.Code != 404 {
		t.Fatalf("foreign attachment metadata status %d", w.Code)
	}
	var hiddenProject, visibleProject string
	for i, target := range []*string{&hiddenProject, &visibleProject} {
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, p.TenantID, []string{"HIDDEN-1", "VISIBLE-1"}[i]).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, hiddenProject, node); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, p.TenantID, p.ID, visibleProject); err != nil {
		t.Fatal(err)
	}
	// The auth middleware supplies the permitted project scope. Prove both a
	// project-only read and RLS rejection when the node moves after that scope
	// was decided, without relying on a workspace-permission denial.
	projectRead := func() *httptest.ResponseRecorder {
		ctx := authz.WithRouteScope(tenant.WithPrincipal(t.Context(), p), authz.Scope{ProjectID: visibleProject})
		r := httptest.NewRequest("GET", "/api/attachments/"+id, nil).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, visibleProject, node); err != nil {
		t.Fatal(err)
	}
	if w := projectRead(); w.Code != 200 {
		t.Fatalf("visible project attachment metadata status %d", w.Code)
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, hiddenProject, node); err != nil {
		t.Fatal(err)
	}
	if w := projectRead(); w.Code != 404 {
		t.Fatalf("hidden attachment metadata status %d", w.Code)
	}
	dbtest.BindLegacy(t, d, p.TenantID, p.ID)
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, node)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/content"} {
		w = request(t, mux, p, "GET", "/api/attachments/"+id+suffix, "", nil)
		if w.Code != 404 {
			t.Fatalf("deleted-node attachment leaked through %q: %d", suffix, w.Code)
		}
	}
}

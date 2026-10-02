// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"testing"
)

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
	if w := request(t, mux, p, "GET", "/api/attachments/"+id, "", nil); w.Code != 404 {
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

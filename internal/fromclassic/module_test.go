// SPDX-License-Identifier: AGPL-3.0-only

package fromclassic

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestResolveImportedLinksWithProjectVisibility(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	boundary := errors.New("stop before work upgrade")
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name == "1215_one_work_kind.sql" {
			return boundary
		}
		return nil
	}); !errors.Is(err, boundary) {
		t.Fatalf("legacy schema: %v", err)
	}
	const tenantID = "11111111-1111-4111-8111-111111111111"
	var member, guest, project, hidden, customer string
	ctx := dbtest.Seed(t.Context())
	err = db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1::uuid,'classic','Classic')`, tenantID); err != nil {
			return err
		}
		for _, name := range []string{"member", "guest"} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person',$2,ARRAY[$2::text]) RETURNING id::text`, tenantID, name).Scan(&id); err != nil {
				return err
			}
			if name == "member" {
				member = id
			} else {
				guest = id
			}
		}
		for _, item := range []struct {
			id            int
			key, routeKey string
		}{{7, "PRJ-1", "PAI"}, {8, "PRJ-2", "SECRET"}} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,fields)
			  SELECT $1::uuid,$2,k.id,$2,jsonb_build_object('classic',jsonb_build_object('id',$3::int,'key',$4::text))
			  FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug='project' RETURNING id::text`, tenantID, item.key, item.id, item.routeKey).Scan(&id); err != nil {
				return err
			}
			if item.id == 7 {
				project = id
			} else {
				hidden = id
			}
		}
		for _, item := range []struct {
			id          int
			key, parent string
		}{{42, "PAI-42", project}, {99, "SEC-99", hidden}} {
			if _, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
			  SELECT $1::uuid,$2,k.id,$2,$3::uuid,jsonb_build_object('classic',jsonb_build_object('id',$4::int))
			  FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug='ticket'`, tenantID, item.key, item.parent, item.id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon)
		  VALUES($1::uuid,'organisation','Organisation','ORG','organisation')`, tenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title)
		  SELECT $1::uuid,'ORG-1',k.id,'Classic customer' FROM node_kinds k
		  WHERE k.tenant_id=$1::uuid AND k.slug='organisation' RETURNING id::text`, tenantID).Scan(&customer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO paimos_offer_imports
		  (tenant_id,source_instance,source_kind,source_id,node_id,source_revision,revision_rank,source_sha256)
		  VALUES($1::uuid,'classic-source','customer','17',$2::uuid,'v1',1,repeat('a',64))`, tenantID, customer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
		  SELECT $1::uuid,$2::uuid,r.id,'workspace' FROM roles r WHERE r.tenant_id=$1::uuid AND r.key='owner'`, tenantID, member); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		  SELECT $1::uuid,$2::uuid,r.id,'project',$3::uuid FROM roles r WHERE r.tenant_id=$1::uuid AND r.key='guest'`, tenantID, guest, project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.key='PAI-42'`).Scan(&kind); err != nil {
			return err
		}
		if kind != "work" {
			t.Fatalf("imported issue was not upgraded: %s", kind)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(p *tenant.Principal, classicPath string) (int, destination) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/from-classic?path="+url.QueryEscape(classicPath), nil)
		if p != nil {
			req = req.WithContext(tenant.WithPrincipal(req.Context(), *p))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var got destination
		if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("bad response: %s", w.Body.String())
		}
		return w.Code, got
	}
	memberPrincipal := tenant.Principal{ID: member, TenantID: tenantID, Kind: tenant.Person}
	guestPrincipal := tenant.Principal{ID: guest, TenantID: tenantID, Kind: tenant.Person}
	for _, test := range []struct{ path, want string }{
		{"/issues/42?tab=activity", "/p/PAI/PAI-42"},
		{"/projects/7", "/p/PAI"},
		{"/projects/7/issues/42", "/p/PAI/PAI-42"},
		{"/projects/7/journey", "/p/PAI"},
		{"/portal/projects/7/issues/42", "/p/PAI/PAI-42"},
		{"/customers", "/business/customers"},
		{"/crm", "/business/customers"},
		{"/hours/week", "/business/hours"},
		{"/customers/17", "/business/customers/" + customer},
		{"/crm/17", "/business/customers/" + customer},
	} {
		status, got := call(&memberPrincipal, test.path)
		if status != 200 || got.Path != test.want || got.Notice != "" {
			t.Errorf("%s: %d %+v, want %s", test.path, status, got, test.want)
		}
	}
	for _, path := range []string{"/issues/99", "/projects/8", "/issues/404", "/customers/17", "/unknown", "/intake", "/portal"} {
		status, got := call(&guestPrincipal, path)
		if status != 200 || got.Path != "/" || got.Notice != movedNotice {
			t.Errorf("%s: %d %+v", path, status, got)
		}
	}
	if status, got := call(&guestPrincipal, "/issues/42"); status != 200 || got.Path != "/p/PAI/PAI-42" {
		t.Errorf("guest's visible issue: %d %+v", status, got)
	}
	if status, _ := call(nil, "/issues/42"); status != 401 {
		t.Errorf("anonymous: %d", status)
	}
	if status, _ := call(&memberPrincipal, "https://example.com/issues/42"); status != 400 {
		t.Errorf("external URL: %d", status)
	}
	// Duplicate visible imported identities fail closed after kind migration too.
	if err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields) SELECT $1,'PAI-43',k.id,'Ambiguous import',$2,'{"classic":{"id":42}}' FROM node_kinds k WHERE k.slug='work'`, tenantID, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if status, got := call(&memberPrincipal, "/issues/42"); status != 200 || got.Path != "/" || got.Notice != movedNotice {
		t.Fatalf("ambiguous upgraded issue resolved: %d %+v", status, got)
	}
}

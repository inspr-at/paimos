// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestRolesNoEscalationAndBuiltinImmutability(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, ownerID, adminID, builtinID, ownerRoleID string
	err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('az1-roles','AZ1 roles') RETURNING id::text`).Scan(&tid)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for _, item := range []struct {
			id         *string
			name, role string
		}{{&ownerID, "Owner", "owner"}, {&adminID, "Admin", "admin"}} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'person',$2) RETURNING id::text`, tid, item.name).Scan(item.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
              SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key=$3`, tid, *item.id, item.role); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key='admin'`, tid).Scan(&builtinID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key='owner'`, tid).Scan(&ownerRoleID)
	})
	if err != nil {
		t.Fatal(err)
	}
	config := d.App.Config().Copy()
	config.MaxConns = 1 // Role changes must not open a nested tenant transaction.
	one, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	mux := http.NewServeMux()
	New(one).Mount(mux)
	request := func(method, path, body, id string) int {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), tenant.Principal{ID: id, TenantID: tid, Kind: tenant.Person}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := request("POST", "/api/roles", `{"name":"Escalated","permissions":["ownership.transfer"]}`, adminID); code != 403 {
		t.Errorf("admin escalated: %d", code)
	}
	if code := request("POST", "/api/roles", `{"name":"Observer","permissions":["nodes.read"]}`, adminID); code != 201 {
		t.Errorf("admin role creation: %d", code)
	}
	if code := request("POST", "/api/roles", `{"name":"Watch denied","permissions":["harness.watch"]}`, adminID); code != 403 {
		t.Errorf("admin implicitly delegated conversation access: %d", code)
	}
	if code := request("POST", "/api/roles", `{"name":"Watch explicit","permissions":["harness.watch"]}`, ownerID); code != 201 {
		t.Errorf("owner could not explicitly delegate conversation access: %d", code)
	}
	err = db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		p := tenant.Principal{ID: ownerID, TenantID: tid, Kind: tenant.Person}
		if RequireTx(ctx, tx, p, "harness.watch", Scope{}) == nil {
			t.Error("delegating conversation access implicitly granted owner viewing")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if code := request("PATCH", "/api/roles/"+builtinID, `{"name":"Changed"}`, ownerID); code != 403 {
		t.Errorf("built-in edited: %d", code)
	}
	if code := request("DELETE", "/api/roles/"+builtinID, "", ownerID); code != 403 {
		t.Errorf("built-in deleted: %d", code)
	}
	if code := request("PUT", "/api/members/"+ownerID+"/workspace-role", `{"role_id":null}`, adminID); code != 403 {
		t.Errorf("admin removed owner: %d", code)
	}
	if code := request("PUT", "/api/members/"+ownerID+"/workspace-role", `{"role_id":null}`, ownerID); code != 409 {
		t.Errorf("last owner removed: %d", code)
	}
	if code := request("PUT", "/api/members/"+adminID+"/workspace-role", `{"role_id":"`+ownerRoleID+`"}`, ownerID); code != 200 {
		t.Errorf("owner granted ownership: %d", code)
	}
	if code := request("PUT", "/api/members/"+ownerID+"/workspace-role", `{"role_id":"`+builtinID+`"}`, ownerID); code != 200 {
		t.Errorf("owner transferred ownership: %d", code)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestResolvePlacementAuthorizationPrecedesPersonAndTicketMismatch(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "resolve-authority", "person", "Admin", []string{"admin"})
	prefDoc(t, admin)
	viewer := addPrincipal(t, admin.TenantID, "person", "Registry-only", nil)
	var ticket string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		var role, project string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'models_only','Models only') RETURNING id::text`, admin.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'models.read')`, admin.TenantID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, admin.TenantID, viewer.ID, role); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'AUTH-1','Hidden project' FROM node_kinds WHERE slug='project' RETURNING id::text`, admin.TenantID).Scan(&project); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,fields) SELECT $1,id,'AUTH-2','Hidden ticket',$2,'{"route_role":"build","area":"backend"}' FROM node_kinds WHERE slug='ticket' RETURNING id::text`, admin.TenantID, project).Scan(&ticket)
	}); err != nil {
		t.Fatal(err)
	}
	expectPrefError(t, viewer, "GET", "/api/models/resolve?ticket="+ticket+"&project_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", 403, "permission")
	expectPrefError(t, admin, "GET", "/api/models/resolve?role=build&person_id=bad", "", 400, "invalid_placement")
	denied := addPrincipal(t, admin.TenantID, "person", "Denied", nil)
	expectPrefError(t, denied, "GET", "/api/models/resolve?role=build&person_id=bad", "", 403, "permission")
}

func TestRestoringUnretiredProfileDoesNotInventAnEvent(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "restore-noop", "person", "Admin", []string{"admin"})
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	path := "/api/models/" + profiles[0].ID + "/retire"
	for range 2 {
		decode[map[string]any](t, &admin, "DELETE", path, "", 200)
	}
	if eventCount(t, admin, "model.profile_restored") != 0 {
		t.Fatal("no-op restores appended events")
	}
	decode[map[string]any](t, &admin, "POST", path, `{"reason":"Fixture retirement"}`, 200)
	decode[map[string]any](t, &admin, "DELETE", path, "", 200)
	decode[map[string]any](t, &admin, "DELETE", path, "", 200)
	if eventCount(t, admin, "model.profile_restored") != 1 {
		t.Fatal("restore transition recorded incorrectly")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCoordinatorKeyRecognition(t *testing.T) {
	colon := []string{"harness:read", "harness:write", "harness:worker", "inbox:read", "inbox:send", "nodes:read", "work_orders:read"}
	if !IsCoordinatorKey(CoordinatorBaseScopes) || !IsCoordinatorKey(CoordinatorKeyScopes) || !IsCoordinatorKey(colon) {
		t.Fatal("coordinator ceiling not recognized")
	}
	if IsCoordinatorKey(nil) || IsCoordinatorKey([]string{"nodes.read", "models.read", "rules.read"}) || IsCoordinatorKey(CoordinatorBaseScopes[:len(CoordinatorBaseScopes)-1]) {
		t.Fatal("narrow or partial key recognized as coordinator")
	}
	if CoordinatorCeiling(CoordinatorBaseScopes, "rules.write") || CoordinatorCeiling([]string{"nodes.read"}, "models.read") {
		t.Fatal("ceiling widened past the two reads")
	}
}

func TestCoordinatorReadsModelsAndProjectRules(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, projectA, projectB, adminID, viewerID string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('az327','AEON-327') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Owner',ARRAY['admin']) RETURNING id::text`, tid).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Viewer',ARRAY['viewer']) RETURNING id::text`, tid).Scan(&viewerID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, adminID, "admin")
	dbtest.BindRole(t, d, tid, viewerID, "viewer")
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1::uuid,id,'AZ327-1','A' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project' RETURNING id::text`, tid).Scan(&projectA); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1::uuid,id,'AZ327-2','B' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project' RETURNING id::text`, tid).Scan(&projectB)
	}); err != nil {
		t.Fatal(err)
	}

	historical := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Scopes: append([]string{}, CoordinatorBaseScopes...)}
	if err := insertCoordinator(ctx, d, tid, &historical, "historical", ""); err != nil {
		t.Fatal(err)
	}
	limited := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Scopes: append([]string{}, CoordinatorBaseScopes...), KeyCreatorID: adminID}
	if err := insertProjectCoordinator(ctx, d, tid, &limited, projectA); err != nil {
		t.Fatal(err)
	}
	capped := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Scopes: append([]string{}, CoordinatorBaseScopes...), KeyCreatorID: viewerID}
	if err := insertCoordinator(ctx, d, tid, &capped, "capped", ""); err != nil {
		t.Fatal(err)
	}
	worker := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Scopes: []string{"nodes.read"}}
	if err := insertCoordinator(ctx, d, tid, &worker, "worker", "nodes.read"); err != nil {
		t.Fatal(err)
	}

	allow := func(name string, p tenant.Principal, permission string, scope Scope, want bool) {
		t.Helper()
		err := Require(BindPool(tenant.WithPrincipal(ctx, p), d.App), permission, scope)
		if (err == nil) != want {
			t.Errorf("%s %s %+v: %v, want allowed=%v", name, permission, scope, err, want)
		}
	}
	allow("historical", historical, "models.read", Scope{}, true)
	allow("historical", historical, "models.manage", Scope{}, false)
	allow("historical", historical, "rules.read", Scope{}, false)
	allow("historical", historical, "rules.read", Scope{ProjectID: projectA}, true)
	allow("historical", historical, "rules.read", Scope{ProjectID: projectB}, true)
	allow("historical", historical, "rules.read", Scope{AnyProject: true}, true)
	allow("historical", historical, "rules.write", Scope{ProjectID: projectA}, false)
	allow("limited", limited, "models.read", Scope{}, true)
	allow("limited", limited, "rules.read", Scope{ProjectID: projectA}, true)
	allow("limited", limited, "rules.read", Scope{ProjectID: projectB}, false)
	allow("limited", limited, "rules.read", Scope{}, false)
	allow("limited", limited, "rules.read", Scope{AnyProject: true}, true)
	allow("viewer creator", capped, "models.read", Scope{}, true)
	allow("viewer creator", capped, "rules.read", Scope{ProjectID: projectA}, false)
	allow("worker", worker, "models.read", Scope{}, false)
	allow("worker", worker, "rules.read", Scope{AnyProject: true}, false)

	err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		check, err := ProjectsTx(ctx, tx, limited)
		if err != nil {
			return err
		}
		for _, tc := range []struct {
			permission, project string
		}{
			{"models.read", ""},
			{"rules.read", ""},
			{"rules.read", projectA},
			{"rules.read", projectB},
			{"rules.write", projectA},
			{"nodes.read", projectA},
			{"nodes.read", projectB},
		} {
			got := check(tc.permission, tc.project)
			want := RequireTx(ctx, tx, limited, tc.permission, Scope{ProjectID: tc.project})
			if want != nil && !errors.Is(want, ErrForbidden) {
				return want
			}
			if got != (want == nil) {
				t.Errorf("ProjectsTx %s %s = %v, RequireTx %v", tc.permission, tc.project, got, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, pattern := range []string{"GET /api/models/resolve", "GET /api/rules/layers", "GET /api/rules/sets", "GET /api/rules/merged"} {
		err := RequirePattern(BindPool(tenant.WithPrincipal(ctx, historical), d.App), pattern, Scope{AnyProject: true})
		if err != nil {
			t.Errorf("historical %s: %v", pattern, err)
		}
	}
	if err := RequirePattern(BindPool(tenant.WithPrincipal(ctx, historical), d.App), "GET /api/rules/layers", Scope{}); err == nil {
		t.Fatal("workspace rules.read granted to a coordinator key")
	}
	if err := RequirePattern(BindPool(tenant.WithPrincipal(ctx, worker), d.App), "GET /api/models/resolve", Scope{}); err == nil {
		t.Fatal("worker resolved models")
	}
}

func insertCoordinator(ctx context.Context, d *dbtest.DB, tid string, p *tenant.Principal, name, only string) error {
	return db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'agent',$2) RETURNING id::text`, tid, name).Scan(&p.ID); err != nil {
			return err
		}
		var roleID string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,$2,$3) RETURNING id::text`, tid, "agent_"+name, name).Scan(&roleID); err != nil {
			return err
		}
		perms := CoordinatorBaseScopes
		if only != "" {
			perms = []string{only}
		}
		for _, permission := range perms {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,$3)`, tid, roleID, permission); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, tid, p.ID, roleID)
		return err
	})
}

func insertProjectCoordinator(ctx context.Context, d *dbtest.DB, tid string, p *tenant.Principal, projectA string) error {
	return db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'agent','project-coordinator') RETURNING id::text`, tid).Scan(&p.ID); err != nil {
			return err
		}
		var workspace, projectRole string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'coord_ws','Coordinator workspace') RETURNING id::text`, tid).Scan(&workspace); err != nil {
			return err
		}
		for _, permission := range []string{"harness.read", "harness.write", "harness.worker", "inbox.read", "inbox.send", "work_orders.read"} {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,$3)`, tid, workspace, permission); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, tid, p.ID, workspace); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'coord_project','Coordinator project') RETURNING id::text`, tid).Scan(&projectRole); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'nodes.read')`, tid, projectRole); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1::uuid,$2::uuid,$3::uuid,'project',$4::uuid)`, tid, p.ID, projectRole, projectA)
		return err
	})
}

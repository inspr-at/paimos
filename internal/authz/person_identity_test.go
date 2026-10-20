// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Risk: an expanded scope is interpreted as a project grant, widening AnyProject
// and node visibility; malformed mixed scopes can evade their intended boundary.
func TestPersonIdentityScopeShapesAndGrantIsolation(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	tid, err := tenantbootstrap.Create(ctx, d.App, "identity-scopes", "Identity scopes")
	if err != nil {
		t.Fatal(err)
	}
	var person, agent, role, project, computer, request, key string
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for _, v := range []struct {
			kind string
			id   *string
		}{{"person", &person}, {"agent", &agent}} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,'Scope fixture') RETURNING id::text`, tid, v.kind).Scan(v.id); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1 AND key='member'`, tid).Scan(&role); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'SCOPE','Scope project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash) VALUES($1,$2,'Scope key','scope-fixture','not-a-credential') RETURNING id::text`, tid, agent).Scan(&key); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest) VALUES($1,gen_random_uuid(),'123456789',repeat('a',64),repeat('b',64),repeat('c',64),'{}','fixture') RETURNING id::text`, tid).Scan(&request); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash) VALUES($1,gen_random_uuid(),$2,$3,$4,'scope-computer',repeat('c',64)) RETURNING id::text`, tid, request, agent, key).Scan(&computer)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"workspace", "project", "node", "computer", "invalid"} {
		for _, hasNode := range []bool{false, true} {
			for _, hasComputer := range []bool{false, true} {
				var nodeID, computerID any
				if hasNode {
					nodeID = project
				}
				if hasComputer {
					computerID = computer
				}
				valid := scope == "workspace" && !hasNode && !hasComputer || (scope == "project" || scope == "node") && hasNode && !hasComputer || scope == "computer" && !hasNode && hasComputer
				tx, err := d.Admin.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id,computer_id) VALUES($1,$2,$3,$4,$5,$6)`, tid, person, role, scope, nodeID, computerID)
				_ = tx.Rollback(ctx)
				if valid && err != nil {
					t.Fatalf("valid %s node=%v computer=%v: %v", scope, hasNode, hasComputer, err)
				}
				var pe *pgconn.PgError
				if !valid && (!errors.As(err, &pe) || pe.Code != "23514") {
					t.Fatalf("invalid %s node=%v computer=%v: wanted check violation, got %v", scope, hasNode, hasComputer, err)
				}
			}
		}
	}
	p := tenant.Principal{ID: person, TenantID: tid, Kind: tenant.Person}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for _, scope := range []string{"computer", "node"} {
			var nodeID, computerID any
			if scope == "node" {
				nodeID = project
			} else {
				computerID = computer
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id,computer_id) VALUES($1,$2,$3,$4,$5,$6)`, tid, person, role, scope, nodeID, computerID); err != nil {
				return err
			}
		}
		g, err := readGrants(ctx, tx, p)
		if err != nil {
			return err
		}
		if len(g.projects) != 0 || len(g.anyProject) != 0 || len(g.workspace) != 0 || g.allows("nodes.read", project) {
			t.Fatalf("expanded scope granted project access: %+v", g)
		}
		var visibility string
		if err := tx.QueryRow(ctx, `SELECT aeon_principal_visibility($1,$2)`, tid, person).Scan(&visibility); err != nil {
			return err
		}
		if visibility != "{}" {
			t.Fatalf("SQL project visibility leaked: %s", visibility)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, tid, person, role, project); err != nil {
			return err
		}
		g, err = readGrants(ctx, tx, p)
		if err != nil {
			return err
		}
		if !g.allows("nodes.read", project) || !contains(g.anyProject, "nodes.read") {
			t.Fatal("existing project grant lost")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tid, person, role); err != nil {
			return err
		}
		g, err = readGrants(ctx, tx, p)
		if err == nil && !contains(g.workspace, "nodes.read") {
			t.Fatal("existing workspace grant lost")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

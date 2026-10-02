// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestEffectiveRouteMatrix(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('az1-matrix','AZ1 matrix') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	people := map[string]tenant.Principal{}
	for role, classic := range map[string]string{"owner": "super_admin", "admin": "admin", "member": "member", "viewer": "viewer", "guest": "external", "customer": "customer"} {
		p := tenant.Principal{TenantID: tid, Kind: tenant.Person, Roles: []string{classic}}
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person',$2,ARRAY[$3]) RETURNING id::text`, tid, role, classic).Scan(&p.ID); err != nil {
			t.Fatal(err)
		}
		dbtest.BindLegacy(t, d, tid, p.ID)
		if role == "viewer" {
			if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='viewer'`, tid, p.ID); err != nil {
				t.Fatal(err)
			}
		}
		people[role] = p
	}
	// Guest is a project role (ADR-003 P2): external people get no workspace
	// binding, only project bindings.
	var projectID string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1::uuid,id,'MAT-1','Matrix' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project' RETURNING id::text`, tid).Scan(&projectID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='guest'`, tid, people["guest"].ID, projectID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checkIn := func(name string, p tenant.Principal, pattern string, scope Scope, want bool) {
		t.Helper()
		err := RequirePattern(BindPool(tenant.WithPrincipal(ctx, p), d.App), pattern, scope)
		if (err == nil) != want {
			t.Errorf("%s %s %+v: got %v, want allowed=%v", name, pattern, scope, err, want)
		}
	}
	check := func(name string, p tenant.Principal, pattern string, want bool) {
		t.Helper()
		checkIn(name, p, pattern, Scope{}, want)
	}
	for _, tc := range []struct {
		role, route string
		allow       bool
	}{
		{"owner", "POST /api/nodes", true}, {"owner", "POST /api/roles", true},
		{"admin", "POST /api/roles", true}, {"admin", "POST /api/nodes", true},
		{"member", "POST /api/nodes", true}, {"member", "POST /api/kinds", false},
		{"member", "POST /api/roles", false}, {"member", "POST /api/approvals/{approvalId}/decision", true},
		{"viewer", "GET /api/nodes", true}, {"viewer", "POST /api/nodes", false},
		{"guest", "GET /api/nodes", false}, {"guest", "POST /api/nodes/{nodeId}/comments", false},
		{"guest", "GET /api/agent-keys", false},
		{"customer", "GET /api/quotes/{quoteId}/versions/{version}", true},
		{"customer", "POST /api/quotes/{quoteId}/versions/{version}/accept", true},
		{"customer", "GET /api/quotes", false},
		{"customer", "GET /api/me/profile", true},
	} {
		check(tc.role, people[tc.role], tc.route, tc.allow)
	}
	guest := people["guest"]
	check("project-only guest", guest, "GET /api/me", true)
	checkIn("guest", guest, "GET /api/nodes", Scope{AnyProject: true}, true)
	checkIn("guest", guest, "GET /api/me", Scope{AnyProject: true}, true)
	// AEON-184: who is working where follows project visibility, so a
	// project-only guest may ask; a customer never may.
	checkIn("guest", guest, "GET /api/harness-sessions/live", Scope{AnyProject: true}, true)
	reader := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'person','Project-only session reader') RETURNING id::text`, tid).Scan(&reader.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='viewer'`, tid, reader.ID, projectID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"GET /api/me/host-labels", "PUT /api/me/host-labels"} {
		check("project reader without project scope", reader, pattern, false)
		checkIn("project reader", reader, pattern, Scope{AnyProject: true}, true)
		// Seeing the guest live count alone does not grant harness.read.
		checkIn("guest", guest, pattern, Scope{AnyProject: true}, false)
		checkIn("customer", people["customer"], pattern, Scope{AnyProject: true}, false)
		if !ProjectFilteredRoutes[pattern] {
			t.Fatalf("own host labels must support project-only people: %s", pattern)
		}
	}
	checkIn("customer", people["customer"], "GET /api/harness-sessions/live", Scope{AnyProject: true}, false)
	check("member", people["member"], "GET /api/usage/dashboard", true)
	checkIn("guest", guest, "GET /api/usage/dashboard", Scope{AnyProject: true}, false)
	check("customer", people["customer"], "GET /api/usage/dashboard", false)
	checkIn("guest", guest, "GET /api/me/permissions", Scope{AnyProject: true}, true)
	checkIn("guest", guest, "POST /api/nodes/{nodeId}/comments", Scope{ProjectID: projectID}, true)
	checkIn("guest", guest, "PATCH /api/nodes/{nodeId}", Scope{ProjectID: projectID}, false)
	checkIn("guest", guest, "GET /api/members", Scope{AnyProject: true}, false)
	checkIn("guest", guest, "POST /api/nodes/{nodeId}/comments", Scope{ProjectID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, false)
	checkIn("customer", people["customer"], "GET /api/nodes", Scope{AnyProject: true}, false)
	var agent tenant.Principal
	agent.TenantID, agent.Kind = tid, tenant.Agent
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'agent','Matrix agent') RETURNING id::text`, tid).Scan(&agent.ID); err != nil {
			return err
		}
		var roleID string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'matrix_agent','Matrix agent') RETURNING id::text`, tid).Scan(&roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'nodes.read'),($1::uuid,$2::uuid,'nodes.write')`, tid, roleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, tid, agent.ID, roleID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	agent.KeyCreatorID = people["admin"].ID
	check("agent without scopes", agent, "GET /api/me", true)
	check("agent without scopes", agent, "GET /api/nodes", false)
	agent.Scopes = []string{"nodes.read"}
	check("agent read scope", agent, "GET /api/nodes", true)
	check("agent read scope", agent, "POST /api/nodes", false)
	agent.Scopes = []string{"nodes.read", "nodes.write"}
	check("agent write scope", agent, "POST /api/nodes", true)
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1::uuid AND key='viewer') WHERE principal_id=$2::uuid AND scope_type='workspace'`, tid, people["admin"].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check("creator demoted", agent, "POST /api/nodes", false)
	check("creator demoted", agent, "GET /api/nodes", true)
	if _, err := d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, people["viewer"].ID); err != nil {
		t.Fatal(err)
	}
	check("deactivated viewer", people["viewer"], "GET /api/nodes", false)
}

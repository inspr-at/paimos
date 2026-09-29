// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestCreateAgentAndFirstKey(t *testing.T) {
	m, owner := keyFixture(t)
	ctx := dbtest.Seed(t.Context())
	mux := http.NewServeMux()
	authz.New(m.pool).Mount(mux)
	call := func(method, path string, body any, p tenant.Principal) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	var viewer, member, guest, projectA, projectB, foreignRole, foreignProject string
	manager := tenant.Principal{Kind: tenant.Person, TenantID: owner.TenantID}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		for key, target := range map[string]*string{"viewer": &viewer, "member": &member, "guest": &guest} {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE key=$1`, key).Scan(target); err != nil {
				return err
			}
		}
		for key, target := range map[string]*string{"PRJ-1": &projectA, "PRJ-2": &projectB} {
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,title,state,kind_id) SELECT $1::uuid,$2,$2,'active',id FROM node_kinds WHERE slug='project' RETURNING id::text`, owner.TenantID, key).Scan(target); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Key manager','{}') RETURNING id::text`, owner.TenantID).Scan(&manager.ID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'key_manager','Key manager') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,$1::uuid,permission FROM role_permissions WHERE role_id=$2::uuid`, role, viewer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'keys.manage')`, owner.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, owner.TenantID, manager.ID, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var otherTenant string
	if err := db.InTenant(ctx, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('agent-other','Other') RETURNING id::text`).Scan(&otherTenant)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, m.pool, otherTenant, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE key='viewer'`).Scan(&foreignRole); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,title,state,kind_id) SELECT $1::uuid,'FOR-1','Foreign','active',id FROM node_kinds WHERE slug='project' RETURNING id::text`, otherTenant).Scan(&foreignProject)
	}); err != nil {
		t.Fatal(err)
	}
	create := func(body any, who tenant.Principal, status int) authz.AgentMember {
		t.Helper()
		w := call("POST", "/api/members/agents", body, who)
		if w.Code != status {
			t.Fatalf("agent creation status %d, want %d: %s", w.Code, status, w.Body.String())
		}
		var out authz.AgentMember
		if status == 201 {
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("agent response cacheable")
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	// keys.manage suffices; this person has no members.manage.
	created := create(map[string]any{"name": "CLI helper", "description": "Build reports", "workspace_role_id": viewer}, manager, 201)
	if created.WorkspaceRole == nil || created.WorkspaceRole.ID != viewer || created.Description != "Build reports" || created.KeyCount != 0 {
		t.Fatal("agent details lost")
	}
	key := decodeKey(t, keyRequest(m, manager, map[string]any{"principal_id": created.PrincipalID, "scopes": []string{"nodes.read"}}))
	if key.PrincipalID != created.PrincipalID {
		t.Fatal("key belongs to a different agent")
	}
	if w := keyRequest(m, owner, map[string]any{"principal_id": created.PrincipalID, "scopes": []string{"nodes.write"}}); w.Code != 403 {
		t.Fatalf("role ceiling status %d", w.Code)
	}
	create(map[string]any{"name": "cli HELPER", "workspace_role_id": viewer}, owner, 409)
	create(map[string]any{"name": "Escalation", "workspace_role_id": member}, manager, 403)
	create(map[string]any{"name": "Guest role", "workspace_role_id": guest}, owner, 400)
	create(map[string]any{"name": "Foreign role", "workspace_role_id": foreignRole}, owner, 404)
	create(map[string]any{"name": "No role"}, owner, 400)
	create(map[string]any{"name": " ", "workspace_role_id": viewer}, owner, 400)
	create(map[string]any{"name": "Long description", "description": strings.Repeat("x", 1001), "workspace_role_id": viewer}, owner, 400)
	projectRoles := func(project, role string) []map[string]string {
		return []map[string]string{{"project_id": project, "role_id": role}}
	}
	beforeKeys, beforeEvents := keyCounts(t, m, owner)
	create(map[string]any{"name": "Rollback", "workspace_role_id": viewer, "project_roles": projectRoles(foreignProject, viewer)}, owner, 404)
	afterKeys, afterEvents := keyCounts(t, m, owner)
	if beforeKeys != afterKeys || beforeEvents != afterEvents {
		t.Fatal("failed creation wrote keys or audit")
	}
	projectAgent := create(map[string]any{"name": "Project helper", "project_roles": projectRoles(projectA, viewer)}, owner, 201)
	if projectAgent.WorkspaceRole != nil || len(projectAgent.ProjectRoles) != 1 {
		t.Fatal("project-only access lost")
	}
	projectKey := decodeKey(t, keyRequest(m, owner, map[string]any{"principal_id": projectAgent.PrincipalID, "scopes": []string{"nodes.read"}}))
	principal := tenant.Principal{ID: projectAgent.PrincipalID, TenantID: owner.TenantID, Kind: tenant.Agent, Scopes: projectKey.Scopes, KeyCreatorID: owner.ID}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, principal, "nodes.read", authz.Scope{ProjectID: projectA}); err != nil {
			t.Fatal("selected project refused")
		}
		for _, scope := range []authz.Scope{{}, {ProjectID: projectB}} {
			if err := authz.RequireTx(ctx, tx, principal, "nodes.read", scope); err == nil {
				t.Fatal("project key escaped its project")
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE principal_id=$1::uuid AND scope_type='workspace'`, projectAgent.PrincipalID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("first key added workspace access")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, projectKey.ID, nil)
	if err != nil || !slices.Contains(view.Grantable, "nodes.read") {
		t.Fatalf("project key scopes unavailable: %v", err)
	}
	// Even a forged agent request with keys.manage cannot create an agent.
	forged := owner
	forged.Kind = tenant.Agent
	forged.Scopes = []string{"keys.manage"}
	create(map[string]any{"name": "Agent creates agent", "workspace_role_id": viewer}, forged, 403)
	guestPerson := tenant.Principal{Kind: tenant.Person, TenantID: owner.TenantID}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Guest','{}') RETURNING id::text`, owner.TenantID).Scan(&guestPerson.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1::uuid,$2::uuid,$3::uuid,'project',$4::uuid)`, owner.TenantID, guestPerson.ID, guest, projectA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	create(map[string]any{"name": "Guest creates agent", "workspace_role_id": viewer}, guestPerson, 403)
	// Directory persists description/project roles; audit names both creations.
	directory := call("GET", "/api/members", nil, owner)
	if directory.Code != 200 || !strings.Contains(directory.Body.String(), "Build reports") || !strings.Contains(directory.Body.String(), projectA) {
		t.Fatal("directory lost agent details")
	}
	audit := call("GET", "/api/audit?category=access", nil, owner)
	if audit.Code != 200 || !strings.Contains(audit.Body.String(), "principal.agent_created") || !strings.Contains(audit.Body.String(), "agent_key.created") || strings.Contains(audit.Body.String(), key.Token) {
		t.Fatal("creation audit missing or leaked key")
	}
	// Removing all bindings must never revive the legacy auto-grant path.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1::uuid`, projectAgent.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := keyRequest(m, owner, map[string]any{"principal_id": projectAgent.PrincipalID, "scopes": []string{"nodes.read"}}); w.Code != 403 {
		t.Fatal("removed project binding acquired workspace grant")
	}
}

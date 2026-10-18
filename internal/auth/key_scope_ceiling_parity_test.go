// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The client reads this fixture too. Delivery joined the registry and the
// built-in agent exclusion list together; the shared fixture has to carry
// both or the client ceiling drifts from the server.
func TestDeliveryBuiltinAdminCeilingFixture(t *testing.T) {
	var fixture struct {
		Registry []authz.Permission `json:"registry"`
		Cases    []struct {
			BuiltinRole         string     `json:"builtin_role"`
			Workspace           []string   `json:"workspace"`
			Projects            [][]string `json:"projects"`
			ProjectBuiltinRoles []string   `json:"project_builtin_roles"`
			Want                []string   `json:"want"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/key_scope_ceiling.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Registry) != len(authz.Registry) {
		t.Fatalf("client/server parity registry size drift: fixture=%d server=%d", len(fixture.Registry), len(authz.Registry))
	}
	for _, permission := range fixture.Registry {
		actual, ok := authz.Lookup(permission.Key)
		if !ok || actual.AgentGrantable != permission.AgentGrantable || !slices.Equal(actual.GrantableAt, permission.GrantableAt) {
			t.Fatalf("client/server parity registry drift: %s", permission.Key)
		}
	}
	read, readOK := authz.Lookup("delivery.read")
	manage, manageOK := authz.Lookup("delivery.manage")
	if !readOK || !manageOK || !read.AgentGrantable || !manage.AgentGrantable || !slices.Equal(read.GrantableAt, []string{"workspace", "project"}) || !slices.Equal(manage.GrantableAt, []string{"workspace", "project"}) {
		t.Fatal("delivery permissions drifted from the agent-grantable catalog")
	}
	var sawRead, sawManage bool
	for _, permission := range fixture.Registry {
		if permission.Key == "delivery.read" && permission.AgentGrantable && slices.Equal(permission.GrantableAt, read.GrantableAt) {
			sawRead = true
		}
		if permission.Key == "delivery.manage" && permission.AgentGrantable && slices.Equal(permission.GrantableAt, manage.GrantableAt) {
			sawManage = true
		}
	}
	if !sawRead || !sawManage {
		t.Fatal("shared ceiling fixture omitted delivery.read or delivery.manage")
	}
	for _, tc := range fixture.Cases {
		if tc.BuiltinRole != "" {
			permissions, ok := authz.BuiltinPermissions(tc.BuiltinRole)
			if !ok || !slices.Equal(permissions, tc.Workspace) {
				t.Fatalf("client/server built-in role permissions drift: %s", tc.BuiltinRole)
			}
		}
		for i, role := range tc.ProjectBuiltinRoles {
			permissions, ok := authz.BuiltinPermissions(role)
			if !ok || i >= len(tc.Projects) || !slices.Equal(permissions, tc.Projects[i]) {
				t.Fatalf("client/server built-in project role permissions drift: %s", role)
			}
		}
		builtin := tc.BuiltinRole != "" || len(tc.ProjectBuiltinRoles) > 0
		if builtin && (!slices.Contains(tc.Want, "delivery.read") || slices.Contains(tc.Want, "delivery.manage")) {
			t.Fatalf("built-in agent ceiling must keep delivery.read and exclude delivery.manage: %s", tc.BuiltinRole+strings.Join(tc.ProjectBuiltinRoles, ","))
		}
	}
}

// The client reads this fixture too. Verify its catalog against the real
// registry, then exercise the ceiling through live bindings and key writes.
func TestKeyScopeCeilingParityCreateEditRotate(t *testing.T) {
	var fixture struct {
		Registry []authz.Permission `json:"registry"`
		Cases    []struct {
			Name                string     `json:"name"`
			Workspace           []string   `json:"workspace"`
			Projects            [][]string `json:"projects"`
			Want                []string   `json:"want"`
			PrivateRole         bool       `json:"private_role"`
			RotationWant        []string   `json:"rotation_want"`
			BuiltinRole         string     `json:"builtin_role"`
			ProjectBuiltinRoles []string   `json:"project_builtin_roles"`
			Outside             string     `json:"outside_ceiling"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/key_scope_ceiling.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Registry) != len(authz.Registry) {
		t.Fatalf("client/server parity registry size drift: fixture=%d server=%d", len(fixture.Registry), len(authz.Registry))
	}
	for _, permission := range fixture.Registry {
		actual, ok := authz.Lookup(permission.Key)
		if !ok || actual.AgentGrantable != permission.AgentGrantable || !slices.Equal(actual.GrantableAt, permission.GrantableAt) {
			t.Fatalf("client/server parity registry drift: %s", permission.Key)
		}
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			outside := tc.Outside
			if outside == "" {
				outside = "models.read"
			}
			if permission, ok := authz.Lookup(outside); !ok || !permission.AgentGrantable || slices.Contains(tc.Want, outside) {
				t.Fatal("fixture requires an agent-grantable permission outside its ceiling")
			}
			if tc.BuiltinRole != "" {
				permissions, ok := authz.BuiltinPermissions(tc.BuiltinRole)
				if !ok || !slices.Equal(permissions, tc.Workspace) {
					t.Fatal("client/server built-in role permissions drift")
				}
			}
			if tc.ProjectBuiltinRoles != nil && len(tc.ProjectBuiltinRoles) != len(tc.Projects) {
				t.Fatal("fixture requires one built-in role key per project binding")
			}
			for i, role := range tc.ProjectBuiltinRoles {
				permissions, ok := authz.BuiltinPermissions(role)
				if !ok || !slices.Equal(permissions, tc.Projects[i]) {
					t.Fatalf("client/server built-in project role permissions drift: %s", role)
				}
			}
			m, owner := keyFixture(t)
			ctx := dbtest.Seed(t.Context())
			agent := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Agent}
			var serverCeiling []string
			if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,agent_access_configured)
					VALUES($1::uuid,'agent','Ceiling worker',true) RETURNING id::text`, owner.TenantID).Scan(&agent.ID); err != nil {
					return err
				}
				bind := func(name, projectID string, permissions []string) error {
					var roleID string
					if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,$2,$2) RETURNING id::text`, owner.TenantID, name).Scan(&roleID); err != nil {
						return err
					}
					for _, permission := range permissions {
						if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,$3)`, owner.TenantID, roleID, permission); err != nil {
							return err
						}
					}
					scope := "workspace"
					var scopeID any
					if projectID != "" {
						scope, scopeID = "project", projectID
					}
					_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
						VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid)`, owner.TenantID, agent.ID, roleID, scope, scopeID)
					return err
				}
				if tc.Workspace != nil {
					if tc.BuiltinRole != "" {
						if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
							SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE key=$3 AND builtin`, owner.TenantID, agent.ID, tc.BuiltinRole); err != nil {
							return err
						}
					} else {
						name := "ceiling_workspace"
						if tc.PrivateRole {
							name = "agent_" + strings.ReplaceAll(agent.ID, "-", "")
						}
						if err := bind(name, "", tc.Workspace); err != nil {
							return err
						}
					}
				}
				for i, permissions := range tc.Projects {
					var projectID string
					if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,title,state,kind_id)
						SELECT $1::uuid,$2,'Ceiling project','active',id FROM node_kinds WHERE slug='project'
						RETURNING id::text`, owner.TenantID, fmt.Sprintf("CEIL-%d", i+1)).Scan(&projectID); err != nil {
						return err
					}
					if tc.ProjectBuiltinRoles != nil {
						var roleID string
						if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key=$2 AND builtin`, owner.TenantID, tc.ProjectBuiltinRoles[i]).Scan(&roleID); err != nil {
							return err
						}
						if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
							VALUES($1::uuid,$2::uuid,$3::uuid,'project',$4::uuid)`, owner.TenantID, agent.ID, roleID, projectID); err != nil {
							return err
						}
					} else if err := bind(fmt.Sprintf("ceiling_project_%d", i), projectID, permissions); err != nil {
						return err
					}
				}
				ceiling, err := authz.AgentKeyCeilingTx(ctx, tx, agent)
				if err != nil {
					return err
				}
				for _, permission := range authz.Registry {
					if permission.AgentGrantable && slices.Contains(ceiling, permission.Key) {
						serverCeiling = append(serverCeiling, permission.Key)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(serverCeiling, tc.Want) {
				t.Fatalf("server ceiling = %v, parity fixture = %v", serverCeiling, tc.Want)
			}
			beforeAccess := rotationAccessSnapshot(t, m, owner)
			key := decodeKey(t, keyRequest(m, owner, map[string]any{"principal_id": agent.ID, "scopes": tc.Want}))
			view, err := m.agentKeyScopes(tenant.WithPrincipal(ctx, owner), owner, key.ID, nil)
			if err != nil || !slices.Equal(view.Grantable, tc.Want) {
				t.Fatal("edit ceiling disagrees with creation/client ceiling")
			}
			if len(tc.Want) > 0 {
				if _, err := m.agentKeyScopes(tenant.WithPrincipal(ctx, owner), owner, key.ID, &keyScopeDelta{Remove: tc.Want[:1]}); err != nil {
					t.Fatal(err)
				}
				view, err := m.agentKeyScopes(tenant.WithPrincipal(ctx, owner), owner, key.ID, &keyScopeDelta{Add: tc.Want})
				if err != nil || view.Key.ID != key.ID || !slices.Equal(slices.Sorted(slices.Values(view.Key.Scopes)), tc.Want) {
					t.Fatal("Full access edit failed or changed key identity")
				}
				key.Scopes = view.Key.Scopes
			}
			prefix, secret, _ := parseBearer("Bearer " + key.Token)
			if p, ok, err := m.authenticateAgent(ctx, prefix, secret); err != nil || !ok || !slices.Equal(p.Scopes, key.Scopes) {
				t.Fatal("Full access edit changed bearer or lost scopes")
			}
			beforeKeys, beforeEvents := keyCounts(t, m, owner)
			// Known agent-grantable, but outside this fixture's live ceiling.
			if w := keyRequest(m, owner, map[string]any{"principal_id": agent.ID, "scopes": []string{outside}}); w.Code != http.StatusForbidden {
				t.Fatalf("outside-ceiling creation status = %d", w.Code)
			}
			if w := scopesRequest(m, owner, key.ID, http.MethodPatch, fmt.Sprintf(`{"add":[%q]}`, outside)); w.Code != http.StatusForbidden {
				t.Fatalf("outside-ceiling edit status = %d", w.Code)
			}
			for _, tc := range []struct {
				scope  string
				status int
			}{{outside, http.StatusForbidden}, {"keys.read", http.StatusBadRequest}, {"model_prefs.manage", http.StatusBadRequest}, {"retired.scope", http.StatusBadRequest}} {
				if w := keyRequest(m, owner, map[string]any{"rotate_key_id": key.ID, "rotation_scopes": []string{tc.scope}}); w.Code != tc.status {
					t.Fatalf("outside-ceiling rotation status = %d for %s", w.Code, tc.scope)
				}
			}
			rotationScopes := key.Scopes
			body := map[string]any{"rotate_key_id": key.ID}
			if tc.RotationWant != nil {
				rotationScopes = tc.RotationWant
				// Preserving a key must not bypass the private role's narrower cap.
				if w := keyRequest(m, owner, body); w.Code != http.StatusForbidden {
					t.Fatalf("preserved private-role rotation status = %d", w.Code)
				}
				for _, scope := range tc.Want {
					if slices.Contains(rotationScopes, scope) {
						continue
					}
					if w := keyRequest(m, owner, map[string]any{"rotate_key_id": key.ID, "rotation_scopes": []string{scope}}); w.Code != http.StatusForbidden {
						t.Fatalf("project-only private-role rotation status = %d for %s", w.Code, scope)
					}
				}
				body["rotation_scopes"] = rotationScopes
			}
			keys, events := keyCounts(t, m, owner)
			if keys != beforeKeys || events != beforeEvents || rotationAccessSnapshot(t, m, owner) != beforeAccess {
				t.Fatal("creation, edits or rejected rotations changed grants or leaked key/audit writes")
			}
			next := decodeKey(t, keyRequest(m, owner, body))
			if next.ID == key.ID || next.Token == key.Token || next.PrincipalID != key.PrincipalID || !slices.Equal(next.Scopes, rotationScopes) {
				t.Fatal("rotation did not preserve the allowed scope set and agent")
			}
			keys, events = keyCounts(t, m, owner)
			if keys != beforeKeys+1 || events != beforeEvents+2 || rotationAccessSnapshot(t, m, owner) != beforeAccess {
				t.Fatal("rotation changed grants or failed atomic replacement/revocation")
			}
			if _, ok, err := m.authenticateAgent(ctx, prefix, secret); err != nil || ok {
				t.Fatal("rotation did not revoke the old bearer")
			}
		})
	}
}

// Builtin role lists in the shared fixture must match the live catalog before
// any database ceiling is built. reviewpolicy.manage stays on the person role
// and out of the agent want; reviewpolicy.read stays in both.
func TestKeyScopeCeilingFixtureMatchesBuiltinRoles(t *testing.T) {
	var fixture struct {
		Registry []authz.Permission `json:"registry"`
		Cases    []struct {
			Name                string     `json:"name"`
			Workspace           []string   `json:"workspace"`
			Projects            [][]string `json:"projects"`
			Want                []string   `json:"want"`
			BuiltinRole         string     `json:"builtin_role"`
			ProjectBuiltinRoles []string   `json:"project_builtin_roles"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/key_scope_ceiling.json")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &fixture) != nil || len(fixture.Registry) != len(authz.Registry) {
		t.Fatal("ceiling fixture registry drifted from the live catalog")
	}
	for _, permission := range fixture.Registry {
		actual, ok := authz.Lookup(permission.Key)
		if !ok || actual.AgentGrantable != permission.AgentGrantable || !slices.Equal(actual.GrantableAt, permission.GrantableAt) {
			t.Fatalf("ceiling fixture registry drift: %s", permission.Key)
		}
	}
	sawRead, sawManage := false, false
	for _, tc := range fixture.Cases {
		if tc.BuiltinRole != "" {
			permissions, ok := authz.BuiltinPermissions(tc.BuiltinRole)
			if !ok || !slices.Equal(permissions, tc.Workspace) {
				t.Fatalf("%s workspace drifted from built-in %s", tc.Name, tc.BuiltinRole)
			}
		}
		for i, role := range tc.ProjectBuiltinRoles {
			permissions, ok := authz.BuiltinPermissions(role)
			if !ok || !slices.Equal(permissions, tc.Projects[i]) {
				t.Fatalf("%s project drifted from built-in %s", tc.Name, role)
			}
		}
		if tc.BuiltinRole == "" && len(tc.ProjectBuiltinRoles) == 0 {
			continue
		}
		if !slices.Contains(tc.Want, "reviewpolicy.read") || slices.Contains(tc.Want, "reviewpolicy.manage") {
			t.Fatalf("%s agent ceiling must read review policy without managing it", tc.Name)
		}
		sawRead = true
		person := tc.Workspace
		if len(tc.ProjectBuiltinRoles) > 0 {
			person = tc.Projects[0]
		}
		if slices.Contains(person, "reviewpolicy.manage") {
			sawManage = true
		}
	}
	if !sawRead || !sawManage {
		t.Fatal("fixture lost the built-in review-policy split")
	}
	manage, ok := authz.Lookup("reviewpolicy.manage")
	if !ok || !manage.AgentGrantable {
		t.Fatal("reviewpolicy.manage must stay agent-grantable for a custom role")
	}
}

func TestEngineAdmissionCeilingFixtureExcludesBuiltinAgents(t *testing.T) {
	// Risk: engine.admission, engine.read and engine.manage stay on the person
	// and off every built-in agent key. The shared client fixture has to carry
	// both halves or the key editor drifts from the server.
	keys := []string{"engine.admission", "engine.read", "engine.manage"}
	var fixture struct {
		Registry []authz.Permission `json:"registry"`
		Cases    []struct {
			Name                string     `json:"name"`
			Workspace           []string   `json:"workspace"`
			Projects            [][]string `json:"projects"`
			Want                []string   `json:"want"`
			BuiltinRole         string     `json:"builtin_role"`
			ProjectBuiltinRoles []string   `json:"project_builtin_roles"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/key_scope_ceiling.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Registry) != len(authz.Registry) {
		t.Fatalf("fixture registry %d, live catalog %d", len(fixture.Registry), len(authz.Registry))
	}
	for _, key := range keys {
		live, ok := authz.Lookup(key)
		if !ok || !live.AgentGrantable || !slices.Equal(live.GrantableAt, []string{"workspace", "project"}) {
			t.Fatalf("%s drifted from the agent-grantable project catalog", key)
		}
		var saw bool
		for _, permission := range fixture.Registry {
			if permission.Key == key && permission.AgentGrantable && slices.Equal(permission.GrantableAt, live.GrantableAt) {
				saw = true
			}
		}
		if !saw {
			t.Fatalf("shared ceiling fixture omitted %s", key)
		}
	}
	var sawBuiltin bool
	for _, tc := range fixture.Cases {
		person := tc.Workspace
		role := tc.BuiltinRole
		if len(tc.ProjectBuiltinRoles) > 0 {
			person = tc.Projects[0]
			role = tc.ProjectBuiltinRoles[0]
		}
		if role == "" {
			continue
		}
		live, ok := authz.BuiltinPermissions(role)
		if !ok || !slices.Equal(person, live) {
			t.Fatalf("%s person list drifted from built-in %s", tc.Name, role)
		}
		switch role {
		case "owner", "admin":
			if !slices.Contains(person, "engine.admission") || !slices.Contains(person, "engine.read") || !slices.Contains(person, "engine.manage") {
				t.Fatalf("%s must keep shadow admission on the person", role)
			}
		case "member":
			if !slices.Contains(person, "engine.admission") || !slices.Contains(person, "engine.read") || slices.Contains(person, "engine.manage") {
				t.Fatal("members evaluate and read shadow admission and cannot manage it")
			}
		}
		for _, key := range keys {
			if slices.Contains(tc.Want, key) {
				t.Fatalf("%s built-in agent ceiling must exclude %s", tc.Name, key)
			}
		}
		sawBuiltin = true
	}
	if !sawBuiltin {
		t.Fatal("fixture lost the built-in shadow-admission split")
	}
}

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

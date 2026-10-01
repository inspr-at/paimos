// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func keyScopeRole(t *testing.T, m *Module, owner tenant.Principal, key agentKeyCreatedJSON) *authz.Role {
	t.Helper()
	view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
	if err != nil || view.AgentRole == nil {
		t.Fatal("missing agent role metadata")
	}
	return view.AgentRole
}

func keyScopeEditor(t *testing.T, m *Module, owner tenant.Principal, permissions ...string) tenant.Principal {
	t.Helper()
	editor := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person}
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Scope editor') RETURNING id::text`, owner.TenantID).Scan(&editor.ID); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'scope_editor','Scope editor') RETURNING id::text`, owner.TenantID).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1::uuid,$2::uuid,unnest($3::text[])`, owner.TenantID, id, permissions); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, editor.ID, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return editor
}

func roleGrantBody(roleID string, add, roleAdd []string) string {
	body, _ := json.Marshal(keyScopeDelta{Add: add, RoleExtension: &keyRoleExtension{RoleID: roleID, Add: roleAdd}})
	return string(body)
}

func TestKeyScopeRoleGrantAtomicSharedAndImmediate(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "grant-worker", "scopes": []string{"nodes.read"}}))
	role := keyScopeRole(t, m, owner, key)
	if !slices.Contains(role.Permissions, "models.read") || slices.Contains(key.Scopes, "models.read") {
		t.Fatal("generated role baseline must not silently expand key scopes")
	}
	other := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "shared-worker", "scopes": []string{"nodes.read"}}))
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace'`, other.PrincipalID, role.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
	if err != nil || view.AgentRole.MemberCount != 2 || !slices.Contains(view.RoleGrantable, "nodes.write") || slices.Contains(view.Grantable, "nodes.write") {
		t.Fatal("incorrect shared-role extension metadata")
	}
	keysBefore, eventsBefore := keyCounts(t, m, owner)
	body := roleGrantBody(role.ID, []string{"nodes:write"}, []string{"nodes.write"})
	if w := scopesRequest(m, owner, key.ID, http.MethodPatch, body); w.Code != 200 {
		t.Fatalf("role grant status %d", w.Code)
	}
	keysAfter, eventsAfter := keyCounts(t, m, owner)
	if keysAfter != keysBefore || eventsAfter != eventsBefore+1 {
		t.Fatal("role grant must write exactly one event without rotating the key")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var complete bool
		err := tx.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(actor_principal_id=$1::uuid AND before->>'key_id'=$2 AND after->>'key_id'=$2 AND before->'role'->>'id'=$3 AND after->'role'->>'id'=$3 AND NOT (before->'role'->'permissions' ? 'nodes.write') AND after->'role'->'permissions' ? 'nodes.write' AND after->'scopes' ? 'nodes.write' AND NOT (after ? 'token') AND NOT (after ? 'hash')) FROM events WHERE type='agent_key.scopes_changed'`, owner.ID, key.ID, role.ID).Scan(&complete)
		if err == nil && !complete {
			t.Error("combined audit does not name both changes")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key     agentKeyCreatedJSON
		allowed bool
	}{{key, true}, {other, false}} {
		prefix, secret, _ := parseBearer("Bearer " + tc.key.Token)
		principal, ok, err := m.authenticateAgent(ctx, prefix, secret)
		if err != nil || !ok {
			t.Fatal("existing key stopped authenticating")
		}
		effective, err := authz.Load(tenant.WithPrincipal(ctx, principal), m.pool, principal, "")
		if err != nil || !slices.Contains(effective.Workspace.Permissions, "nodes.write") {
			t.Fatal("shared role permission missing")
		}
		err = authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, principal), m.pool), "nodes.write", authz.Scope{})
		if (err == nil) != tc.allowed {
			t.Fatal("other key scopes were widened or original key did not take effect")
		}
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", body); w.Code != 200 {
		t.Fatalf("idempotent retry status %d", w.Code)
	}
	_, finalEvents := keyCounts(t, m, owner)
	if finalEvents != eventsAfter {
		t.Fatal("idempotent role grant created an audit event")
	}
}

func TestKeyScopeRoleGrantAuthorizationAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		permissions                    []string
		add, roleAdd                   []string
		wrongRole, builtin, creatorCap bool
		want                           int
	}{
		{name: "nonowner with roles.manage", permissions: []string{"keys.manage", "roles.manage", "nodes.read", "nodes.write"}, add: []string{"nodes.write"}, roleAdd: []string{"nodes.write"}, want: 200},
		{name: "no roles.manage", permissions: []string{"keys.manage", "nodes.read", "nodes.write"}, add: []string{"nodes.write"}, roleAdd: []string{"nodes.write"}, want: 403},
		{name: "editor lacks permission", permissions: []string{"keys.manage", "roles.manage", "nodes.read"}, add: []string{"nodes.write"}, roleAdd: []string{"nodes.write"}, want: 403},
		{name: "creator lacks permission", creatorCap: true, add: []string{"nodes.write"}, roleAdd: []string{"nodes.write"}, want: 403},
		{name: "unrelated role", wrongRole: true, add: []string{"nodes.write"}, roleAdd: []string{"nodes.write"}, want: 403},
		{name: "builtin immutable", builtin: true, add: []string{"nodes.write"}, roleAdd: []string{"nodes.write"}, want: 403},
		{name: "role grant absent from key delta", add: []string{"nodes.write"}, roleAdd: []string{"knowledge.write"}, want: 400},
		{name: "human governance", add: []string{"keys.manage"}, roleAdd: []string{"keys.manage"}, want: 400},
		{name: "unknown grant", add: []string{"retired.scope"}, roleAdd: []string{"retired.scope"}, want: 400},
		{name: "remaining scope rejects after role insert", add: []string{"nodes.write", "knowledge.write"}, roleAdd: []string{"nodes.write"}, want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, owner := keyFixture(t)
			key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "grant-worker", "scopes": []string{"nodes.read"}}))
			role := keyScopeRole(t, m, owner, key)
			editor := owner
			if tc.permissions != nil {
				editor = keyScopeEditor(t, m, owner, tc.permissions...)
			}
			ctx := dbtest.Seed(t.Context())
			roleID := role.ID
			if tc.wrongRole || tc.builtin {
				if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
					if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE key='viewer'`).Scan(&roleID); err != nil {
						return err
					}
					if tc.builtin {
						_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, key.PrincipalID, roleID)
						return err
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.creatorCap {
				creator := keyScopeEditor(t, m, owner, "nodes.read", "keys.manage")
				if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE agent_keys SET created_by_principal_id=$2 WHERE id=$1`, key.ID, creator.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, beforeEvents := keyCounts(t, m, owner)
			w := scopesRequest(m, editor, key.ID, "PATCH", roleGrantBody(roleID, tc.add, tc.roleAdd))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if tc.want != 200 {
				current := keyScopeRole(t, m, owner, key)
				if !current.Builtin && slices.Contains(current.Permissions, "nodes.write") {
					t.Fatal("rejected request partially expanded the role")
				}
				view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
				if err != nil || !slices.Equal(view.Key.Scopes, []string{"nodes.read"}) {
					t.Fatal("rejected request changed key scopes")
				}
				_, afterEvents := keyCounts(t, m, owner)
				if afterEvents != beforeEvents {
					t.Fatal("rejected request wrote audit")
				}
			}
		})
	}
}

func TestKeyScopeRoleGrantForeignRoleAndStaleEditor(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "grant-worker", "scopes": []string{"nodes.read"}}))
	role := keyScopeRole(t, m, owner, key)
	ctx := dbtest.Seed(t.Context())
	var foreignTenant, foreignRole string
	if err := db.InTenant(ctx, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('grant-foreign','Foreign') RETURNING id::text`).Scan(&foreignTenant)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, m.pool, foreignTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'foreign','Foreign') RETURNING id::text`, foreignTenant).Scan(&foreignRole)
	}); err != nil {
		t.Fatal(err)
	}
	if w := scopesRequest(m, owner, key.ID, "PATCH", roleGrantBody(foreignRole, []string{"nodes.write"}, []string{"nodes.write"})); w.Code != 403 {
		t.Fatalf("foreign role status=%d", w.Code)
	}
	editor := keyScopeEditor(t, m, owner, "keys.manage", "roles.manage", "nodes.read", "nodes.write")
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE permission='roles.manage' AND role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$1)`, editor.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	delta := &keyScopeDelta{Add: []string{"nodes.write"}, RoleExtension: &keyRoleExtension{RoleID: role.ID, Add: []string{"nodes.write"}}}
	if _, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), editor), editor, key.ID, delta); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("stale role editor allowed: %v", err)
	}
	delta.Add = []string{"keys.manage"}
	delta.RoleExtension.Add = []string{"keys.manage"}
	if _, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, delta); !errors.Is(err, errKeyScopes) {
		t.Fatal("internal forged call bypassed registry validation")
	}
}

func TestKeyScopeUnknownCleanupAuditedAndKnownScopesPreserved(t *testing.T) {
	for _, method := range []string{"GET", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			m, owner := keyFixture(t)
			key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "cleanup-worker", "scopes": []string{"nodes.read"}}))
			ctx := dbtest.Seed(t.Context())
			if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=ARRAY['nodes.read','retired.scope','old:scope'] WHERE id=$1`, key.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, beforeEvents := keyCounts(t, m, owner)
			body := ""
			if method == "PATCH" {
				body = `{"add":[]}`
			}
			if w := scopesRequest(m, owner, key.ID, method, body); w.Code != 200 {
				t.Fatalf("cleanup status=%d", w.Code)
			}
			view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
			if err != nil || !slices.Equal(view.Key.Scopes, []string{"nodes.read"}) {
				t.Fatal("unknown scopes were not pruned")
			}
			_, afterEvents := keyCounts(t, m, owner)
			if afterEvents != beforeEvents+1 {
				t.Fatal("cleanup must audit once and remain idempotent")
			}
			if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
				var complete bool
				err := tx.QueryRow(ctx, `SELECT bool_and(actor_principal_id=$1::uuid AND before->'scopes' ? 'retired.scope' AND after->'pruned_scopes' ? 'retired.scope' AND after->'pruned_scopes' ? 'old:scope') FROM events WHERE type='agent_key.scopes_changed'`, owner.ID).Scan(&complete)
				if err == nil && !complete {
					return fmt.Errorf("cleanup audit lacks removed scopes")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// models.resolve still exists in today's registry. Reads must not discard
			// known scopes or invent a replacement models.read permission.
			if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=ARRAY['nodes.read','models.resolve'] WHERE id=$1`, key.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			view, err = m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
			if err != nil || !slices.Contains(view.Key.Scopes, "models.resolve") {
				t.Fatal("known stored scope was pruned")
			}
		})
	}
}

func TestKeyScopeRoleGrantAuditFailureRollsBackBothWrites(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "audit-worker", "scopes": []string{"nodes.read"}}))
	role := keyScopeRole(t, m, owner, key)
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `CREATE FUNCTION reject_scope_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='agent_key.scopes_changed' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_scope_audit BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_scope_audit()`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, beforeEvents := keyCounts(t, m, owner)
	if w := scopesRequest(m, owner, key.ID, "PATCH", roleGrantBody(role.ID, []string{"nodes.write"}, []string{"nodes.write"})); w.Code != 500 {
		t.Fatalf("audit failure status=%d", w.Code)
	}
	view, err := m.agentKeyScopes(tenant.WithPrincipal(t.Context(), owner), owner, key.ID, nil)
	if err != nil || slices.Contains(view.AgentRole.Permissions, "nodes.write") || !slices.Equal(view.Key.Scopes, []string{"nodes.read"}) {
		t.Fatal("audit failure committed a partial grant")
	}
	_, afterEvents := keyCounts(t, m, owner)
	if beforeEvents != afterEvents {
		t.Fatal("failed audit changed the event count")
	}
}

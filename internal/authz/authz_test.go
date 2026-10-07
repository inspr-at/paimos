// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestRegistryAndBuiltins(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Registry {
		if seen[p.Key] || p.Key == "" || p.Group == "" || p.Description == "" || len(p.GrantableAt) == 0 {
			t.Fatalf("bad permission %+v", p)
		}
		if p.Risk != "low" && p.Risk != "medium" && p.Risk != "high" {
			t.Fatalf("risk %+v", p)
		}
		seen[p.Key] = true
	}
	cases := []struct {
		role, permission string
		allow            bool
	}{
		{"owner", "ownership.transfer", true}, {"admin", "ownership.transfer", false},
		{"admin", "roles.manage", true}, {"member", "nodes.write", true},
		{"member", "roles.manage", false}, {"viewer", "nodes.read", true},
		{"member", "plugins.read", true}, {"member", "plugins.manage", false},
		{"member", "quotes.portal_accept", false},
		{"viewer", "nodes.write", false}, {"guest", "comments.write", true},
		{"guest", "nodes.write", false}, {"guest", "keys.read", false},
		{"customer", "quotes.portal_read", true},
		{"customer", "quotes.read", false},
		{"customer", "quotes.issue", false},
		{"member", "outcome.write", true}, {"viewer", "outcome.read", true},
		{"viewer", "outcome.write", false}, {"guest", "outcome.read", true},
		{"customer", "outcome.read", false},
	}
	for _, tc := range cases {
		got, _ := BuiltinPermissions(tc.role)
		if contains(got, tc.permission) != tc.allow {
			t.Errorf("%s %s: want %v", tc.role, tc.permission, tc.allow)
		}
	}
	for pattern, declaration := range RoutePermissions {
		if declaration == PublicRoute || declaration == AuthenticatedRoute {
			continue
		}
		for _, key := range strings.Split(declaration, "|") {
			if _, ok := Lookup(key); !ok {
				t.Errorf("route %q declares unknown permission %q", pattern, key)
			}
		}
	}
}

func TestBuiltinAgentExclusionsDefinition(t *testing.T) {
	if !slices.Equal(builtinAgentExclusions, []string{"recurrences.manage", "reviewpolicy.manage"}) {
		t.Fatal("built-in agent exclusions drifted from the explicit recurrence and review policies")
	}
	for _, key := range builtinAgentExclusions {
		permission, ok := Lookup(key)
		if !ok || !permission.AgentGrantable {
			t.Fatal("an explicit custom-role permission must remain agent-grantable")
		}
		for _, role := range []string{"owner", "admin", "member"} {
			want := key != "reviewpolicy.manage" || role != "member"
			if contains(builtinPermissions(role), key) != want {
				t.Fatalf("agent exclusions must leave person %s grants intact", role)
			}
		}
	}
}

func TestRouteDeclarationsFailClosed(t *testing.T) {
	if err := RequirePattern(context.Background(), "GET /api/undeclared", Scope{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("undeclared route: %v", err)
	}
	if err := RequirePattern(context.Background(), "GET /api/health", Scope{}); err != nil {
		t.Fatalf("public health: %v", err)
	}
	if err := RequirePattern(context.Background(), "GET /api/ready", Scope{}); err != nil {
		t.Fatalf("public readiness: %v", err)
	}
	const undo = "POST /api/queue/{nodeId}/undo"
	if permission, declared := PermissionForPattern(undo); !declared || permission != "nodes.read" {
		t.Fatalf("queue Undo entry permission: %q, declared=%v", permission, declared)
	}
	if err := RequirePattern(context.Background(), undo, Scope{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous queue Undo: %v", err)
	}
	for _, route := range []string{
		"POST /api/queue/{nodeId}/snapshots", "GET /api/queue-snapshots/{snapshotId}",
		"POST /api/queue-snapshots/{snapshotId}/apply", "DELETE /api/queue-snapshots/{snapshotId}",
	} {
		permission, declared := PermissionForPattern(route)
		if !declared || permission != "nodes.read" || !ProjectDecidedRoutes[route] {
			t.Fatalf("snapshot route has no project-scoped handler authorization: %s (%q)", route, permission)
		}
		if err := RequirePattern(context.Background(), route, Scope{}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("anonymous snapshot route: %s: %v", route, err)
		}
	}

}

func TestMeRequiresAuthenticationOnly(t *testing.T) {
	if PatternIsPublic("GET /api/me") {
		t.Fatal("self identity must not bypass authentication")
	}
	if declaration, ok := PermissionForPattern("GET /api/me"); !ok || declaration != AuthenticatedRoute {
		t.Fatalf("me declaration: %q, declared=%v", declaration, ok)
	}
	for _, p := range []tenant.Principal{{}, {ID: "caller"}, {TenantID: "tenant"}} {
		ctx := tenant.WithPrincipal(context.Background(), p)
		if err := RequirePattern(ctx, "GET /api/me", Scope{}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("incomplete principal allowed: %v", err)
		}
	}
	if err := RequirePattern(context.Background(), "GET /api/me", Scope{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous identity: %v", err)
	}
	for _, kind := range []tenant.PrincipalKind{tenant.Person, tenant.Agent} {
		ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{ID: "caller", TenantID: "tenant", Kind: kind})
		if err := RequirePattern(ctx, "GET /api/me", Scope{}); err != nil {
			t.Fatalf("authenticated %s without scopes or bindings: %v", kind, err)
		}
	}
	// Self identity is the only route with no permission for either caller kind.
	// Status help exempts agents only; people retain their nodes.read permission.
	for _, route := range []string{"GET /api/status/help", "GET /api/me/profile", "GET /api/me/greeting", "GET /api/me/permissions", "GET /api/members", "GET /api/agent-keys", "GET /api/events"} {
		declaration, ok := PermissionForPattern(route)
		if !ok || declaration == PublicRoute || declaration == AuthenticatedRoute {
			t.Errorf("permission gate missing on %s: %q", route, declaration)
		}
	}
	for route, declaration := range RoutePermissions {
		if declaration == AuthenticatedRoute && route != "GET /api/me" {
			t.Errorf("unexpected authenticated-only route %s", route)
		}
	}
}

func TestStatusHelpAgentReadRequiresAuthentication(t *testing.T) {
	const route = "GET /api/status/help"
	if PatternIsPublic(route) {
		t.Fatal("status metadata must require authentication")
	}
	if declaration, ok := PermissionForPattern(route); !ok || declaration != "nodes.read" {
		t.Fatalf("status help declaration: %q, declared=%v", declaration, ok)
	}
	for _, p := range []tenant.Principal{{}, {ID: "caller"}, {TenantID: "tenant"}, {Kind: tenant.Agent}, {Kind: tenant.Agent, ID: "caller"}, {Kind: tenant.Agent, TenantID: "tenant"}} {
		if err := RequirePattern(tenant.WithPrincipal(t.Context(), p), route, Scope{}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("incomplete principal allowed: %v", err)
		}
	}
	if err := RequirePattern(t.Context(), route, Scope{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous status help: %v", err)
	}
	ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: "caller", TenantID: "tenant", Kind: tenant.Agent})
	if err := RequirePattern(ctx, route, Scope{}); err != nil {
		t.Fatalf("authenticated agent without scopes or bindings: %v", err)
	}
	person := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: "caller", TenantID: "tenant", Kind: tenant.Person})
	if err := RequirePattern(person, route, Scope{}); !errors.Is(err, ErrNoStore) {
		t.Fatalf("person must consult the permission store: %v", err)
	}
	for _, route := range []string{"POST /api/status/help", "PUT /api/status/help", "PATCH /api/status/help", "DELETE /api/status/help", "GET /api/status/help/extra"} {
		if err := RequirePattern(ctx, route, Scope{}); !errors.Is(err, ErrForbidden) {
			t.Errorf("unexpected status route authority: %s: %v", route, err)
		}
	}
}

// Every current module declares literal ServeMux patterns. This source walk
// catches a new route even when its module is mounted only in production.
func TestRouteSourceCoverage(t *testing.T) {
	pattern := regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE|HEAD) /api/[^"\n]+)"`)
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "route_map.go" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range pattern.FindAllSubmatch(body, -1) {
			if _, ok := PermissionForPattern(string(match[1])); !ok {
				t.Errorf("undeclared API pattern %s in %s", match[1], path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMappingAndOwnerProtection(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('az1-fixture','AZ1 fixture') RETURNING id::text`).Scan(&tid)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE roles SET name='Changed' WHERE tenant_id=$1::uuid AND key='owner'`, tid)
		return err
	})
	if err == nil {
		t.Fatal("built-in role changed in database")
	}
	ids := map[string]string{}
	for _, role := range []string{"super_admin", "admin", "member", "reviewer", "external", "customer", "system", "importer", "operator", "embedding", "quote_public_service"} {
		err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			kind := "person"
			if role == "system" || role == "importer" || role == "operator" || role == "embedding" || role == "quote_public_service" {
				kind = "agent"
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,$2,$3,ARRAY[$3]) RETURNING id::text`, tid, kind, role).Scan(&id); err != nil {
				return err
			}
			ids[role] = id
			_, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_principal($1::uuid,$2::uuid)`, tid, id)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Guest is a project role (ADR-003 P2): "external" gets no workspace binding.
	expected := map[string]string{"super_admin": "owner", "admin": "admin", "member": "member", "reviewer": "member", "customer": "customer"}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for old, want := range expected {
			var got string
			if err := tx.QueryRow(ctx, `SELECT r.key FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id WHERE b.principal_id=$1::uuid`, ids[old]).Scan(&got); err != nil {
				return err
			}
			if got != want {
				t.Errorf("%s mapped to %s, want %s", old, got, want)
			}
		}
		for _, service := range []string{"external", "system", "importer", "operator", "embedding", "quote_public_service"} {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE principal_id=$1::uuid`, ids[service]).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Errorf("%s service principal received a binding", service)
			}
		}
		// An existing binding survives a changed legacy label and a rerun.
		if _, err := tx.Exec(ctx, `UPDATE principals SET roles=ARRAY['customer'] WHERE id=$1::uuid`, ids["admin"]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_principal($1::uuid,$2::uuid)`, tid, ids["admin"]); err != nil {
			return err
		}
		var got string
		if err := tx.QueryRow(ctx, `SELECT r.key FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id WHERE b.principal_id=$1::uuid`, ids["admin"]).Scan(&got); err != nil {
			return err
		}
		if got != "admin" {
			t.Errorf("rerun replaced binding with %s", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p := tenant.Principal{ID: ids["super_admin"], TenantID: tid, Kind: tenant.Person}
	check := func(permission string, scopes []string, want bool) {
		t.Helper()
		p.Scopes = scopes
		err := Require(BindPool(tenant.WithPrincipal(ctx, p), d.App), permission, Scope{})
		if (err == nil) != want {
			t.Errorf("%s scopes %v: %v", permission, scopes, err)
		}
	}
	check("ownership.transfer", nil, true)
	p.Kind = tenant.Agent
	check("nodes.read", nil, false)
	check("nodes.read", []string{"nodes.read"}, false)
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, ids["super_admin"])
		return err
	})
	if err == nil {
		t.Fatal("last active owner deactivated")
	}
	if !errors.Is(err, ErrForbidden) { // PostgreSQL constraint error is expected.
		t.Logf("last owner guard: %v", err)
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1::uuid AND key='owner')
		  WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, tid, ids["admin"])
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, ids["super_admin"])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Kind = tenant.Person
	check("nodes.read", nil, false)
}

func TestLinkedAliasAndLegacyAgentMigration(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, personID, aliasID, agentID string
	err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('az1-migration','AZ1 migration') RETURNING id::text`).Scan(&tid)
	})
	if err != nil {
		t.Fatal(err)
	}
	scopes := []string{"harness.read", "harness.write", "harness.worker", "inbox.send", "inbox.read", "work_orders.read", "nodes.read"}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Signed-in admin',ARRAY['admin']) RETURNING id::text`, tid).Scan(&personID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles,linked_to) VALUES($1::uuid,'person','Classic alias',ARRAY['super_admin'],$2::uuid) RETURNING id::text`, tid, personID).Scan(&aliasID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'agent','aeon-coordinator') RETURNING id::text`, tid).Scan(&agentID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1::uuid,$2::uuid,'legacy','az1-migration','unused',$3,(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, tid, agentID, scopes); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_principal($1::uuid,$2::uuid)`, tid, aliasID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_migrate_agent_binding($1::uuid,$2::uuid)`, tid, agentID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx, `SELECT r.key FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id WHERE b.principal_id=$1::uuid`, personID).Scan(&role); err != nil {
			return err
		}
		if role != "owner" {
			t.Errorf("linked signed-in person mapped to %s, want owner", role)
		}
		var aliasCount, systemEvents, bindingEvents int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE principal_id=$1::uuid`, aliasID).Scan(&aliasCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.type IN ('authz.binding_migrated','authz.agent_binding_migrated') AND p.name='System' AND p.roles @> ARRAY['system']`).Scan(&systemEvents); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type IN ('authz.binding_migrated','authz.agent_binding_migrated')`).Scan(&bindingEvents); err != nil {
			return err
		}
		if aliasCount != 0 || systemEvents != 2 || bindingEvents != 2 {
			t.Errorf("alias bindings=%d, System events=%d, binding events=%d", aliasCount, systemEvents, bindingEvents)
		}
		for _, scope := range scopes {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_bindings b JOIN role_permissions rp ON rp.tenant_id=b.tenant_id AND rp.role_id=b.role_id WHERE b.principal_id=$1::uuid AND rp.permission=$2)`, agentID, scope).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				t.Errorf("agent migration omitted %s", scope)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	alias := tenant.Principal{ID: aliasID, TenantID: tid, Kind: tenant.Person}
	if err := Require(BindPool(tenant.WithPrincipal(ctx, alias), d.App), "ownership.transfer", Scope{}); err != nil {
		t.Errorf("linked alias did not inherit canonical owner binding: %v", err)
	}
	agent := tenant.Principal{ID: agentID, TenantID: tid, Kind: tenant.Agent, Scopes: scopes}
	for _, route := range []string{
		"POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat",
		"GET /api/projects/{projectId}/messages/listen",
		"POST /api/projects/{projectId}/messages/{messageId}/ack",
		"POST /api/projects/{projectId}/messages",
		"GET /api/nodes",
	} {
		if err := RequirePattern(BindPool(tenant.WithPrincipal(ctx, agent), d.App), route, Scope{}); err != nil {
			t.Errorf("migrated agent denied %s: %v", route, err)
		}
	}
	err = db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var replacement string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Replacement owner',ARRAY['super_admin']) RETURNING id::text`, tid).Scan(&replacement); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_principal($1::uuid,$2::uuid)`, tid, replacement); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1::uuid`, personID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Require(BindPool(tenant.WithPrincipal(ctx, alias), d.App), "ownership.transfer", Scope{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("linked alias retained deactivated owner's permission: %v", err)
	}
}

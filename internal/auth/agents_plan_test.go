// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/views"
	"github.com/jackc/pgx/v5"
)

func TestAgentsPlanRouteScopeIsReadOnly(t *testing.T) {
	if scope, ok := coreAgentScope(httptest.NewRequest("GET", "/api/agents/plan", nil)); !ok || scope != agentplan.ReadScope {
		t.Fatal("plan read has no explicit scope")
	}
	for _, route := range []struct{ method, path string }{
		{"PUT", "/api/agents/plan"}, {"POST", "/api/agents/plan"}, {"GET", "/api/agents/plan/another-person"}, {"PUT", "/api/preferences/agents.working"},
	} {
		if scope, ok := coreAgentScope(httptest.NewRequest(route.method, route.path, nil)); ok && scope != "" {
			t.Fatalf("plan exposed %s %s", route.method, route.path)
		}
	}
}

func TestAgentsPlanRealKeyScopeCreatorAndLiveGrants(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "plan-enforcer", "scopes": []string{agentplan.ReadScope}}))
	unscoped := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "plain-worker", "scopes": []string{"harness.read"}}))
	mux := http.NewServeMux()
	views.New(m.pool).Mount(mux)
	secured := m.Middleware(mux)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, r.Pattern = mux.Handler(r)
		secured.ServeHTTP(w, r)
	}))
	t.Cleanup(app.Close)
	read := func(key agentKeyCreatedJSON, want int) {
		t.Helper()
		status, _, response := do(t, &http.Client{}, "GET", app.URL+"/api/agents/plan?principal_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", http.Header{"Authorization": {"Bearer " + key.Token}})
		if status != want {
			t.Fatalf("plan status=%d want=%d", status, want)
		}
		if want == 200 && response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("plan is cacheable")
		}
	}
	read(key, 200)
	read(unscoped, 403)
	ctx := dbtest.Seed(t.Context())
	mutate := func(sql string, args ...any) {
		t.Helper()
		if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Stored scopes never substitute for a role's live permission.
	mutate(`DELETE FROM role_permissions WHERE tenant_id=$1 AND permission=$2 AND role_id IN (SELECT role_id FROM role_bindings WHERE principal_id=$3)`, owner.TenantID, agentplan.ReadScope, key.PrincipalID)
	read(key, 403)
	mutate(`INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT tenant_id,role_id,$2 FROM role_bindings WHERE principal_id=$1 AND scope_type='workspace'`, key.PrincipalID, agentplan.ReadScope)
	read(key, 200)
	mutate(`UPDATE agent_keys SET created_by_principal_id=NULL WHERE id=$1`, key.ID)
	read(key, 403)
	mutate(`UPDATE agent_keys SET created_by_principal_id=$2 WHERE id=$1`, key.ID, owner.ID)
	mutate(`UPDATE principals SET status='deactivated' WHERE id=$1`, owner.ID)
	read(key, 403)
	mutate(`UPDATE principals SET status='active' WHERE id=$1`, owner.ID)
	read(key, 200)
	mutate(`UPDATE agent_keys SET revoked_at=now() WHERE id=$1`, key.ID)
	read(key, 401)
}

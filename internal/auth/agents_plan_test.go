// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
		{"PUT", "/api/agents/plan"}, {"POST", "/api/agents/plan"}, {"GET", "/api/agents/plan/another-person"},
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
	writer := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "plan-with-preferences", "scopes": []string{agentplan.ReadScope, "views.write"}}))
	mux := http.NewServeMux()
	views.New(m.pool).Mount(mux)
	secured := m.Middleware(mux)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, r.Pattern = mux.Handler(r)
		secured.ServeHTTP(w, r)
	}))
	t.Cleanup(app.Close)
	read := func(key agentKeyCreatedJSON, want int, messages ...string) {
		t.Helper()
		status, body, response := do(t, &http.Client{}, "GET", app.URL+"/api/agents/plan?principal_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", http.Header{"Authorization": {"Bearer " + key.Token}})
		if status != want {
			t.Fatalf("plan status=%d want=%d", status, want)
		}
		for _, message := range messages {
			if !strings.Contains(string(body), message) {
				t.Fatalf("plan refusal did not explain %s", message)
			}
		}
		if want == 200 && response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("plan is cacheable")
		}
	}
	read(key, 200)
	read(unscoped, 403, "scope missing")
	for _, key := range []agentKeyCreatedJSON{key, writer} {
		status, _, _ := do(t, &http.Client{}, "PUT", app.URL+"/api/preferences/agents.working", `{"value":{"total":0}}`, http.Header{"Authorization": {"Bearer " + key.Token}})
		if status != 403 {
			t.Fatalf("agent plan write status=%d", status)
		}
	}
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
	legacyKey(t, m, owner, key.ID)
	read(key, 403, "no person owner", "Settings")
	if w := adoptionRequest(m, owner, key.ID); w.Code != 200 {
		t.Fatalf("adoption status=%d", w.Code)
	}
	read(key, 200)
	// Respect the schema's last-owner guard while exercising deactivation.
	mutate(`INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Backup owner')`, owner.TenantID)
	mutate(`INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
		SELECT p.tenant_id,p.id,r.id,'workspace' FROM principals p JOIN roles r ON r.tenant_id=p.tenant_id AND r.key='owner'
		WHERE p.tenant_id=$1 AND p.name='Backup owner'`, owner.TenantID)
	mutate(`UPDATE principals SET status='deactivated' WHERE id=$1`, owner.ID)
	read(key, 403)
	mutate(`UPDATE principals SET status='active' WHERE id=$1`, owner.ID)
	read(key, 200)
	mutate(`UPDATE agent_keys SET revoked_at=now() WHERE id=$1`, key.ID)
	read(key, 401)
}

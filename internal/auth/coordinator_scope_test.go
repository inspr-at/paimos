// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCoordinatorKeyCeilingAtMiddleware(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "coord-ceiling", "Coordinator ceiling")
	m := newMod(t, Config{})
	historical, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "historical-coordinator", "", authz.CoordinatorBaseScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "current-coordinator", "", authz.CoordinatorKeyScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "paired-runtime", "", agentpairing.RuntimePermissions, nil)
	if err != nil {
		t.Fatal(err)
	}
	perms := workspacePermissions(t, tenantID, current.PrincipalID)
	if !slices.Contains(perms, "models.read") || slices.Contains(perms, "rules.read") {
		t.Fatalf("workspace role permissions = %v", perms)
	}
	historicalPerms := workspacePermissions(t, tenantID, historical.PrincipalID)
	if slices.Contains(historicalPerms, "models.read") || slices.Contains(historicalPerms, "rules.read") {
		t.Fatalf("historical role stored reads it does not list: %v", historicalPerms)
	}

	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(token, method, path string, want int) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		setPolicyPattern(req)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Errorf("%s %s: %d %s, want %d", method, path, res.Code, res.Body.String(), want)
		}
	}
	for _, token := range []string{historical.Token, current.Token} {
		call(token, http.MethodGet, "/api/models/resolve?role=build-hard", http.StatusNoContent)
		call(token, http.MethodGet, "/api/models", http.StatusNoContent)
		call(token, http.MethodGet, "/api/rules/layers", http.StatusNoContent)
		call(token, http.MethodGet, "/api/rules/sets?layer_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", http.StatusNoContent)
		call(token, http.MethodGet, "/api/rules/merged?project_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", http.StatusNoContent)
		call(token, http.MethodPost, "/api/rules/layers", http.StatusForbidden)
		call(token, http.MethodPost, "/api/models", http.StatusForbidden)
	}
	call(runtime.Token, http.MethodGet, "/api/models/resolve?role=scout", http.StatusNoContent)
	call(runtime.Token, http.MethodGet, "/api/rules/layers", http.StatusForbidden)
	call(runtime.Token, http.MethodGet, "/api/rules/merged", http.StatusForbidden)
}

func workspacePermissions(t *testing.T, tenantID, principalID string) []string {
	t.Helper()
	var out []string
	err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT rp.permission FROM role_permissions rp
			JOIN role_bindings b ON b.tenant_id=rp.tenant_id AND b.role_id=rp.role_id
			WHERE b.principal_id=$1::uuid AND b.scope_type='workspace' ORDER BY rp.permission`, principalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var permission string
			if err := rows.Scan(&permission); err != nil {
				return err
			}
			out = append(out, permission)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

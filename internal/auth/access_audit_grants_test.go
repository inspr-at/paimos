// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestAccessAuditIncludesGeneratedAgentPermissionGrant(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "audit-worker", "scopes": []string{"nodes.read"}}))
	role := keyScopeRole(t, m, owner, key)
	decodeKey(t, keyRequest(m, owner, map[string]any{"name": "audit-worker-expanded", "principal_id": key.PrincipalID, "scopes": []string{"nodes.read", "nodes.write"}}))
	mux := http.NewServeMux()
	authz.New(m.pool).Mount(mux)
	r := httptest.NewRequest("GET", "/api/audit?category=access", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), owner))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("audit status %d", w.Code)
	}
	var page struct {
		Items []struct {
			Type  string `json:"type"`
			Actor struct {
				ID string `json:"principal_id"`
			} `json:"actor"`
			Subject struct {
				ID string `json:"principal_id"`
			} `json:"subject"`
			Data struct {
				After struct {
					Permission string `json:"permission"`
					RoleID     string `json:"role_id"`
				} `json:"after"`
			} `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Data.After.Permission != "nodes.write" {
			continue
		}
		if item.Type != "role.updated" || item.Actor.ID != owner.ID || item.Subject.ID != key.PrincipalID || item.Data.After.RoleID != role.ID {
			t.Fatal("grant attribution incomplete")
		}
		return
	}
	t.Fatal("generated-agent permission grant missing from access audit")
}

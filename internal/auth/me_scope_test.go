// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestMeEmptyScopeKeyWithoutBindings(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "identity-only", "scopes": []string{}}))
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, owner.TenantID, key.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	app := startApp(t, m)
	hc := &http.Client{}
	header := http.Header{"Authorization": {"Bearer " + key.Token}}
	for _, path := range []string{"/api/me", "/api/me?principal_id=" + owner.ID + "&tenant_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"} {
		status, body, response := do(t, hc, http.MethodGet, app.URL+path, "", header)
		if status != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("identity-only key: status %d", status)
		}
		var me meJSON
		if err := json.Unmarshal(body, &me); err != nil {
			t.Fatal(err)
		}
		if me.Principal.ID != key.PrincipalID || me.Principal.Name != "identity-only" || me.Principal.Email != nil || me.Tenant.ID != owner.TenantID || me.Identity != nil {
			t.Fatal("self identity disclosed another principal or tenant")
		}
	}
	if status, _, _ := do(t, hc, http.MethodGet, app.URL+"/api/events", "", header); status != http.StatusForbidden {
		t.Fatalf("identity-only key reached workspace history: %d", status)
	}
	for _, header := range []http.Header{nil, {"Authorization": {"Bearer invalid"}}} {
		if status, _, _ := do(t, hc, http.MethodGet, app.URL+"/api/me", "", header); status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated identity: %d", status)
		}
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=now() WHERE id=$1::uuid`, key.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := do(t, hc, http.MethodGet, app.URL+"/api/me", "", header); status != http.StatusUnauthorized {
		t.Fatalf("revoked identity-only key: %d", status)
	}
}

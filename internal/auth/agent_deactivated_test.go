// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// AEON-470: a deactivated agent's keys never authenticate, so no key is minted
// for it, by id or by name; reactivating lifts that.
func TestNoKeyForADeactivatedAgent(t *testing.T) {
	m, owner := keyFixture(t)
	ctx := dbtest.Seed(t.Context())
	var old string
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles,status) VALUES($1::uuid,'agent','relic','{}','deactivated') RETURNING id::text`, owner.TenantID).Scan(&old)
	}); err != nil {
		t.Fatal(err)
	}
	if w := keyRequest(m, owner, map[string]any{"principal_id": old, "scopes": []string{"nodes.read"}}); w.Code != 409 {
		t.Fatalf("key by id for a deactivated agent: status %d, want 409", w.Code)
	}
	if w := keyRequest(m, owner, map[string]any{"name": "relic", "scopes": []string{"nodes.read"}}); w.Code != 409 {
		t.Fatalf("key by name for a deactivated agent: status %d, want 409", w.Code)
	}

	// An active agent of the same name is the one a name-only request reaches.
	var live string
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent','relic','{}') RETURNING id::text`, owner.TenantID).Scan(&live)
	}); err != nil {
		t.Fatal(err)
	}
	if key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "relic", "scopes": []string{"nodes.read"}})); key.PrincipalID != live {
		t.Fatal("a name-only key went to the deactivated identity")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE principals SET status='active' WHERE id=$1::uuid`, old)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if key := decodeKey(t, keyRequest(m, owner, map[string]any{"principal_id": old, "scopes": []string{"nodes.read"}})); key.PrincipalID != old {
		t.Fatal("reactivated agent got a key for another identity")
	}
}

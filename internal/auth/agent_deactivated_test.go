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

// AEON-470 review: reactivating a retired computer's identity never makes it
// keyable. A computer is connected by pairing afresh, so the server refuses an
// agent key for any paired identity, revoked computers included; the Access UI
// points at pairing instead of offering a key.
func TestNoKeyForAReactivatedRetiredComputer(t *testing.T) {
	m, owner := keyFixture(t)
	ctx := dbtest.Seed(t.Context())
	var principal string
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent','old-laptop','{}') RETURNING id::text`, owner.TenantID).Scan(&principal); err != nil {
			return err
		}
		var key, request string
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,revoked_at) VALUES($1::uuid,$2::uuid,'runtime','old-laptop-runtime','old-laptop-hash','{nodes.read}',now()) RETURNING id::text`, owner.TenantID, principal).Scan(&key); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
			VALUES($1::uuid,gen_random_uuid(),'123456789',repeat('a',64),repeat('b',64),repeat('c',64),'{}'::jsonb,'digest','approved') RETURNING id::text`, owner.TenantID).Scan(&request); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,state) VALUES($1::uuid,$2::uuid,$2::uuid,$3::uuid,$4::uuid,$5,repeat('c',64),'revoked')`, owner.TenantID, request, principal, key, "paired-"+request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := keyRequest(m, owner, map[string]any{"principal_id": principal, "scopes": []string{"nodes.read"}}); w.Code != 403 {
		t.Fatalf("key for a retired computer's identity: status %d, want 403", w.Code)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestOperatorAgentKeyAuditAttribution(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tenantID string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('operator-key-audit','Operator keys') RETURNING id::text`).Scan(&tenantID)
	}); err != nil {
		t.Fatal(err)
	}
	keyID, _, _, err := OperatorCreateAgentKey(ctx, d.App, tenantID, "worker", "", []string{"nodes.read"}, nil, keyTestPerson(t, d.App, tenantID))
	if err != nil {
		t.Fatal(err)
	}
	if err := OperatorRevokeAgentKey(ctx, d.App, tenantID, keyID); err != nil {
		t.Fatal(err)
	}
	if err := OperatorRevokeAgentKey(ctx, d.App, tenantID, keyID); err != nil {
		t.Fatal(err)
	}
	var operatorID string
	var creator *string
	var principals, created, bindings, keys int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT p.id::text,k.created_by_principal_id::text,
			(SELECT count(*) FROM principals WHERE tenant_id=$1::uuid AND name='Access operator'),
			(SELECT count(*) FROM events WHERE tenant_id=$1::uuid AND type='principal.created' AND actor_principal_id=p.id),
			(SELECT count(*) FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=p.id),
			(SELECT count(*) FROM agent_keys WHERE tenant_id=$1::uuid AND principal_id=p.id)
			FROM principals p JOIN agent_keys k ON k.tenant_id=p.tenant_id AND k.id=$2::uuid
			WHERE p.tenant_id=$1::uuid AND p.kind='agent' AND p.name='Access operator' AND p.roles=ARRAY['operator']::text[]`, tenantID, keyID).Scan(&operatorID, &creator, &principals, &created, &bindings, &keys)
	}); err != nil {
		t.Fatal(err)
	}
	if creator == nil || principals != 1 || created != 1 || bindings != 0 || keys != 0 {
		t.Fatalf("operator safety: creator=%v operator=%s principals=%d created=%d bindings=%d keys=%d", creator, operatorID, principals, created, bindings, keys)
	}
	for _, typ := range []string{"authz.agent_binding_created", "agent_key.created", "agent_key.revoked"} {
		var total, attributed int
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE actor_principal_id=$2::uuid)
				FROM events WHERE tenant_id=$1::uuid AND type=$3`, tenantID, operatorID, typ).Scan(&total, &attributed)
		}); err != nil {
			t.Fatal(err)
		}
		if total != 1 || attributed != 1 {
			t.Fatalf("%s: total=%d attributed=%d", typ, total, attributed)
		}
	}
}

func keyTestPerson(t *testing.T, pool *pgxpool.Pool, tenantID string) string {
	return dbtest.KeyPerson(t, pool, tenantID)
}

// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestAgentKeyPersonOwnerExpansionPreservesLegacyWriters(t *testing.T) {
	ctx := dbtest.Seed(t.Context())
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tid, person, agent, legacy, owned string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1256_agent_key_person_owner.sql" {
			return nil
		}
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('key-owner-migration','Owner migration') RETURNING id::text`).Scan(&tid); err != nil {
			return err
		}
		return db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Person') RETURNING id::text`, tid).Scan(&person); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Agent') RETURNING id::text`, tid).Scan(&agent); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash) VALUES($1,$2,'Legacy','legacy-owner-fixture','fixture-not-a-credential') RETURNING id::text`, tid, agent).Scan(&legacy); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'Owned','owned-owner-fixture','fixture-not-a-credential',$3) RETURNING id::text`, tid, agent, person).Scan(&owned)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	run := func(query string, args ...any) error {
		return db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, query, args...); return err })
	}
	if err := run(`UPDATE agent_keys SET last_used_at=now(),revoked_at=now() WHERE id=$1`, legacy); err != nil {
		t.Fatalf("legacy updates broken: %v", err)
	}
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		var preserved bool
		err := tx.QueryRow(ctx, `SELECT created_by_principal_id IS NULL AND NOT person_owner_required FROM agent_keys WHERE id=$1`, legacy).Scan(&preserved)
		if err == nil && !preserved {
			t.Error("legacy ownership invented")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Replay the previous binary's key insert, rotation and usage/revocation
	// writes after expansion. Current code rejects creatorless issuance; the
	// schema cannot enforce that until older writers are retired.
	for _, prefix := range []string{"old-binary-create", "old-binary-rotate"} {
		if err := run(`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash) VALUES($1,$2,'Old writer',$3,'fixture-not-a-credential')`, tid, agent, prefix); err != nil {
			t.Fatalf("previous-binary insert %s rejected after expansion: %v", prefix, err)
		}
	}
	if err := run(`UPDATE agent_keys SET last_used_at=now(),revoked_at=now() WHERE id=$1`, owned); err != nil {
		t.Fatalf("previous-binary owned-key update rejected: %v", err)
	}
	if err := run(`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id,person_owner_required) VALUES($1,$2,'New owned','new-owned','fixture-not-a-credential',$3,true)`, tid, agent, person); err != nil {
		t.Fatalf("current-binary owned-key insert rejected: %v", err)
	}
	if err := run(`UPDATE agent_keys SET created_by_principal_id=$2,person_owner_required=true WHERE id=$1`, legacy, person); err != nil {
		t.Fatalf("adoption marker write rejected: %v", err)
	}
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		var oldWriters, ownedPreserved, adopted, currentOwned bool
		err := tx.QueryRow(ctx, `SELECT
		 (SELECT count(*)=2 AND bool_and(created_by_principal_id IS NULL AND person_owner_required=false) FROM agent_keys WHERE prefix IN ('old-binary-create','old-binary-rotate')),
		 (SELECT created_by_principal_id=$2::uuid AND person_owner_required=false AND revoked_at IS NOT NULL AND hash='fixture-not-a-credential' FROM agent_keys WHERE id=$1),
		 (SELECT created_by_principal_id=$2::uuid AND person_owner_required=true AND hash='fixture-not-a-credential' FROM agent_keys WHERE id=$3),
		 (SELECT created_by_principal_id=$2::uuid AND person_owner_required=true FROM agent_keys WHERE prefix='new-owned')`, owned, person, legacy).Scan(&oldWriters, &ownedPreserved, &adopted, &currentOwned)
		if err == nil && (!oldWriters || !ownedPreserved || !adopted || !currentOwned) {
			t.Errorf("expansion state: old writers=%v existing owner=%v adoption=%v current owner=%v", oldWriters, ownedPreserved, adopted, currentOwned)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: generated CHECK names differ on an upgraded installation, or nullable
// identity expansion changes the previous binary's key/binding write shapes.
func TestPersonIdentityMigrationDiscoversChecksAndPreservesOldWriters(t *testing.T) {
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
	var tid, person, agent, key, role string
	var original []string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1389_person_identity_scope_expansion.sql" {
			return nil
		}
		rows, err := d.App.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid='role_bindings'::regclass AND contype='c' ORDER BY conname`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			original = append(original, name)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(original) != 2 {
			return fmt.Errorf("expected two original scope CHECKs, got %v", original)
		}
		t.Logf("pg_constraint before expansion: %v", original)
		for i, name := range original {
			if _, err = d.App.Exec(ctx, `ALTER TABLE role_bindings RENAME CONSTRAINT `+pgx.Identifier{name}.Sanitize()+` TO `+pgx.Identifier{fmt.Sprintf("identity_fixture_check_%d", i)}.Sanitize()); err != nil {
				return err
			}
		}
		if err = d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-expansion','Person expansion') RETURNING id::text`).Scan(&tid); err != nil {
			return err
		}
		return db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			for _, v := range []struct {
				kind string
				id   *string
			}{{"person", &person}, {"agent", &agent}} {
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,'Identity migration') RETURNING id::text`, tid, v.kind).Scan(v.id); err != nil {
					return err
				}
			}
			if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1 AND key='member'`, tid).Scan(&role); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tid, person, role); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash) VALUES($1,$2,'Old key','identity-old-key','not-a-credential') RETURNING id::text`, tid, agent).Scan(&key)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		var untouched bool
		if err := tx.QueryRow(ctx, `SELECT person_id IS NULL FROM agent_keys WHERE id=$1`, key).Scan(&untouched); err != nil {
			return err
		}
		if !untouched {
			t.Fatal("legacy key person was inferred")
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_keys SET last_used_at=clock_timestamp(),revoked_at=clock_timestamp() WHERE id=$1`, key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash) VALUES($1,$2,'Old replacement','identity-old-replacement','not-a-credential')`, tid, agent); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace') ON CONFLICT(tenant_id,principal_id) WHERE scope_type='workspace' DO UPDATE SET role_id=EXCLUDED.role_id`, tid, person, role)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var notNull bool
	if err = d.App.QueryRow(ctx, `SELECT attnotnull FROM pg_attribute WHERE attrelid='role_bindings'::regclass AND attname='principal_id'`).Scan(&notNull); err != nil || !notNull {
		t.Fatalf("principal subject nullability changed: %v", err)
	}
}

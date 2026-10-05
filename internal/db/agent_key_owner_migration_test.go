// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestAgentKeyPersonOwnerMigrationPreservesLegacyAndGuardsNew(t *testing.T) {
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
	var tid, foreign, person, agent, legacy, owned string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1256_agent_key_person_owner.sql" {
			return nil
		}
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('key-owner-migration','Owner migration') RETURNING id::text`).Scan(&tid); err != nil {
			return err
		}
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('key-owner-foreign','Foreign') RETURNING id::text`).Scan(&foreign); err != nil {
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
	assertCheck := func(query string, args ...any) {
		t.Helper()
		err := run(query, args...)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Fatalf("expected CHECK rejection, got %v", err)
		}
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
	assertCheck(`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash) VALUES($1,$2,'New','new-no-owner','fixture')`, tid, agent)
	assertCheck(`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,person_owner_required) VALUES($1,$2,'Bypass','bypass','fixture',false)`, tid, agent)
	assertCheck(`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'Agent owner','agent-owner','fixture',$2)`, tid, agent)
	assertCheck(`UPDATE agent_keys SET created_by_principal_id=NULL WHERE id=$1`, owned)
	if err := run(`UPDATE agent_keys SET created_by_principal_id=$2 WHERE id=$1`, legacy, person); err != nil {
		t.Fatal(err)
	}
	assertCheck(`UPDATE agent_keys SET person_owner_required=false,created_by_principal_id=NULL WHERE id=$1`, legacy)
	var foreignPerson string
	if err := db.InTenant(ctx, d.App, foreign, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign') RETURNING id::text`, foreign).Scan(&foreignPerson)
	}); err != nil {
		t.Fatal(err)
	}
	assertCheck(`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'Foreign owner','foreign-owner','fixture',$3)`, tid, agent, foreignPerson)
	if err := run(`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'New owned','new-owned','fixture',$3)`, tid, agent, person); err != nil {
		t.Fatal(err)
	}
}

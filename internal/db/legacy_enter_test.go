// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a schema that has not applied migration 1397 rejects every tenant
// entry because the caller always asks for the four-argument function.
func TestLegacySchemaEnterPrincipal(t *testing.T) {
	ctx := t.Context()
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	stop := errors.New("before person ceiling")
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name == "1397_person_ceiling.sql" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("old schema: %v", err)
	}
	const tid = "11111111-1111-4111-8111-111111111111"
	const person = "22222222-2222-4222-8222-222222222222"
	if err := db.InTenant(dbtest.Seed(ctx), d.Admin, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1,'legacy-enter','Legacy')`, tid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.Admin, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Legacy')`, tid, person)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := tenant.Principal{ID: person, TenantID: tid, Kind: tenant.Person}
	if err := db.InTenant(tenant.WithPrincipal(ctx, p), d.App, tid, func(tx pgx.Tx) error {
		var one int
		return tx.QueryRow(ctx, `SELECT 1`).Scan(&one)
	}); err != nil {
		t.Fatal(err)
	}
}

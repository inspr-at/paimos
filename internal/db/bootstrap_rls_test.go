// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Production runs as a table-owning NOSUPERUSER NOBYPASSRLS role, so FORCE ROW
// LEVEL SECURITY applies to it. Creating a tenant there must still seed the six
// built-in roles (the image smoke caught this failing after migration 0810).
func TestEnsureTenantSeedsBuiltinRolesUnderForcedRLS(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	if err := db.EnsureTenant(ctx, d.App, "seed-rls", "Seed under RLS"); err != nil {
		t.Fatal(err)
	}
	var tenantID string
	if err := d.Admin.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug='seed-rls'`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	var builtins int
	if err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM roles WHERE builtin`).Scan(&builtins)
	}); err != nil {
		t.Fatal(err)
	}
	if builtins != 6 {
		t.Fatalf("built-in roles seeded under forced RLS: %d, want 6", builtins)
	}
	// A second call is a no-op and must not fail either.
	if err := db.EnsureTenant(ctx, d.App, "seed-rls", "Seed under RLS"); err != nil {
		t.Fatal(err)
	}
}

func TestTenantWorkKindsSeedRestoresScopeUnderForcedRLS(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	if err := db.EnsureTenant(ctx, d.App, "work-kinds-first", "First"); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := d.Admin.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug='work-kinds-first'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, first, func(tx pgx.Tx) error {
		var second, prior, after string
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&prior); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('work-kinds-second','Second') RETURNING id::text`).Scan(&second); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&after); err != nil {
			return err
		}
		if after != prior || after != first {
			t.Fatalf("tenant trigger changed scope %q to %q", prior, after)
		}
		var total, foreign int
		if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE tenant_id<>$1::uuid) FROM work_kinds`, first).Scan(&total, &foreign); err != nil {
			return err
		}
		if total != 9 || foreign != 0 {
			t.Fatalf("first tenant kinds %d, foreign %d", total, foreign)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true)`, second); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM work_kinds`).Scan(&total); err != nil {
			return err
		}
		if total != 9 {
			t.Fatalf("second tenant kinds: %d", total)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

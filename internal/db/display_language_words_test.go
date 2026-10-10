// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

// Risk: backfilling translated defaults overwrites existing tenant edits, or
// newly bootstrapped tenants lack translations / can read another tenant's words.
func TestDisplayWordsMigrationPreservesExistingEditsAndTenantIsolation(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var old, fresh string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1295_display_language_words.sql" {
			return nil
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('words-old','Words old') RETURNING id::text`).Scan(&old); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(t.Context()), d.App, old, func(tx pgx.Tx) error {
			if err := modelprefs.SeedKinds(t.Context(), tx, old); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE work_kinds SET label='Existing custom design',hint='Keep this exact sentence.',examples=ARRAY['Keep this example'] WHERE slug='design'`)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('words-new','Words new') RETURNING id::text`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, fresh} {
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			if err := modelprefs.SeedKinds(t.Context(), tx, id); err != nil {
				return err
			}
			var count, foreign int
			if err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE tenant_id<>$1) FROM work_display_words`, id).Scan(&count, &foreign); err != nil {
				return err
			}
			if count != 15 || foreign != 0 {
				t.Fatal("seed missing or RLS crossed tenants", count, foreign)
			}
			if id == old {
				var label, hint string
				var examples []string
				if err := tx.QueryRow(t.Context(), `SELECT label,hint,examples FROM work_kinds WHERE slug='design'`).Scan(&label, &hint, &examples); err != nil {
					return err
				}
				if label != "Existing custom design" || hint != "Keep this exact sentence." || !reflect.DeepEqual(examples, []string{"Keep this example"}) {
					t.Fatal("migration overwrote existing edits", label, hint, examples)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

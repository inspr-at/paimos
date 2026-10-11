// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Risk (AEON-1142): the defaults reach existing workspaces (a backfill, or an
// overwrite of a saved vocabulary), or a new workspace misses them, or the
// trigger leaves the caller acting as the new tenant.
func TestWorkVocabularyDefaultsSeedNewWorkspacesOnly(t *testing.T) {
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
	const saved = `{"leaf": {"icon": "check", "name": "Schritt"}, "levels": [{"icon": "tree", "name": "Vorhaben"}]}`
	var unsaved, custom, fresh string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1332_work_vocabulary_defaults.sql" {
			return nil
		}
		for i, id := range []*string{&unsaved, &custom} {
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Vocabulary upgrade') RETURNING id::text`, fmt.Sprintf("vocabulary-upgrade-%d", i)).Scan(id); err != nil {
				return err
			}
		}
		return db.InTenant(ctx, d.App, custom, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO work_vocabulary(tenant_id,revision,vocabulary) VALUES($1,4,$2::jsonb)`, custom, saved)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(ctx, d.App, unsaved, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('vocabulary-fresh','Vocabulary fresh') RETURNING id::text`).Scan(&fresh); err != nil {
			return err
		}
		var scope string
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&scope); err != nil {
			return err
		}
		if scope != unsaved {
			return fmt.Errorf("tenant creation left scope %q, want %q", scope, unsaved)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(tenantID string) (rows int, revision int64, vocabulary string) {
		t.Helper()
		err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM work_vocabulary`).Scan(&rows); err != nil || rows == 0 {
				return err
			}
			return tx.QueryRow(ctx, `SELECT revision,vocabulary::text FROM work_vocabulary`).Scan(&revision, &vocabulary)
		})
		if err != nil {
			t.Fatal(err)
		}
		return rows, revision, vocabulary
	}
	if rows, _, _ := read(unsaved); rows != 0 {
		t.Fatalf("existing workspace without a saved vocabulary got %d rows: the defaults must not backfill", rows)
	}
	if rows, revision, vocabulary := read(custom); rows != 1 || revision != 4 || vocabulary != saved {
		t.Fatalf("saved vocabulary changed: %d rows, revision %d, %s", rows, revision, vocabulary)
	}
	const defaults = `{"leaf": {"icon": "task", "name": "Task"}, "levels": [{"icon": "layers", "name": "Epic"}, {"icon": "ticket", "name": "Ticket"}]}`
	if rows, revision, vocabulary := read(fresh); rows != 1 || revision != 1 || vocabulary != defaults {
		t.Fatalf("new workspace: %d rows, revision %d, %s", rows, revision, vocabulary)
	}
	if err := db.MigrateWithHook(ctx, d.App, func(name string) error { return fmt.Errorf("unexpected migration replay %s", name) }); err != nil {
		t.Fatal(err)
	}
}

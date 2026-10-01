// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestBillingSnapshotMigrationBoundsLockWaitAndRetries(t *testing.T) {
	const name = "1065_billing_estimate_snapshots.sql"
	body, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	// The timeout must be effective before the first existing-table lock.
	sql := strings.TrimSpace(string(body))
	for strings.HasPrefix(sql, "--") {
		_, sql, _ = strings.Cut(sql, "\n")
		sql = strings.TrimSpace(sql)
	}
	if !strings.HasPrefix(sql, "SET LOCAL lock_timeout = '5s';") {
		t.Fatalf("%s must set its lock timeout before any SQL", name)
	}
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var contender pgx.Tx
	var began time.Time
	err = db.MigrateWithHook(t.Context(), d.App, func(next string) error {
		if next != name {
			return nil
		}
		var err error
		contender, err = d.App.Begin(t.Context())
		if err != nil {
			return err
		}
		t.Cleanup(func() { _ = contender.Rollback(context.Background()) })
		// A reader prevents ALTER TABLE's access-exclusive lock.
		if _, err := contender.Exec(t.Context(), `LOCK TABLE agent_accounts IN ACCESS SHARE MODE`); err != nil {
			return err
		}
		began = time.Now()
		return nil
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("migration must fail with lock_timeout (55P03): %v", err)
	}
	if waited := time.Since(began); waited < 4*time.Second || waited > 20*time.Second {
		t.Fatalf("migration lock wait %s, want about 5s", waited)
	}
	var recorded, column, table bool
	err = d.App.QueryRow(t.Context(), `SELECT
        EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1),
        EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='agent_accounts'::regclass AND attname='billing_mode' AND NOT attisdropped),
        to_regclass('ticket_estimate_snapshots') IS NOT NULL`, name).Scan(&recorded, &column, &table)
	if err != nil || recorded || column || table {
		t.Fatalf("timed-out migration partially applied: recorded=%t column=%t table=%t: %v", recorded, column, table, err)
	}
	if err := contender.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatalf("retry after lock cleared: %v", err)
	}
	err = d.App.QueryRow(t.Context(), `SELECT
        EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1),
        EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='agent_accounts'::regclass AND attname='billing_mode' AND NOT attisdropped),
        to_regclass('ticket_estimate_snapshots') IS NOT NULL`, name).Scan(&recorded, &column, &table)
	if err != nil || !recorded || !column || !table {
		t.Fatalf("retry did not apply migration: recorded=%t column=%t table=%t: %v", recorded, column, table, err)
	}
}

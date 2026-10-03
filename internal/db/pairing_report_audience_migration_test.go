// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// 1050 creates a policy on events, which waits for a table lock. A long
// transaction on events must fail the migration after its bounded wait, not
// queue it (and every query behind it) indefinitely. The failure is retryable:
// the transaction rolls back, nothing is recorded, and a later run applies it.
func TestPairingReportAudienceMigrationBoundsLockWait(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	const name = "1050_pairing_report_audience.sql"
	body, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "SET LOCAL lock_timeout = '5s';") {
		t.Fatalf("%s must bound its lock wait", name)
	}

	var contender pgx.Tx
	var began time.Time
	err = db.MigrateWithHook(t.Context(), d.App, func(next string) error {
		if next != name {
			return nil
		}
		tx, err := d.App.Begin(t.Context())
		if err != nil {
			return err
		}
		contender = tx
		// Stands in for a long transaction holding a conflicting lock on the event log.
		if _, err := tx.Exec(t.Context(), `LOCK TABLE events IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		began = time.Now()
		return nil
	})
	if contender == nil {
		t.Fatal("the lock contender never started")
	}
	t.Cleanup(func() { _ = contender.Rollback(t.Context()) })
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("migration did not fail on lock_timeout (55P03): %v", err)
	}
	if waited := time.Since(began); waited < 4*time.Second || waited > 20*time.Second {
		t.Fatalf("migration waited %s for the lock, want about 5s", waited)
	}
	var recorded bool
	if err := d.App.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatalf("%s was recorded after a lock timeout", name)
	}
	var policies int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_policy WHERE polname='events_pairing_report_audience'`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 0 {
		t.Fatal("a timed-out migration left its policy behind")
	}

	// The contender ends; the same runner retries and applies it.
	if err := contender.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatalf("retry after the lock cleared: %v", err)
	}
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_policy WHERE polname='events_pairing_report_audience'`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 1 {
		t.Fatalf("policy count after the retry = %d, want 1", policies)
	}
}

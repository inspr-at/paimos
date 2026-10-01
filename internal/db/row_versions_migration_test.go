// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRowVersionValidationRunsAfterDDLLocksRelease(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var sessionFile, runFile uint32
	var writer pgx.Tx
	var writerReleased bool
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if writer != nil && !writerReleased {
			// The preceding file completed both VALIDATEs with this writer
			// still holding its lock. Later migrations may legitimately need
			// a stronger table lock (for example the AEON-498 index swap).
			var validated int
			if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conname IN ('harness_sessions_row_version_check','agent_runs_row_version_check') AND convalidated`).Scan(&validated); err != nil {
				return err
			}
			if validated != 2 {
				return fmt.Errorf("validated under writer lock = %d, want 2", validated)
			}
			if err := writer.Rollback(t.Context()); err != nil {
				return err
			}
			writerReleased = true
		}
		switch name {
		case "1052_row_versions.sql":
			return d.App.QueryRow(t.Context(), `SELECT
                (SELECT relfilenode FROM pg_class WHERE oid='harness_sessions'::regclass),
                (SELECT relfilenode FROM pg_class WHERE oid='agent_runs'::regclass)`).Scan(&sessionFile, &runFile)
		case "1058_row_versions_validate.sql":
			var pending int
			var afterSession, afterRun uint32
			if err := d.App.QueryRow(t.Context(), `SELECT
                (SELECT count(*) FROM pg_constraint WHERE conname IN ('harness_sessions_row_version_check','agent_runs_row_version_check') AND NOT convalidated),
                (SELECT relfilenode FROM pg_class WHERE oid='harness_sessions'::regclass),
                (SELECT relfilenode FROM pg_class WHERE oid='agent_runs'::regclass)`).Scan(&pending, &afterSession, &afterRun); err != nil {
				return err
			}
			if pending != 2 {
				return fmt.Errorf("validated before DDL commit: %d pending", pending)
			}
			if afterSession != sessionFile || afterRun != runFile {
				return errors.New("row version installation rewrote a table")
			}
			var err error
			writer, err = d.App.Begin(t.Context())
			if err != nil {
				return err
			}
			// Existing writers must not prevent validation. Keep this transaction
			// open until the real migration runner finishes both VALIDATEs,
			// then release it before any later migration.
			_, err = writer.Exec(t.Context(), `LOCK TABLE harness_sessions, agent_runs IN ROW EXCLUSIVE MODE`)
			return err
		}
		return nil
	})
	if writer != nil {
		defer writer.Rollback(t.Context())
	}
	if err != nil {
		t.Fatal(err)
	}
	if writer == nil {
		t.Fatal("validation migration never ran")
	}
	var validated int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conname IN ('harness_sessions_row_version_check','agent_runs_row_version_check') AND convalidated`).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if validated != 2 {
		t.Fatalf("validated = %d, want 2", validated)
	}
}

func TestRowVersionInstallationBoundsLockWaitAndRetries(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var writer pgx.Tx
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1052_row_versions.sql" {
			return nil
		}
		var err error
		writer, err = d.App.Begin(t.Context())
		if err != nil {
			return err
		}
		_, err = writer.Exec(t.Context(), `LOCK TABLE harness_sessions IN ROW EXCLUSIVE MODE`)
		return err
	})
	if writer == nil {
		t.Fatal("lock contender never started")
	}
	defer writer.Rollback(t.Context())
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("want bounded lock timeout, got %v", err)
	}
	var recorded, installed bool
	if err := d.App.QueryRow(t.Context(), `SELECT
        EXISTS(SELECT 1 FROM schema_migrations WHERE version='1052_row_versions.sql'),
        EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='harness_sessions'::regclass AND attname='row_version' AND NOT attisdropped)`).Scan(&recorded, &installed); err != nil {
		t.Fatal(err)
	}
	if recorded || installed {
		t.Fatal("timed-out installation was not rolled back")
	}
	if err := writer.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

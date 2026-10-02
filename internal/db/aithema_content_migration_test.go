// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestAithemaContentMigrationLockReleaseAndUniqueRecovery(t *testing.T) {
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
	defer func() {
		if writer != nil {
			_ = writer.Rollback(t.Context())
		}
	}()
	var beforeFile, invalidOID uint32
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		switch name {
		case "1100_aithema_pending_content.sql":
			return d.App.QueryRow(t.Context(), `SELECT relfilenode FROM pg_class WHERE oid='aithema_journal_records'::regclass`).Scan(&beforeFile)
		case "1101_aithema_content_bytes_validate.sql":
			var pending, oldCheck bool
			var afterFile uint32
			if err := d.App.QueryRow(t.Context(), `SELECT
				EXISTS(SELECT 1 FROM pg_constraint WHERE conname='aithema_journal_records_content_bytes_check' AND NOT convalidated),
				EXISTS(SELECT 1 FROM pg_constraint WHERE conname='aithema_journal_records_original_bytes_check'),
				(SELECT relfilenode FROM pg_class WHERE oid='aithema_journal_records'::regclass)`).Scan(&pending, &oldCheck, &afterFile); err != nil {
				return err
			}
			if !pending || oldCheck || beforeFile != afterFile {
				return fmt.Errorf("expansion scanned/rewrote table or failed to replace check: pending=%v old=%v", pending, oldCheck)
			}
			var err error
			writer, err = d.App.Begin(t.Context())
			if err != nil {
				return err
			}
			// Validation must finish while existing writers hold their lock.
			_, err = writer.Exec(t.Context(), `LOCK TABLE aithema_journal_records IN ROW EXCLUSIVE MODE`)
			return err
		case "1102_aithema_content_address.sql":
			var validated bool
			if err := d.App.QueryRow(t.Context(), `SELECT convalidated FROM pg_constraint WHERE conname='aithema_journal_records_content_bytes_check'`).Scan(&validated); err != nil {
				return err
			}
			if writer == nil || !validated {
				return fmt.Errorf("validation did not complete with the writer open")
			}
			// Cancel the real concurrent unique build while that writer remains
			// open, exercising recovery from an invalid partial unique index.
			builder, err := d.App.Acquire(t.Context())
			if err != nil {
				return err
			}
			defer builder.Release()
			if _, err := builder.Exec(t.Context(), `SET statement_timeout='200ms'`); err != nil {
				return err
			}
			body, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			_, buildErr := builder.Exec(t.Context(), string(body))
			if _, err := builder.Exec(t.Context(), `RESET statement_timeout`); err != nil {
				return err
			}
			if buildErr == nil {
				return fmt.Errorf("concurrent index did not wait for the writer")
			}
			var valid, unique bool
			if err := builder.QueryRow(t.Context(), `SELECT indexrelid,indisvalid,indisunique FROM pg_index WHERE indexrelid='aithema_journal_content_address'::regclass`).Scan(&invalidOID, &valid, &unique); err != nil {
				return err
			}
			if valid || !unique {
				return fmt.Errorf("interrupted unique index has unexpected validity/uniqueness")
			}
			return writer.Rollback(t.Context())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var oid uint32
	if err := d.App.QueryRow(t.Context(), `SELECT indexrelid FROM pg_index WHERE indexrelid='aithema_journal_content_address'::regclass AND indisvalid AND indisunique`).Scan(&oid); err != nil || oid == invalidOID {
		t.Fatalf("invalid unique index was not rebuilt: %v", err)
	}
	const migration = "1102_aithema_content_address.sql"
	if _, err := d.App.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version=$1`, migration); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	var replayOID uint32
	if err := d.App.QueryRow(t.Context(), `SELECT indexrelid FROM pg_index WHERE indexrelid='aithema_journal_content_address'::regclass`).Scan(&replayOID); err != nil || oid != replayOID {
		t.Fatal("valid unrecorded unique index was rebuilt")
	}
	// A valid nonunique index with the same name must never satisfy UNIQUE.
	if _, err := d.App.Exec(t.Context(), `DROP INDEX aithema_journal_content_address;
		DELETE FROM schema_migrations WHERE version='1102_aithema_content_address.sql';
		CREATE INDEX aithema_journal_content_address ON aithema_journal_records(tenant_id,sid,content_sha256) WHERE content_sha256 IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err == nil {
		t.Fatal("recorded a nonunique index as a unique content address")
	}
	var recorded bool
	if err := d.App.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, migration).Scan(&recorded); err != nil || recorded {
		t.Fatal("failed unique migration was recorded")
	}
}

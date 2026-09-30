// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestConcurrentMigrationRecovery(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	interrupted := errors.New("simulated process interruption")
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		switch name {
		case "0901_session_message_validation.sql":
			var pending int
			if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conname IN ('inbox_messages_recipient_session_fk','inbox_messages_sender_session_fk','inbox_compat_recipient_session_fk','inbox_compat_sender_session_fk','inbox_reply_obligations_closed_reason_check','inbox_reply_obligations_closure') AND NOT convalidated`).Scan(&pending); err != nil {
				return err
			}
			if pending != 6 {
				return fmt.Errorf("constraints validated before lock release: %d pending", pending)
			}
		case "0902_inbox_session_pending.sql":
			// Cancel while CREATE waits for a writer after its initial commit.
			writer, err := d.App.Begin(t.Context())
			if err != nil {
				return err
			}
			defer writer.Rollback(t.Context())
			if _, err := writer.Exec(t.Context(), `LOCK TABLE inbox_messages IN ROW EXCLUSIVE MODE`); err != nil {
				return err
			}
			builder, err := d.App.Acquire(t.Context())
			if err != nil {
				return err
			}
			defer builder.Release()
			if _, err := builder.Exec(t.Context(), `SET statement_timeout='100ms'`); err != nil {
				return err
			}
			_, buildErr := builder.Exec(t.Context(), `CREATE INDEX CONCURRENTLY inbox_session_pending ON inbox_messages(tenant_id,recipient_session_id,sent_event_id) WHERE recipient_session_id IS NOT NULL AND acked_at IS NULL`)
			if _, err := builder.Exec(t.Context(), `RESET statement_timeout`); err != nil {
				return err
			}
			if buildErr == nil {
				return errors.New("expected cancelled index build")
			}
			var valid bool
			if err := builder.QueryRow(t.Context(), `SELECT indisvalid FROM pg_index WHERE indexrelid='inbox_session_pending'::regclass`).Scan(&valid); err != nil {
				return err
			}
			if valid {
				return errors.New("failed build left valid index")
			}
		case "0903_inbox_compat_session.sql":
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	var originalOID uint32
	if err := d.App.QueryRow(t.Context(), `SELECT indexrelid FROM pg_index WHERE indexrelid='inbox_session_pending'::regclass AND indisvalid`).Scan(&originalOID); err != nil {
		t.Fatal(err)
	}
	// Model a crash between successful CREATE and recording the migration.
	if _, err := d.App.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version='0902_inbox_session_pending.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	var oid uint32
	var indexes, validated, recorded int
	if err := d.App.QueryRow(t.Context(), `SELECT indexrelid FROM pg_index WHERE indexrelid='inbox_session_pending'::regclass AND indisvalid`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if oid != originalOID {
		t.Fatal("valid unrecorded index was rebuilt")
	}
	if err := d.App.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM pg_index WHERE indexrelid IN ('inbox_session_pending'::regclass,'inbox_compat_session'::regclass) AND indisvalid),
  (SELECT count(*) FROM pg_constraint WHERE conname IN ('inbox_messages_recipient_session_fk','inbox_messages_sender_session_fk','inbox_compat_recipient_session_fk','inbox_compat_sender_session_fk','inbox_reply_obligations_closed_reason_check','inbox_reply_obligations_closure') AND convalidated),
  (SELECT count(*) FROM schema_migrations WHERE version IN ('0902_inbox_session_pending.sql','0903_inbox_compat_session.sql'))`).Scan(&indexes, &validated, &recorded); err != nil {
		t.Fatal(err)
	}
	if indexes != 2 || validated != 6 || recorded != 2 {
		t.Fatalf("indexes=%d validated=%d recorded=%d", indexes, validated, recorded)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, func(name string) error { return fmt.Errorf("replayed %s", name) }); err != nil {
		t.Fatal(err)
	}
}

// Exercise the actual AEON-345 and AEON-329 SQL, including both crash windows: an INVALID
// partial build, and a successful build whose migration record was never saved.
func TestSessionThreadConcurrentMigrationRecovery(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	// The historical token migration leases a second connection. Inspect both
	// afterward so the setting-restoration assertion covers the DDL connection.
	cfg := d.App.Config()
	cfg.MaxConns = 2
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "7s"
	runner, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.Close)
	checkTimeout := func() {
		t.Helper()
		for range cfg.MaxConns {
			conn, err := runner.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			var timeout string
			if err := conn.QueryRow(t.Context(), `SHOW lock_timeout`).Scan(&timeout); err != nil {
				t.Fatal(err)
			}
			if timeout != "7s" {
				t.Fatalf("migration leaked lock_timeout: %s", timeout)
			}
		}
	}
	files := map[string]string{
		"0992_session_thread_lookups.sql": "inbox_compat_sender_session",
		"0993_session_thread_replies.sql": "inbox_compat_reply",
		"0996_session_ticket_index.sql":   "harness_sessions_ticket_node",
	}
	invalidOIDs := make(map[string]uint32)
	err = db.MigrateWithHook(t.Context(), runner, func(name string) error {
		index, ok := files[name]
		if !ok {
			return nil
		}
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		writer, err := d.App.Begin(t.Context())
		if err != nil {
			return err
		}
		defer writer.Rollback(t.Context())
		table := "inbox_compat_messages"
		if name == "0996_session_ticket_index.sql" {
			table = "harness_sessions"
		}
		if _, err := writer.Exec(t.Context(), `LOCK TABLE `+table+` IN ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		builder, err := d.App.Acquire(t.Context())
		if err != nil {
			return err
		}
		defer builder.Release()
		if _, err := builder.Exec(t.Context(), `SET statement_timeout='200ms'`); err != nil {
			return err
		}
		_, buildErr := builder.Exec(t.Context(), string(body))
		if _, err := builder.Exec(t.Context(), `RESET statement_timeout`); err != nil {
			return err
		}
		if buildErr == nil {
			return fmt.Errorf("%s: expected interrupted build", name)
		}
		var valid bool
		var oid uint32
		if err := builder.QueryRow(t.Context(), `SELECT indexrelid, indisvalid FROM pg_index WHERE indexrelid=to_regclass($1)`, index).Scan(&oid, &valid); err != nil {
			return fmt.Errorf("%s did not leave an index while a writer held the table: %w", name, err)
		}
		if valid {
			return fmt.Errorf("%s: interrupted build is valid", name)
		}
		invalidOIDs[index] = oid
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkTimeout()
	validOIDs := make(map[string]uint32)
	for name, index := range files {
		var oid uint32
		if err := d.App.QueryRow(t.Context(), `SELECT indexrelid FROM pg_index WHERE indexrelid=to_regclass($1) AND indisvalid`, index).Scan(&oid); err != nil {
			t.Fatal(err)
		}
		if invalidOIDs[index] == 0 || oid == invalidOIDs[index] {
			t.Fatalf("%s: invalid partial index was not rebuilt", name)
		}
		validOIDs[index] = oid
		if tag, err := d.App.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version=$1`, name); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("remove successful migration record %s: %v (%d rows)", name, err, tag.RowsAffected())
		}
	}
	if err := db.MigrateWithHook(t.Context(), runner, nil); err != nil {
		t.Fatal(err)
	}
	for name, index := range files {
		var oid uint32
		var recorded bool
		if err := d.App.QueryRow(t.Context(), `SELECT indexrelid, EXISTS(SELECT 1 FROM schema_migrations WHERE version=$2)
			FROM pg_index WHERE indexrelid=to_regclass($1) AND indisvalid`, index, name).Scan(&oid, &recorded); err != nil {
			t.Fatal(err)
		}
		if oid != validOIDs[index] || !recorded {
			t.Fatalf("%s: valid unrecorded index was rebuilt or not recorded", name)
		}
	}
	if err := db.MigrateWithHook(t.Context(), runner, func(name string) error { return fmt.Errorf("replayed %s", name) }); err != nil {
		t.Fatal(err)
	}
	checkTimeout()
	// IF NOT EXISTS must not record success when a non-index relation collides.
	if _, err := d.App.Exec(t.Context(), `DROP INDEX inbox_compat_reply;
		DELETE FROM schema_migrations WHERE version='0993_session_thread_replies.sql';
		CREATE TABLE inbox_compat_reply(id int)`); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), runner, nil); err == nil {
		t.Fatal("recorded a conflicting table as a valid index")
	}
	checkTimeout()
	var recorded bool
	if err := d.App.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='0993_session_thread_replies.sql')`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded {
		t.Fatal("failed migration was recorded")
	}
}

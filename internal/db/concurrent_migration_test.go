// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"fmt"
	"testing"

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

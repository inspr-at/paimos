// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestMoreHarnessesValidationReleasesInstallationLocksAndResumes(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	const name = "1088_more_harnesses.sql"
	interrupted := errors.New("simulated stop after checks installed")
	err = db.MigrateWithHook(t.Context(), d.App, func(phase string) error {
		if phase == name+"#validate" {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("installation did not commit separately: %v", err)
	}
	var pending int
	var recorded bool
	if err := d.App.QueryRow(t.Context(), `SELECT
        (SELECT count(*) FROM pg_constraint WHERE conname LIKE '%_v2_check' AND NOT convalidated),
        EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&pending, &recorded); err != nil || pending != 10 || recorded {
		t.Fatalf("interrupted installation: pending=%d recorded=%v err=%v", pending, recorded, err)
	}
	var writer pgx.Tx
	err = db.MigrateWithHook(t.Context(), d.App, func(phase string) error {
		switch phase {
		case name + "#validate":
			var err error
			writer, err = d.App.Begin(t.Context())
			if err != nil {
				return err
			}
			// This writer remains open across every VALIDATE. It cannot coexist
			// with installation's ACCESS EXCLUSIVE locks.
			_, err = writer.Exec(t.Context(), `SET LOCAL lock_timeout='1s'; LOCK TABLE harness_sessions, work_order_reviews IN ROW EXCLUSIVE MODE`)
			return err
		case name + "#replace":
			var validated int
			if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conname LIKE '%_v2_check' AND convalidated`).Scan(&validated); err != nil {
				return err
			}
			if writer == nil || validated != 10 {
				return fmt.Errorf("writer-concurrent validation did not finish: %d", validated)
			}
			// Retiring old enums needs a short exclusive DDL lock again.
			return writer.Rollback(t.Context())
		}
		return nil
	})
	if writer != nil {
		defer writer.Rollback(t.Context())
	}
	if err != nil || writer == nil {
		t.Fatalf("resume with writer: %v", err)
	}
	var checkpoints int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations WHERE version LIKE $1`, name+"#check-phase:%").Scan(&checkpoints); err != nil || checkpoints != 0 {
		t.Fatalf("completed migration retained temporary checkpoints: %d %v", checkpoints, err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal("repeat migration:", err)
	}
}

func TestMoreHarnessesMigrationPreservesReviewChecks(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		t.Run(fmt.Sprintf("renamed=%t", renamed), func(t *testing.T) {
			d, err := dbtest.NewUnmigrated(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := d.Close(); err != nil {
					t.Error(err)
				}
			})
			var pairingBefore string
			err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
				if name != "1088_more_harnesses.sql" {
					return nil
				}
				if err := d.App.QueryRow(t.Context(), `SELECT pg_get_constraintdef(oid) FROM pg_constraint
                    WHERE conrelid='work_order_reviews'::regclass AND contype='c'
                    AND pg_get_constraintdef(oid) LIKE '%reviewer_profile_id IS NULL%'
                    AND pg_get_constraintdef(oid) LIKE '%reviewer_family IS NULL%'`).Scan(&pairingBefore); err != nil {
					return err
				}
				if !renamed {
					return nil
				}
				// Constraint names vary with PostgreSQL's naming and older DDL.
				// Rename every replaced enum and the pairing check to prove the
				// upgrade selects predicates rather than guessing their names.
				rows, err := d.App.Query(t.Context(), `SELECT conrelid::regclass::text, conname FROM pg_constraint
                    WHERE contype='c' AND conrelid IN ('model_profiles'::regclass,'agent_accounts'::regclass,
                    'harness_sessions'::regclass,'account_groups'::regclass,'account_ticket_pins'::regclass,
                    'inbox_message_targets'::regclass,'rule_served_manifests'::regclass,'work_order_reviews'::regclass)
                    AND (pg_get_constraintdef(oid) ~ '\m(harness|family|adapter|author_family|reviewer_family)\M\s*=\s*ANY\s*\('
                         OR pg_get_constraintdef(oid) LIKE '%reviewer_profile_id IS NULL%') ORDER BY oid`)
				if err != nil {
					return err
				}
				type constraint struct{ table, name string }
				var constraints []constraint
				for rows.Next() {
					var c constraint
					if err := rows.Scan(&c.table, &c.name); err != nil {
						rows.Close()
						return err
					}
					constraints = append(constraints, c)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return err
				}
				if len(constraints) != 11 {
					return fmt.Errorf("historical enum/pairing checks: %d, want 11", len(constraints))
				}
				for i, c := range constraints {
					if _, err := d.App.Exec(t.Context(), fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO historical_enum_%d", c.table, c.name, i)); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := d.App.Query(t.Context(), `SELECT pg_get_constraintdef(oid), convalidated FROM pg_constraint
                WHERE conrelid='work_order_reviews'::regclass AND contype='c'
                AND (pg_get_constraintdef(oid) LIKE '%reviewer_family%' OR pg_get_constraintdef(oid) LIKE '%author_family%')`)
			if err != nil {
				t.Fatal(err)
			}
			var pairingAfter, reviewerEnum, authorEnum string
			for rows.Next() {
				var def string
				var validated bool
				if err := rows.Scan(&def, &validated); err != nil {
					t.Fatal(err)
				}
				if !validated {
					t.Errorf("check remains unvalidated: %s", def)
				}
				switch {
				case strings.Contains(def, "reviewer_profile_id IS NULL"):
					pairingAfter = def
				case strings.Contains(def, "reviewer_family"):
					reviewerEnum = def
				default:
					authorEnum = def
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if pairingBefore == "" || pairingAfter != pairingBefore {
				t.Fatalf("null pairing changed: before=%s after=%s", pairingBefore, pairingAfter)
			}
			for _, def := range []string{reviewerEnum, authorEnum} {
				for _, family := range []string{"openai", "anthropic", "xai", "cursor", "google", "local"} {
					if !strings.Contains(def, "'"+family+"'::text") {
						t.Fatalf("missing %s in widened enum: %s", family, def)
					}
				}
			}
			if !strings.Contains(reviewerEnum, "reviewer_family <> author_family") {
				t.Fatal("cross-family rule lost:", reviewerEnum)
			}
			var count int
			if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conname LIKE '%_v2_check' AND convalidated`).Scan(&count); err != nil || count != 10 {
				t.Fatalf("validated widened checks: %d, want 10: %v", count, err)
			}
		})
	}
}

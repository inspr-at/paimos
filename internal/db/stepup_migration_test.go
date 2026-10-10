// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: expansion rejects 1092 writers or rollback retains widened checks/table.
func TestStepupMigrationExpansionRollbackAndLegacyKinds(t *testing.T) {
	d, err := dbtest.NewUnmigrated(dbtest.Seed(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	body, err := os.ReadFile("migrations/1307_stepup_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1307_stepup_requests.sql" {
			return nil
		}
		checked = true
		rollback := errors.New("rollback trial")
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, "00000000-0000-4000-8000-000000000000", func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), string(body)); err != nil {
				return err
			}
			var ok bool
			if err := tx.QueryRow(t.Context(), `SELECT to_regclass('stepup_requests') IS NOT NULL AND (SELECT bool_and(pg_get_constraintdef(oid) LIKE '%stepup%') FROM pg_constraint WHERE conname IN ('phone_approval_challenges_kind_stepup_check','phone_push_deliveries_kind_stepup_check'))`).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errors.New("expansion missing table/kinds")
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			return fmt.Errorf("rollback trial: %w", err)
		}
		var absent bool
		if err := d.App.QueryRow(t.Context(), `SELECT to_regclass('stepup_requests') IS NULL AND (SELECT count(*)=2 FROM pg_constraint WHERE conname IN ('phone_approval_challenges_kind_check','phone_push_deliveries_kind_check'))`).Scan(&absent); err != nil {
			return err
		}
		if !absent {
			return errors.New("rollback retained schema")
		}
		return nil
	})
	if err != nil || !checked {
		t.Fatalf("migration %v checked=%v", err, checked)
	}
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name)VALUES('migration-stepup','Step-up') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		var person string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name)VALUES($1,'person','Approver') RETURNING id::text`, tid).Scan(&person); err != nil {
			return err
		}
		for _, kind := range []string{"registration", "approval", "attach", "stepup"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO phone_approval_challenges(tenant_id,person_id,kind,binding,session_data)VALUES($1,$2,$3,'fixture','{}')`, tid, person, kind); err != nil {
				return err
			}
		}
		var sub string
		if err := tx.QueryRow(t.Context(), `INSERT INTO phone_push_subscriptions(tenant_id,person_id,endpoint_hash,subscription)VALUES($1,$2,'fixture','fixture')RETURNING id::text`, tid, person).Scan(&sub); err != nil {
			return err
		}
		for _, kind := range []string{"approval", "attach", "stepup"} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO phone_push_deliveries(tenant_id,person_id,kind,request_id,subscription_id)VALUES($1,$2,$3,gen_random_uuid(),$4)`, tid, person, kind, sub); err != nil {
				return err
			}
		}
		var forced bool
		if err := tx.QueryRow(t.Context(), `SELECT relrowsecurity AND relforcerowsecurity FROM pg_class WHERE oid='stepup_requests'::regclass`).Scan(&forced); err != nil {
			return err
		}
		if !forced {
			return errors.New("native table not FORCE RLS")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

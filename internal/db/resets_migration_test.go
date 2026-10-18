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

// Risk: the additive reset schema rejects old account writers, a rolled-back
// expansion leaves half a schema, or action state crosses tenant boundaries.
func TestResetMigrationExpansionRollbackOldWriterAndRLS(t *testing.T) {
	d, err := dbtest.NewUnmigrated(dbtest.Seed(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	body, err := os.ReadFile("migrations/1303_account_resets.sql")
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1303_account_resets.sql" {
			return nil
		}
		checked = true
		rollback := errors.New("rollback reset expansion")
		ctx := dbtest.Seed(t.Context())
		err := db.InTenant(ctx, d.App, "00000000-0000-4000-8000-000000000000", func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return err
			}
			var present bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass('account_reset_actions') IS NOT NULL AND EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='agent_accounts' AND column_name='reset_report')`).Scan(&present); err != nil {
				return err
			}
			if !present {
				return errors.New("reset expansion missing schema")
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			return fmt.Errorf("reset trial rollback: %w", err)
		}
		var absent bool
		if err := d.App.QueryRow(ctx, `SELECT to_regclass('account_reset_actions') IS NULL AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='agent_accounts' AND column_name='reset_report')`).Scan(&absent); err != nil {
			return err
		}
		if !absent {
			return errors.New("reset rollback retained schema")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("migration hook did not execute")
	}
	ctx := dbtest.Seed(t.Context())
	for i := range 2 {
		var tid string
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Reset migration') RETURNING id::text`, fmt.Sprintf("reset-migration-%d", i)).Scan(&tid); err != nil {
			t.Fatal(err)
		}
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			var agent, account string
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Old writer') RETURNING id::text`, tid).Scan(&agent); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'old','codex','old',$2,'Old writer') RETURNING id::text`, tid, agent).Scan(&account); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO account_reset_actions(tenant_id,account_id,state) VALUES($1,$2,'succeeded')`, tid, account); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_reset_actions`).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("reset action RLS leaked %d rows", count)
			}
			var nullable bool
			if err := tx.QueryRow(ctx, `SELECT reset_report IS NULL AND reset_policy_person_id IS NULL AND reset_policy_link_revision IS NULL FROM agent_accounts WHERE id=$1`, account).Scan(&nullable); err != nil {
				return err
			}
			if !nullable {
				return errors.New("old writer acquired reset consent")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

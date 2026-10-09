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

// Risk: expansion rejects older writers or retains partial schema on rollback;
// immutable readiness observations lose the first reading or cross tenants.
func TestDailyPlanMigrationExpansionRollbackAndRLS(t *testing.T) {
	d, err := dbtest.NewUnmigrated(dbtest.Seed(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	body, err := os.ReadFile("migrations/1302_agents_daily_plan.sql")
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1302_agents_daily_plan.sql" {
			return nil
		}
		checked = true
		rollback := errors.New("rollback expansion")
		ctx := dbtest.Seed(t.Context())
		err := db.InTenant(ctx, d.App, "00000000-0000-4000-8000-000000000000", func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return err
			}
			var present bool
			if err := tx.QueryRow(ctx, `SELECT to_regclass('account_daily_observations') IS NOT NULL AND to_regprocedure('aeon_capture_daily_observation()') IS NOT NULL`).Scan(&present); err != nil {
				return err
			}
			if !present {
				return fmt.Errorf("expansion missing objects")
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			return fmt.Errorf("trial rollback: %w", err)
		}
		var absent bool
		if err := d.App.QueryRow(ctx, `SELECT to_regclass('account_daily_observations') IS NULL AND to_regprocedure('aeon_capture_daily_observation()') IS NULL AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='personal_profiles' AND column_name='daily_points_per_day')`).Scan(&absent); err != nil {
			return err
		}
		if !absent {
			return errors.New("trial rollback retained schema")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("expansion hook did not run")
	}
	ctx := dbtest.Seed(t.Context())
	for i := range 2 {
		var tid string
		if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Daily migration') RETURNING id::text`, fmt.Sprintf("daily-migration-%d", i)).Scan(&tid); err != nil {
			t.Fatal(err)
		}
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			var person, account string
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, tid).Scan(&person); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id) VALUES($1,$2)`, tid, person); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,owner_person_id) VALUES($1,'old-writer','codex','old-daemon',$2,'Old writer',$2) RETURNING id::text`, tid, person).Scan(&account); err != nil {
				return err
			}
			var resource string
			if err := tx.QueryRow(ctx, `INSERT INTO account_readiness_resources(tenant_id,kind,identity_kind,identity_key) VALUES($1,'subscription_quota','account',repeat('a',64)) RETURNING id::text`, tid).Scan(&resource); err != nil {
				return err
			}
			// Older readiness writer is unchanged; the additive trigger retains both
			// samples even though the fact row itself is overwritten.
			if _, err := tx.Exec(ctx, `INSERT INTO account_readiness_facts(tenant_id,resource_id,window_key,reported_by_account_id,binding_revision,source,observed_at,reading_at,resets_at,used_percent) VALUES($1,$2,'weekly',$3,0,'harness',now(),now()-interval '1 hour',now()+interval '1 week',40)`, tid, resource, account); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE account_readiness_facts SET reading_at=now(),used_percent=52 WHERE resource_id=$1`, resource); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE account_readiness_facts SET observed_at=now() WHERE resource_id=$1`, resource); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_daily_observations`).Scan(&count); err != nil {
				return err
			}
			if count != 2 {
				return fmt.Errorf("observation dedupe/RLS count=%d", count)
			}
			var baseline float64
			if err := tx.QueryRow(ctx, `SELECT used_pct FROM account_daily_observations ORDER BY read_at LIMIT 1`).Scan(&baseline); err != nil {
				return err
			}
			if baseline != 40 {
				return errors.New("first observation was overwritten")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

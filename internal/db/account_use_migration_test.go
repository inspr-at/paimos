// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: FORCE RLS skips existing tenants, backfill silently activates their
// rollback floor, or retries widen choices/reset confirmation and duplicate audit.
// Check the backfill before 1329 intentionally retires shipped_only, then prove
// that the complete upgrade changes only the legacy off policy and its fence.
func TestAccountUseMigrationBackfillIdempotentAndPreservesBehavior(t *testing.T) {
	beforeCanonicalRules := errors.New("before canonical model rules")
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var ids []string
	ctx := dbtest.Seed(t.Context())
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name == "1329_retired_model_auto_update.sql" {
			return beforeCanonicalRules
		}
		if name != "1309_account_use_matrix.sql" {
			return nil
		}
		for i := range 2 {
			var id string
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Previous binary tenant') RETURNING id::text`, fmt.Sprintf("previous-%d", i)).Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			if err := db.InTenant(ctx, d.App, id, func(tx pgx.Tx) error {
				var agent string
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Existing daemon') RETURNING id::text`, id).Scan(&agent); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'existing','codex','old',$2,'Existing login')`, id, agent); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Existing project' FROM node_kinds k WHERE k.slug='project'`, id); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO model_refresh_settings(tenant_id,auto_add_profiles) VALUES($1,$2)`, id, i == 0)
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if !errors.Is(err, beforeCanonicalRules) {
		t.Fatalf("expected stop before canonical model rules, got %v", err)
	}
	if len(ids) != 2 {
		t.Fatal("pre-expansion fixture never ran")
	}
	revisions := make([]int64, len(ids))
	for i, id := range ids {
		if err := db.InTenant(ctx, d.App, id, func(tx pgx.Tx) error {
			var allowed, unactivated, confirmation, idempotent bool
			var models string
			var contexts, mappings, cells, audits int
			if err := tx.QueryRow(ctx, `SELECT new_accounts='allow' AND new_contexts='allow' AND new_projects='default',enforced_at IS NULL,confirmation_required,new_models,(SELECT count(*) FROM work_contexts),(SELECT count(*) FROM project_work_contexts),(SELECT count(*) FROM account_use_cells),(SELECT count(*) FROM events WHERE type='account_use.migrated') FROM account_use_rules`).Scan(&allowed, &unactivated, &confirmation, &models, &contexts, &mappings, &cells, &audits); err != nil {
				return err
			}
			wantModels := "allow"
			if i == 1 {
				wantModels = "shipped_only"
			}
			if !allowed || !unactivated || !confirmation || models != wantModels || contexts != 2 || mappings != 1 || cells != 1 || audits != 1 {
				return fmt.Errorf("backfill changed previous behavior: %v %v %v %s %d %d %d %d", allowed, unactivated, confirmation, models, contexts, mappings, cells, audits)
			}
			if err := tx.QueryRow(ctx, `SELECT revision FROM account_use_rules`).Scan(&revisions[i]); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT NOT aeon_seed_account_use($1,true) AND NOT aeon_seed_account_use($1,false)`, id).Scan(&idempotent); err != nil {
				return err
			}
			if !idempotent {
				return fmt.Errorf("seed retry rewrote migrated state")
			}
			var clean bool
			if err := tx.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(metadata->>'migration'='1309' AND metadata->>'policy'='preserve_pre_matrix_behaviour') FROM events WHERE type='account_use.migrated'`).Scan(&clean); err != nil {
				return err
			}
			if !clean {
				return fmt.Errorf("migration audit changed or duplicated")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	verifyUpgrade := func() {
		t.Helper()
		for i, id := range ids {
			if err := db.InTenant(ctx, d.App, id, func(tx pgx.Tx) error {
				var allowed, unactivated, confirmation, auto bool
				var models string
				var revision int64
				var contexts, mappings, cells, audits int
				if err := tx.QueryRow(ctx, `SELECT new_accounts='allow' AND new_contexts='allow' AND new_projects='default',enforced_at IS NULL,confirmation_required,new_models,revision,(SELECT auto_add_profiles FROM model_refresh_settings),(SELECT count(*) FROM work_contexts),(SELECT count(*) FROM project_work_contexts),(SELECT count(*) FROM account_use_cells),(SELECT count(*) FROM events WHERE type='account_use.migrated') FROM account_use_rules`).Scan(&allowed, &unactivated, &confirmation, &models, &revision, &auto, &contexts, &mappings, &cells, &audits); err != nil {
					return err
				}
				wantModels, wantRevision, wantChanges := "allow", revisions[i], 0
				if i == 1 {
					wantModels, wantRevision, wantChanges = "deny", revisions[i]+1, 1
				}
				if !allowed || unactivated != (i == 0) || !confirmation || models != wantModels || revision != wantRevision || auto != (i == 0) || contexts != 2 || mappings != 1 || cells != 1 || audits != 1 {
					return fmt.Errorf("complete upgrade changed unexpected behavior for tenant %s: %v %v %v %s revision=%d auto=%v %d %d %d %d", id, allowed, unactivated, confirmation, models, revision, auto, contexts, mappings, cells, audits)
				}
				var changes, correct int
				if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE type='account_use.rules_changed' AND metadata->>'ticket'='AEON-1148' AND before->>'new_models'='shipped_only' AND after->>'new_models'='deny' AND (before->>'revision')::bigint=$1 AND (after->>'revision')::bigint=$2 AND before->>'enforced_at' IS NULL AND after->>'enforced_at' IS NOT NULL) FROM events WHERE metadata->>'migration'='1329'`, revisions[i], wantRevision).Scan(&changes, &correct); err != nil {
					return err
				}
				if changes != wantChanges || correct != wantChanges {
					return fmt.Errorf("tenant %s: %d canonical rule changes, %d correct, want %d", id, changes, correct, wantChanges)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	verifyUpgrade()
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
	verifyUpgrade()
}

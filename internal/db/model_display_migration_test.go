// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestModelDisplayMigrationPreservesPinsAcrossTenants(t *testing.T) {
	ctx := dbtest.Seed(t.Context())
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenants, profiles []string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1074_model_display_effort.sql" {
			return nil
		}
		for i := 0; i < 2; i++ {
			var tenantID, profileID string
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Display upgrade') RETURNING id::text`, fmt.Sprintf("display-upgrade-%d", i)).Scan(&tenantID); err != nil {
				return err
			}
			if err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier)
     VALUES($1,'upgrade-sol','registry-revision-99','codex','openai','gpt-6.1-sol','xhigh','standard') RETURNING id::text`, tenantID).Scan(&profileID)
			}); err != nil {
				return err
			}
			tenants = append(tenants, tenantID)
			profiles = append(profiles, profileID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var unscoped int
	if err := d.App.QueryRow(ctx, `SELECT count(*) FROM model_profile_display`).Scan(&unscoped); err != nil {
		t.Fatal(err)
	}
	if unscoped != 0 {
		t.Fatal("metadata visible without tenant scope")
	}
	for i, tenantID := range tenants {
		if err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
			var id, revision, name, short, version, provider string
			var level, count int
			if err := tx.QueryRow(ctx, `SELECT p.id::text,p.version,d.model_display->>'display_name',d.model_display->>'short_name',d.model_display->>'model_version',d.effort_level,d.provider
    FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id`).Scan(&id, &revision, &name, &short, &version, &level, &provider); err != nil {
				return err
			}
			if id != profiles[i] || revision != "registry-revision-99" || name != "Codex Sol" || short != "Sol" || version != "6.1" || level != 4 || provider != "openai" {
				return fmt.Errorf("wrong backfilled identity: %s %s %s %s %s %d %s", id, revision, name, short, version, level, provider)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profile_display`).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("tenant isolation: %d profiles", count)
			}
			if err := tx.QueryRow(ctx, `SELECT aeon_backfill_model_profile_display()`).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("repeated backfill inserted %d", count)
			}
			// Previous-binary inserts omit the new column and still receive metadata.
			if _, err := tx.Exec(ctx, `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier)
    VALUES($1,'post-upgrade-opus','2','claude','anthropic','opus','high','strong')`, tenantID); err != nil {
				return err
			}
			var aliasVersion string
			if err := tx.QueryRow(ctx, `SELECT d.model_display->>'model_version',d.effort_level FROM model_profile_display d JOIN model_profiles p ON p.tenant_id=d.tenant_id AND p.id=d.profile_id WHERE p.slug='post-upgrade-opus'`).Scan(&aliasVersion, &level); err != nil {
				return err
			}
			if aliasVersion != "" || level != 3 {
				return fmt.Errorf("alias guessed version or lost effort")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE model_profile_display SET effort_level=0 WHERE profile_id=$1`, profiles[i])
			return err
		}); err == nil {
			t.Fatal("metadata mutation accepted")
		}
		if err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE model_profiles SET effort='low' WHERE id=$1`, profiles[i])
			return err
		}); err == nil {
			t.Fatal("historical profile mutation accepted")
		}
	}
	if err := db.MigrateWithHook(ctx, d.App, func(name string) error { return fmt.Errorf("unexpected replay %s", name) }); err != nil {
		t.Fatal(err)
	}
}

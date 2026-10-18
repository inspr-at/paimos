// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Risk: an upgrade can overwrite custom kinds or omit tags, and a seed can
// cross tenant boundaries or duplicate definitions when initialization repeats.
func TestTagKindMigrationSeedsMissingAndPreservesCustomTenants(t *testing.T) {
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
	var missing, custom, fresh, customBefore string
	err = db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1316_default_tag_kind.sql" {
			return nil
		}
		for i, id := range []*string{&missing, &custom} {
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Tag upgrade') RETURNING id::text`, fmt.Sprintf("tag-upgrade-%d", i)).Scan(id); err != nil {
				return err
			}
		}
		if err := db.InTenant(ctx, d.App, missing, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_kinds WHERE slug='tag'`).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("upgrade fixture already has %d tags", count)
			}
			_, err := tx.Exec(ctx, `DELETE FROM node_kinds WHERE slug='guideline'`)
			return err
		}); err != nil {
			return err
		}
		return db.InTenant(ctx, d.App, custom, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds,field_schema)
				VALUES($1,'tag','Custom label','LBL','circle',ARRAY[]::text[],
				'{"type":"object","properties":{"color":{"type":"string"}},"required":["color"]}'::jsonb)`, custom)
			if err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT row_to_json(k)::text FROM node_kinds k WHERE slug='tag'`).Scan(&customBefore)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Tenant triggers must restore the caller's scope after initializing a new tenant.
	err = db.InTenant(ctx, d.App, missing, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('tag-fresh','Tag fresh') RETURNING id::text`).Scan(&fresh); err != nil {
			return err
		}
		var scope string
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&scope); err != nil {
			return err
		}
		if scope != missing {
			return fmt.Errorf("tenant creation left scope %q, want %q", scope, missing)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, tenantID := range []string{missing, custom, fresh} {
		err := db.InTenant(ctx, d.App, tenantID, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_kinds WHERE slug='tag'`).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("tenant %s sees %d tag kinds, want exactly one", tenantID, count)
			}
			var id, before, after string
			if err := tx.QueryRow(ctx, `SELECT id::text,row_to_json(k)::text FROM node_kinds k WHERE slug='tag'`).Scan(&id, &before); err != nil {
				return err
			}
			if ids[id] {
				return fmt.Errorf("tenants share tag identity %s", id)
			}
			ids[id] = true
			if tenantID == custom {
				if before != customBefore {
					return fmt.Errorf("custom tag definition changed: %s", before)
				}
			} else {
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_kinds WHERE slug='tag' AND tenant_id=$1
					AND label='Tag' AND short_prefix='TAG' AND icon='tag' AND allowed_child_kinds IS NULL AND field_schema='{}'::jsonb`, tenantID).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					return fmt.Errorf("default tag definition is wrong")
				}
			}
			for range 2 {
				if _, err := tx.Exec(ctx, `SELECT aeon_seed_tag_kind($1::uuid)`, tenantID); err != nil {
					return err
				}
			}
			if err := tx.QueryRow(ctx, `SELECT row_to_json(k)::text FROM node_kinds k WHERE slug='tag'`).Scan(&after); err != nil {
				return err
			}
			if after != before {
				return fmt.Errorf("repeated seeding changed the tag definition")
			}
			if tenantID == missing {
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_kinds WHERE slug='guideline'`).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return fmt.Errorf("tag backfill restored a deleted starter kind")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	err = db.InTenant(ctx, d.App, missing, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT aeon_seed_tag_kind($1::uuid)`, custom)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "tenant setting does not match tag-kind tenant") {
		t.Fatalf("cross-tenant initialization: %v", err)
	}
	if err := db.MigrateWithHook(ctx, d.App, func(name string) error { return fmt.Errorf("unexpected migration replay %s", name) }); err != nil {
		t.Fatal(err)
	}
}

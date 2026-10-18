// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestExistingTenantsAndBootstrapGetAuditedPorcelain(t *testing.T) {
	ctx := t.Context()
	d, err := dbtest.NewUnmigrated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenants []string
	if err := db.MigrateWithHook(ctx, d.App, func(name string) error {
		if name != "1206_themes.sql" {
			return nil
		}
		for _, slug := range []string{"theme-old-one", "theme-old-two"} {
			var id string
			if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&id); err != nil {
				return err
			}
			tenants = append(tenants, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// New-tenant trigger is also exercised as a non-bypass app owner under the
	// bootstrap sentinel; it must restore the surrounding transaction's context.
	if err := db.InTenant(db.NoProjects(ctx, "theme bootstrap test"), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.system','off',true)`); err != nil {
			return err
		}
		var id, tid, system string
		if err := tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('theme-new','Theme new') RETURNING id::text`).Scan(&id); err != nil {
			return err
		}
		tenants = append(tenants, id)
		if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id'),current_setting('aeon.system')`).Scan(&tid, &system); err != nil {
			return err
		}
		if tid != "00000000-0000-0000-0000-000000000000" || system != "off" {
			t.Fatalf("bootstrap changed surrounding context: %q %q", tid, system)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 3 {
		t.Fatal("existing/new tenant fixtures missing")
	}
	for _, tid := range tenants {
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			theme, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM themes WHERE tenant_id=$1 AND scope='default'`, tid))
			if err != nil {
				return err
			}
			if theme.Name != "Porcelain" || !same(theme.Values, Porcelain()) {
				t.Fatalf("wrong seed: %+v", theme)
			}
			var raw []byte
			var n, before, after int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='theme.created'`).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Fatalf("seed event count: %d", n)
			}
			if err := tx.QueryRow(ctx, `SELECT after FROM events WHERE type='theme.created'`).Scan(&raw); err != nil {
				return err
			}
			var audited Theme
			if err := json.Unmarshal(raw, &audited); err != nil {
				return err
			}
			// Postgres JSON timestamps use the server's zone; pgx can return UTC.
			// Compare the instants first, then the full snapshots in one zone.
			if !theme.CreatedAt.Equal(audited.CreatedAt) || !theme.UpdatedAt.Equal(audited.UpdatedAt) {
				t.Fatalf("seed audit timestamps mismatch: %+v / %+v", theme, audited)
			}
			theme.CreatedAt, theme.UpdatedAt = theme.CreatedAt.UTC(), theme.UpdatedAt.UTC()
			audited.CreatedAt, audited.UpdatedAt = audited.CreatedAt.UTC(), audited.UpdatedAt.UTC()
			if !same(theme, audited) {
				t.Fatalf("seed audit mismatch: %+v / %+v", theme, audited)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&before); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT aeon_seed_theme($1)`, tid); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&after); err != nil {
				return err
			}
			if after != before {
				t.Fatal("idempotent seed appended events")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(ctx, d.App, func(name string) error { t.Fatalf("replayed completed migration %s", name); return nil }); err != nil {
		t.Fatal(err)
	}
}

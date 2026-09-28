// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestPortalMigrationDoesNotResurrectDeletedStarterKinds(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var id string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "0899_product_portal.sql" {
			return nil
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('portal-keep','Portal Keep') RETURNING id::text`).Scan(&id); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			tag, err := tx.Exec(t.Context(), `DELETE FROM node_kinds WHERE slug='guideline'`)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				t.Fatalf("deleted %d guideline kinds", tag.RowsAffected())
			}
			var portal int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug IN ('portal_product','portal_feature','portal_wish')`).Scan(&portal); err != nil {
				return err
			}
			if portal != 0 {
				t.Fatalf("portal kinds existed before the portal migration: %d", portal)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
		var guideline, portal int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug='guideline'`).Scan(&guideline); err != nil {
			return err
		}
		if guideline != 0 {
			t.Fatal("portal backfill restored a deleted starter kind")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug IN ('portal_product','portal_feature','portal_wish')`).Scan(&portal); err != nil {
			return err
		}
		if portal != 3 {
			t.Fatalf("portal kinds after backfill: %d", portal)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var created string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('portal-new','Portal New') RETURNING id::text`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, created, func(tx pgx.Tx) error {
		var guideline, portal int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug='guideline'`).Scan(&guideline); err != nil {
			return err
		}
		if guideline != 1 {
			t.Fatalf("new tenant guideline kinds: %d", guideline)
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug IN ('portal_product','portal_feature','portal_wish')`).Scan(&portal); err != nil {
			return err
		}
		if portal != 3 {
			t.Fatalf("new tenant portal kinds: %d", portal)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

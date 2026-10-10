// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestHumanCheckMigrationUpgradesExistingStrictSchemasUnderRLS(t *testing.T) {
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
	err = migrateLegacyWorkWithHook(t, d, func(name string) error {
		if name != "1076_ticket_human_check.sql" {
			return nil
		}
		for _, slug := range []string{"human-check-upgrade-one", "human-check-upgrade-two"} {
			var id string
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema='{"type":"object","additionalProperties":false,"properties":{"custom":{"type":"string"}},"required":["custom"]}' WHERE slug IN ('ticket','task')`); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields) SELECT $1,id,'HC-1','Historical','open','{"custom":"keep"}' FROM node_kinds WHERE slug='ticket'`, id)
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM node_kinds WHERE slug IN ('ticket','task') AND field_schema->'properties' ? 'human_check_completed' AND field_schema->'properties' ? 'custom' AND field_schema->>'additionalProperties'='false' AND field_schema->'required'='["custom"]'::jsonb`).Scan(&count); err != nil {
				return err
			}
			if count != 2 {
				t.Fatalf("existing strict ticket/task schemas were not extended in tenant %s", id)
			}
			var fields []byte
			var check *string
			if err := tx.QueryRow(t.Context(), `SELECT fields,human_check FROM nodes WHERE key='HC-1'`).Scan(&fields, &check); err != nil {
				return err
			}
			var object map[string]any
			if err := json.Unmarshal(fields, &object); err != nil {
				return err
			}
			if check != nil || len(object) != 1 || object["custom"] != "keep" {
				t.Fatalf("historical ticket changed: %s, pending %v", fields, check)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateLegacyWorkWithHook(t, d, func(name string) error { t.Fatalf("replayed migration %s", name); return nil }); err != nil {
		t.Fatal(err)
	}
}

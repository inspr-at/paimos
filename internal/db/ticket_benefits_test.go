// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestTicketBenefitMigrationPreservesHistoryAndCustomSchema(t *testing.T) {
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
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "0896_ticket_benefits.sql" {
			return nil
		}
		for _, slug := range []string{"benefit-old-one", "benefit-old-two"} {
			var id string
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema='{"type":"object","additionalProperties":false,"properties":{"custom":{"type":"string"}}}' WHERE slug='ticket'`); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields) SELECT $1,id,'TEST-1','Historical','done','{"custom":"keep"}' FROM node_kinds WHERE slug='ticket'`, id)
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
		err = db.InTenant(dbtest.Seed(context.Background()), d.App, id, func(tx pgx.Tx) error {
			var schema, fields []byte
			var state string
			if err := tx.QueryRow(t.Context(), `SELECT k.field_schema,n.fields,n.state FROM node_kinds k JOIN nodes n ON n.kind_id=k.id AND n.tenant_id=k.tenant_id WHERE k.slug='ticket'`).Scan(&schema, &fields, &state); err != nil {
				return err
			}
			if state != "done" || strings.Contains(string(fields), "benefit") || !strings.Contains(string(fields), "keep") {
				t.Fatalf("rewritten history: %s %s", state, fields)
			}
			var parsed struct {
				Properties map[string]any `json:"properties"`
				Additional bool           `json:"additionalProperties"`
			}
			if err := json.Unmarshal(schema, &parsed); err != nil {
				return err
			}
			if len(parsed.Properties) != 6 || parsed.Additional {
				t.Fatalf("custom schema lost: %s", schema)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal("reapply:", err)
	}
}

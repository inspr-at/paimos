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

func TestTicketRouteMigrationPreservesCustomSchema(t *testing.T) {
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
		if name != "0988_ticket_route_fields.sql" {
			return nil
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('route-old','route-old') RETURNING id::text`).Scan(&id); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema='{"type":"object","additionalProperties":false,"properties":{"custom":{"type":"string"}}}' WHERE slug='ticket'`); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields) SELECT $1,id,'TEST-1','Historical','open','{"custom":"keep"}' FROM node_kinds WHERE slug='ticket'`, id)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(context.Background()), d.App, id, func(tx pgx.Tx) error {
		var schema, fields []byte
		if err := tx.QueryRow(t.Context(), `SELECT k.field_schema,n.fields FROM node_kinds k JOIN nodes n ON n.kind_id=k.id AND n.tenant_id=k.tenant_id WHERE k.slug='ticket'`).Scan(&schema, &fields); err != nil {
			return err
		}
		if string(fields) != `{"custom": "keep"}` && string(fields) != `{"custom":"keep"}` {
			t.Fatalf("rewritten history: %s", fields)
		}
		var parsed struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Additional *bool                      `json:"additionalProperties"`
		}
		if err := json.Unmarshal(schema, &parsed); err != nil {
			return err
		}
		if parsed.Additional == nil || *parsed.Additional || len(parsed.Properties) != 9 {
			t.Fatalf("custom schema: %s", schema)
		}
		for _, key := range []string{"custom", "route_role", "area", "route_role_by", "area_at"} {
			if _, ok := parsed.Properties[key]; !ok {
				t.Fatalf("missing %s in %s", key, schema)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var fresh string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('route-new','route-new') RETURNING id::text`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(context.Background()), d.App, fresh, func(tx pgx.Tx) error {
		var ticket, task string
		if err := tx.QueryRow(t.Context(), `SELECT field_schema::text FROM node_kinds WHERE slug='ticket'`).Scan(&ticket); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT field_schema::text FROM node_kinds WHERE slug='task'`).Scan(&task); err != nil {
			return err
		}
		if !strings.Contains(ticket, "pill_en") || !strings.Contains(ticket, "route_role") || !strings.Contains(task, `"area"`) {
			t.Fatalf("seed ticket %s task %s", ticket, task)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal("reapply:", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// 1048 adds roadmap publication fields onto a ticket kind that already has a
// tenant schema. Custom properties, constraints, and stored node values stay.
func TestRoadmapPublicationMigrationPreservesCustomSchema(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	const ticketSchema = `{"type":"object","additionalProperties":false,"required":["custom"],"properties":{"custom":{"type":"string","description":"tenant note"},"legacy_score":{"type":"integer","minimum":0}}}`
	const taskSchema = `{"type":"object","additionalProperties":false,"properties":{"task_note":{"type":"string"}}}`
	var id string
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1048_roadmap_publication.sql" {
			return nil
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('roadmap-old','roadmap-old') RETURNING id::text`).Scan(&id); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=$1::jsonb WHERE slug='ticket'`, ticketSchema); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=$1::jsonb WHERE slug='task'`, taskSchema); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,fields) SELECT $1,id,'TEST-1','Historical','open','{"custom":"keep","legacy_score":2}' FROM node_kinds WHERE slug='ticket'`, id)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(context.Background()), d.App, id, func(tx pgx.Tx) error {
		var schema, fields, task []byte
		if err := tx.QueryRow(t.Context(), `SELECT k.field_schema,n.fields FROM node_kinds k JOIN nodes n ON n.kind_id=k.id AND n.tenant_id=k.tenant_id WHERE k.slug='ticket'`).Scan(&schema, &fields); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT field_schema FROM node_kinds WHERE slug='task'`).Scan(&task); err != nil {
			return err
		}
		if !jsonEqual(t, string(fields), `{"custom":"keep","legacy_score":2}`) {
			t.Fatalf("rewritten history: %s", fields)
		}
		if !jsonEqual(t, string(task), taskSchema) {
			t.Fatalf("task schema changed: %s", task)
		}
		var parsed struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Additional *bool                      `json:"additionalProperties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(schema, &parsed); err != nil {
			return err
		}
		if parsed.Type != "object" || parsed.Additional == nil || *parsed.Additional || len(parsed.Required) != 1 || parsed.Required[0] != "custom" || len(parsed.Properties) != 6 {
			t.Fatalf("custom schema lost: %s", schema)
		}
		for _, key := range []string{"custom", "legacy_score", "roadmap_public", "roadmap_public_source", "roadmap_public_by", "roadmap_public_at"} {
			if _, ok := parsed.Properties[key]; !ok {
				t.Fatalf("missing %s in %s", key, schema)
			}
		}
		if !jsonEqual(t, string(parsed.Properties["custom"]), `{"type":"string","description":"tenant note"}`) {
			t.Fatalf("custom property changed: %s", parsed.Properties["custom"])
		}
		if !jsonEqual(t, string(parsed.Properties["legacy_score"]), `{"type":"integer","minimum":0}`) {
			t.Fatalf("legacy_score changed: %s", parsed.Properties["legacy_score"])
		}
		var published struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(parsed.Properties["roadmap_public"], &published); err != nil {
			return err
		}
		if published.Type != "boolean" {
			t.Fatalf("roadmap_public: %s", parsed.Properties["roadmap_public"])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var fresh string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('roadmap-new','roadmap-new') RETURNING id::text`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(context.Background()), d.App, fresh, func(tx pgx.Tx) error {
		var ticket string
		if err := tx.QueryRow(t.Context(), `SELECT field_schema::text FROM node_kinds WHERE slug='ticket'`).Scan(&ticket); err != nil {
			return err
		}
		var parsed struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal([]byte(ticket), &parsed); err != nil {
			return err
		}
		for _, key := range []string{"pill_en", "route_role", "roadmap_public", "roadmap_public_source", "roadmap_public_by", "roadmap_public_at"} {
			if _, ok := parsed.Properties[key]; !ok {
				t.Fatalf("seed ticket missing %s: %s", key, ticket)
			}
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

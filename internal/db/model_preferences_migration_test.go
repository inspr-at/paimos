// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRunResidencyMigrationValidatesDeferredConstraints(t *testing.T) {
	raw, err := os.ReadFile("migrations/1106_work_placement_and_run_residency.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Join(strings.Fields(string(raw)), " ")
	if !strings.Contains(sql, "ADD COLUMN residency text,") || !strings.Contains(sql, "ADD COLUMN prefs_person_id uuid,") {
		t.Fatal("run columns must be plain nullable additions")
	}
	for _, constraint := range []string{"agent_runs_residency_check", "agent_runs_prefs_person_fk"} {
		add := regexp.MustCompile(`ADD CONSTRAINT ` + constraint + ` [^;]+? NOT VALID`).FindStringIndex(sql)
		validate := strings.Index(sql, "VALIDATE CONSTRAINT "+constraint)
		if add == nil || validate < add[1] {
			t.Fatalf("%s must be installed NOT VALID before validation", constraint)
		}
	}
	d := dbtest.Open(t)
	var count int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint WHERE conrelid='agent_runs'::regclass AND conname IN ('agent_runs_residency_check','agent_runs_prefs_person_fk') AND convalidated`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("migration left residency constraints unvalidated", count)
	}
}

func TestModelPreferencesMigrationSeedsEveryTenantUnderRLS(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenants []string
	err = migrateLegacyWorkWithHook(t, d, func(name string) error {
		if name != "1104_work_kinds.sql" {
			return nil
		}
		for i := 0; i < 2; i++ {
			var id string
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Prefs upgrade') RETURNING id::text`, fmt.Sprintf("prefs-upgrade-%d", i)).Scan(&id); err != nil {
				return err
			}
			tenants = append(tenants, id)
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET field_schema=jsonb_set(field_schema,'{properties,custom}', '{"type":"string"}'::jsonb) WHERE slug='ticket'`)
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
	for _, id := range tenants {
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			var n int
			var schema []byte
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM work_kinds`).Scan(&n); err != nil {
				return err
			}
			if n != 9 {
				t.Fatal("seed/RLS", n)
			}
			if err := tx.QueryRow(t.Context(), `SELECT field_schema FROM node_kinds WHERE slug='ticket'`).Scan(&schema); err != nil {
				return err
			}
			if !strings.Contains(string(schema), "custom") || !strings.Contains(string(schema), "pattern") {
				t.Fatal("lost schema extension", string(schema))
			}
			_, err := tx.Exec(t.Context(), `UPDATE node_kinds SET label=label WHERE slug='ticket'`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := tenantbootstrap.Create(t.Context(), d.App, "prefs-new", "New tenant")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM work_kinds`).Scan(&n); err != nil {
			return err
		}
		if n != 9 {
			t.Fatal("bootstrap lacks kinds", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

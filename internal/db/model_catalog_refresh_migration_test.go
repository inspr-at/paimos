// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCatalogRefreshExpansionPreservesPublishedRoutes(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenantID, foreignTenantID, profileID, oldRoute, oldConstraint string
	constraintSQL := `SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid='model_role_routes'::regclass AND conname='model_role_routes_role_check'`
	routeSQL := `SELECT to_jsonb(r)::text FROM model_role_routes r WHERE role='build'`
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1072_model_catalog_refresh.sql" {
			return nil
		}
		if err := d.App.QueryRow(t.Context(), constraintSQL).Scan(&oldConstraint); err != nil {
			return err
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('catalog-upgrade','Catalog upgrade') RETURNING id::text`).Scan(&tenantID); err != nil {
			return err
		}
		if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('catalog-foreign','Foreign') RETURNING id::text`).Scan(&foreignTenantID); err != nil {
			return err
		}
		return db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier)
				VALUES($1,'legacy-pin','2','codex','openai','legacy-model','high','standard') RETURNING id::text`, tenantID).Scan(&profileID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state,reason,valid_until)
				VALUES($1,'build',1,$2,'conserved','Existing owner override',now()+interval '1 day')`, tenantID, profileID); err != nil {
				return err
			}
			return tx.QueryRow(t.Context(), routeSQL).Scan(&oldRoute)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	var constraint string
	if err := d.App.QueryRow(t.Context(), constraintSQL).Scan(&constraint); err != nil {
		t.Fatal(err)
	}
	if constraint != oldConstraint || constraint == "" {
		t.Fatal("published role constraint changed")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		var route string
		if err := tx.QueryRow(t.Context(), routeSQL).Scan(&route); err != nil {
			return err
		}
		if route != oldRoute || route == "" {
			t.Fatal("published route or active override changed")
		}
		// SQL issued by a previous binary still writes its original role table.
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id)
			VALUES($1,'scout',1,$2)`, tenantID, profileID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO model_security_role_routes(tenant_id,role,priority,profile_id)
			VALUES($1,'review-gate-security',1,$2)`, tenantID, profileID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// The new role must never become visible to previous-binary route readers.
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id)
			VALUES($1,'review-gate-security',1,$2)`, tenantID, profileID)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("published role CHECK did not reject the new role: %v", err)
	}
	for _, table := range []string{"model_security_role_routes", "model_refresh_settings", "model_observations", "model_report_receipts", "model_discovery_credentials"} {
		var enabled, forced, policy bool
		err := d.App.QueryRow(t.Context(), `SELECT c.relrowsecurity,c.relforcerowsecurity,
			EXISTS(SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid AND p.polqual IS NOT NULL AND p.polwithcheck IS NOT NULL)
			FROM pg_class c WHERE c.oid=$1::regclass`, table).Scan(&enabled, &forced, &policy)
		if err != nil || !enabled || !forced || !policy {
			t.Fatalf("%s lacks forced tenant RLS: %v", table, err)
		}
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, foreignTenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_security_role_routes`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("security routes leaked across tenants")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, foreignTenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_security_role_routes(tenant_id,role,priority,profile_id)
			VALUES($1,'review-gate-security',2,$2)`, tenantID, profileID)
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("tenant RLS did not reject foreign security route write: %v", err)
	}
}

func TestCatalogRefreshObservationsAcceptModelHarnesses(t *testing.T) {
	d, err := dbtest.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	var tenantID string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name)
		VALUES('catalog-observations','Catalog observations') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	for _, observation := range []struct {
		harness, model, effort string
	}{
		{"gemini", "gemini-2.5-pro", "16384"},
		{"opencode", "local-model", "high"},
	} {
		t.Run(observation.harness, func(t *testing.T) {
			err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
				var harness, model, effort string
				if err := tx.QueryRow(t.Context(), `INSERT INTO model_observations(tenant_id,harness,model,effort,source)
					VALUES($1,$2,$3,$4,'agent') RETURNING harness,model,effort`,
					tenantID, observation.harness, observation.model, observation.effort).Scan(&harness, &model, &effort); err != nil {
					return err
				}
				if harness != observation.harness || model != observation.model || effort != observation.effort {
					t.Fatalf("observation tuple changed: %s / %s / %s", harness, model, effort)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, harness := range []string{"media", "terminal"} {
		t.Run(harness, func(t *testing.T) {
			err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO model_observations(tenant_id,harness,model,effort,source)
					VALUES($1,$2,'local-model','high','agent')`, tenantID, harness)
				return err
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "model_observations_harness_check" {
				t.Fatalf("non-model harness was not rejected by the harness CHECK: %v", err)
			}
		})
	}
}

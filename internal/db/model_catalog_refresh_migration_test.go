// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"errors"
	"fmt"
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
		if name != "1126_model_catalog_refresh.sql" {
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

// Risk R7/R14: the 5.5 rollout rewrites immutable pins/history, crosses tenants,
// loses a pause or effort, or silently enables models despite an owner's deny.
func TestAnthropic55MigrationKeepsHistoryAndActivationPolicy(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	ids := []string{}
	before := map[string]string{}
	snapshot := `SELECT jsonb_agg(jsonb_build_object('pin',to_jsonb(p),'display',to_jsonb(d)) ORDER BY p.slug)::text FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id WHERE p.version='2'`
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1357_model_catalog_55.sql" {
			return nil
		}
		for i := 0; i < 2; i++ {
			var id string
			if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Model upgrade') RETURNING id::text`, fmt.Sprintf("model-55-%d", i)).Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			if err := db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled,registered_effort_level)
     VALUES($1,'pi-anthropic-opus-high','2','pi','anthropic','anthropic/claude-opus-5','high','strong',true,3),
     ($1,'pi-anthropic-opus-xhigh','2','pi','anthropic','anthropic/claude-opus-5','xhigh','strong',true,4),
     ($1,'pi-anthropic-sonnet-high','2','pi','anthropic','anthropic/claude-sonnet-5','high','standard',true,3)`, id)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_pauses(tenant_id,harness,model,reason,set_by) SELECT $1,'pi','anthropic/claude-opus-5','Owner pause',aeon_authz_system_actor($1)`, id); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by)
 SELECT $1,'pi','anthropic/claude-opus-5.5',CASE WHEN $2 THEN now()+interval '1 hour' END,
 CASE WHEN $2 THEN 'Weaker successor pause' ELSE 'Existing strict pause' END,aeon_authz_system_actor($1)`, id, i == 0); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,allowed_model_profile_ids)
      SELECT $1,'pi','pi','fixture',aeon_authz_system_actor($1),'Pi',array_agg(id) FROM model_profiles WHERE slug='pi-anthropic-opus-xhigh'`, id); err != nil {
					return err
				}
				if i == 1 {
					if _, err := tx.Exec(t.Context(), `UPDATE account_use_rules SET new_models='deny',enforced_at=coalesce(enforced_at,now())`); err != nil {
						return err
					}
				}
				var raw string
				if err := tx.QueryRow(t.Context(), snapshot).Scan(&raw); err != nil {
					return err
				}
				before[id] = raw
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Re-executing the insert-only data step is harmless, beyond migration tracking.
	if _, err = d.App.Exec(t.Context(), `SELECT aeon_upgrade_anthropic_55()`); err != nil {
		t.Fatal("replay:", err)
	}
	for i, id := range ids {
		err = db.InTenant(dbtest.Seed(t.Context()), d.App, id, func(tx pgx.Tx) error {
			var raw string
			if err := tx.QueryRow(t.Context(), snapshot).Scan(&raw); err != nil {
				return err
			}
			if raw != before[id] {
				t.Fatal("old immutable pins or displays changed")
			}
			var count, enabled, display, pauses, events int
			if err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE p.enabled),count(*) FILTER(WHERE d.model_display->>'model_version'='5.5' AND p.registered_effort_level IN (3,4))
    FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id WHERE p.version='aeon-1409' AND p.model IN ('anthropic/claude-opus-5.5','anthropic/claude-sonnet-5.5')`).Scan(&count, &enabled, &display); err != nil {
				return err
			}
			wantEnabled := 3
			if i == 1 {
				wantEnabled = 0
			}
			if count != 3 || enabled != wantEnabled || display != 3 {
				t.Fatalf("tenant %d: versions=%d enabled=%d display=%d", i, count, enabled, display)
			}
			pauseReason := "Owner pause"
			if i == 1 {
				pauseReason = "Existing strict pause"
			}
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_pauses WHERE model='anthropic/claude-opus-5.5' AND until_at IS NULL AND reason=$1`, pauseReason).Scan(&pauses); err != nil {
				return err
			}
			if pauses != 1 {
				t.Fatal("successor lost owner pause")
			}
			var grantsKept, successorAllowed bool
			if err := tx.QueryRow(t.Context(), `SELECT allowed_model_profile_ids=ARRAY[(SELECT id FROM model_profiles WHERE slug='pi-anthropic-opus-xhigh' AND version='2')],aeon_account_allows_profile(harness,allowed_model_profile_ids,(SELECT id FROM model_profiles WHERE slug='pi-anthropic-opus-xhigh' AND version='aeon-1409')) FROM agent_accounts`).Scan(&grantsKept, &successorAllowed); err != nil {
				return err
			}
			if !grantsKept || !successorAllowed {
				t.Fatal("migration rewrote grants or lost qualified successor policy")
			}
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='model.profile_upgraded' AND metadata->>'migration'='1357'`).Scan(&events); err != nil {
				return err
			}
			if events != 3 {
				t.Fatalf("migration audit duplicated or missing: %d", events)
			}
			if i == 0 {
				var retained bool
				if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE type='model.profile_upgraded'
 AND after->'pause_before'->>'reason'='Weaker successor pause'
 AND after->'pause_before'->>'until_at' IS NOT NULL
 AND after->'pause_after'->>'reason'='Owner pause'
 AND after->'pause_after'->>'until_at' IS NULL)`).Scan(&retained); err != nil {
					return err
				}
				if !retained {
					t.Fatal("extending the successor pause lost its prior audit value")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateWithHook(t.Context(), d.App, nil); err != nil {
		t.Fatal(err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package modelactivation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func fixture(t *testing.T, d *dbtest.DB, slug, rule string, activated bool) tenant.Principal {
	t.Helper()
	p := tenant.Principal{Kind: tenant.Person}
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Admin',ARRAY['admin']) RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE account_use_rules SET new_models=$1,enforced_at=CASE WHEN $2 THEN now() ELSE NULL END`, rule, activated)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, p.TenantID, p.ID)
	return p
}

func fenced(t *testing.T, d *dbtest.DB, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	ctx := tenant.WithPrincipal(t.Context(), p)
	if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := Lock(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func pin(slug string) Pin {
	return Pin{Slug: slug, Version: "1", Harness: "codex", Family: "openai", Model: "gpt-6-sol", Effort: "high", Tier: "standard", Enabled: true, Source: "manual", Permission: "models.manage"}
}

// Risk: any writer/cause silently widens shipped-only or deny, or activation
// unexpectedly changes pre-floor behavior. Persisted audits must match decisions.
func TestModelActivationCauseRuleMatrix(t *testing.T) {
	d := dbtest.Open(t)
	for _, rule := range []string{"allow", "shipped_only", "deny"} {
		for _, cause := range []Cause{ShippedCatalog, VendorSuccessor, Person, ""} {
			name := rule + "-" + string(cause)
			t.Run(name, func(t *testing.T) {
				p := fixture(t, d, name, rule, true)
				want := rule == "allow" || cause == Person || rule == "shipped_only" && cause == ShippedCatalog
				var id string
				fenced(t, d, p, func(tx pgx.Tx) error {
					out, err := Activate(t.Context(), tx, p, pin("matrix"), cause)
					if err != nil {
						return err
					}
					id = out.ID
					if out.Enabled != want {
						return fmt.Errorf("enabled=%v want %v", out.Enabled, want)
					}
					var remaining string
					if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.model_activation_cause',true)`).Scan(&remaining); err != nil {
						return err
					}
					if remaining != "" {
						return fmt.Errorf("unconsumed cause %q", remaining)
					}
					return nil
				})
				fenced(t, d, p, func(tx pgx.Tx) error {
					var audits, observations int
					if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM events WHERE type='model.activation_withheld' AND after->>'profile_id'=$1),(SELECT count(*) FROM model_observations)`, id).Scan(&audits, &observations); err != nil {
						return err
					}
					if want && (audits != 0 || observations != 0) || !want && (audits != 1 || observations != 1) {
						return fmt.Errorf("audits=%d observations=%d enabled=%v", audits, observations, want)
					}
					if !want {
						var recordedRule, recordedCause string
						if err := tx.QueryRow(t.Context(), `SELECT after->>'rule',after->>'cause' FROM events WHERE type='model.activation_withheld'`).Scan(&recordedRule, &recordedCause); err != nil {
							return err
						}
						expected := string(cause)
						if expected == "" {
							expected = "none"
						}
						if recordedRule != rule || recordedCause != expected {
							return fmt.Errorf("wrong audit %s/%s", recordedRule, recordedCause)
						}
					}
					return nil
				})
			})
		}
	}
	// An unactivated migrated tenant with shipped-only keeps old behavior.
	p := fixture(t, d, "unactivated", "shipped_only", false)
	fenced(t, d, p, func(tx pgx.Tx) error {
		out, err := Activate(t.Context(), tx, p, pin("legacy"), "")
		if err == nil && !out.Enabled {
			return errors.New("pre-activation insert withheld")
		}
		return err
	})
}

// Risk: disabled rows and multi-row SQL leak a person cause to later inserts.
func TestModelActivationConsumesDisabledAndMultiRowCauses(t *testing.T) {
	d := dbtest.Open(t)
	for _, rule := range []string{"shipped_only", "deny"} {
		p := fixture(t, d, rule, rule, true)
		fenced(t, d, p, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.model_activation_cause','person',true)`); err != nil {
				return err
			}
			var disabled, later bool
			if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled) VALUES($1,'disabled','1','codex','openai','gpt-6-sol','high','standard',false) RETURNING enabled`, p.TenantID).Scan(&disabled); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'later','1','codex','openai','gpt-6-sol','high','standard') RETURNING enabled`, p.TenantID).Scan(&later); err != nil {
				return err
			}
			if disabled || later {
				return errors.New("disabled row leaked its cause")
			}
			if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.model_activation_cause','person',true)`); err != nil {
				return err
			}
			rows, err := tx.Query(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'first','1','codex','openai','gpt-6-sol','high','standard'),($1,'second','1','codex','openai','gpt-6-sol','high','standard') RETURNING slug,enabled`, p.TenantID)
			if err != nil {
				return err
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var slug string
				var enabled bool
				if err := rows.Scan(&slug, &enabled); err != nil {
					return err
				}
				count++
				if enabled != (slug == "first") {
					return fmt.Errorf("multi-row cause leaked to %s", slug)
				}
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if count != 2 {
				return fmt.Errorf("insert returned %d rows", count)
			}
			return nil
		})
		fenced(t, d, p, func(tx pgx.Tx) error {
			var audits int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='model.activation_withheld' AND after->>'cause'='none'`).Scan(&audits); err != nil {
				return err
			}
			if audits != 2 {
				return fmt.Errorf("wanted two uncaused audits, got %d", audits)
			}
			return nil
		})
	}
}

// Risk: savepoint rollback or denied authority preserves an armed cause.
func TestModelActivationFailedInsertRetryAndPersonAuthority(t *testing.T) {
	d := dbtest.Open(t)
	p := fixture(t, d, "authority", "deny", true)
	fenced(t, d, p, func(tx pgx.Tx) error {
		// Policy must not turn an invalid NULL state into a valid disabled pin.
		sub, err := tx.Begin(t.Context())
		if err != nil {
			return err
		}
		_, err = sub.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier,enabled) VALUES($1,'null-state','1','codex','openai','gpt-6-sol','high','standard',NULL)`, p.TenantID)
		var nullErr *pgconn.PgError
		if !errors.As(err, &nullErr) || nullErr.Code != "23502" {
			_ = sub.Rollback(t.Context())
			return fmt.Errorf("NULL state bypassed existing constraint: %v", err)
		}
		if err = sub.Rollback(t.Context()); err != nil {
			return err
		}
		invalid := pin("INVALID")
		_, err = Activate(t.Context(), tx, p, invalid, Person)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
			return fmt.Errorf("wrong insert failure: %v", err)
		}
		out, err := Activate(t.Context(), tx, p, pin("retry"), "")
		if err != nil {
			return err
		}
		if out.Enabled {
			return errors.New("failed insert leaked person cause")
		}
		return nil
	})
	// A custom role keeps the existing models.manage permission only.
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH r AS (INSERT INTO roles(tenant_id,key,name) VALUES($1,'model_only','Models only') RETURNING id) INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,'models.manage' FROM r`, p.TenantID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='model_only') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fenced(t, d, p, func(tx pgx.Tx) error {
		if err := authz.RequireTx(t.Context(), tx, p, "models.manage", authz.Scope{}); err != nil {
			return err
		}
		_, err := Activate(t.Context(), tx, p, pin("forbidden"), Person)
		if !errors.Is(err, authz.ErrForbidden) {
			return fmt.Errorf("deny bypassed missing account.use.manage: %v", err)
		}
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.model_activation_cause','person',true)`); err != nil {
			return err
		}
		disabled := pin("edit-disabled")
		disabled.Enabled = false
		out, err := Activate(t.Context(), tx, p, disabled, Person)
		if err != nil || out.Enabled {
			return fmt.Errorf("disabled edit: enabled=%v error=%v", out.Enabled, err)
		}
		var cause string
		if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.model_activation_cause',true)`).Scan(&cause); err != nil {
			return err
		}
		if cause != "" {
			return errors.New("disabled helper leaked cause")
		}
		return nil
	})
}

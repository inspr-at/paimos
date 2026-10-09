// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/jackc/pgx/v5"
)

// Risk: a late first read, additional harness, or catalog upgrade enables pins
// in an empty deny tenant, rewires a ladder, or duplicates withheld observations.
func TestDeniedEmptyCatalogSeedAdditionsAndUpgradeStayWithheld(t *testing.T) {
	for _, oldCatalog := range []bool{false, true} {
		t.Run(fmt.Sprint(oldCatalog), func(t *testing.T) {
			reset(t)
			p := makePrincipal(t, "empty-deny", "person", "Admin", []string{"admin"})
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
					return err
				}
				if err := catalogLock(t.Context(), tx); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE account_use_rules SET new_models='deny'`); err != nil {
					return err
				}
				if oldCatalog {
					// An empty tenant received the historical shipped catalog while
					// deny was already set. Upgrade through the real current paths.
					for _, s := range catalogProfiles() {
						if s.Harness == "gemini" || s.Harness == "opencode" || s.Harness == "grok" && s.Effort == "medium" {
							continue
						}
						out, err := insertActivatedProfile(t.Context(), tx, p, profileWrite{Slug: s.Slug, Version: "2", Harness: s.Harness, Family: s.Family, Model: s.Model, Effort: s.Effort, Tier: s.Tier}, true, modelactivation.ShippedCatalog)
						if err != nil {
							return err
						}
						if out.Enabled {
							return fmt.Errorf("initial historical pin enabled")
						}
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO model_refresh_settings(tenant_id,catalog_version) VALUES($1,'2')`, p.TenantID)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			profiles := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", 200)
			if len(profiles) < len(catalogProfiles()) {
				t.Fatalf("catalog incomplete: %d", len(profiles))
			}
			for _, prof := range profiles {
				if prof.Enabled {
					t.Fatalf("automatic pin enabled under deny: %s", prof.Slug)
				}
			}
			assertState := func() {
				t.Helper()
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					var routes, observations, audits, unobserved int
					if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_role_routes)+(SELECT count(*) FROM model_security_role_routes),(SELECT count(*) FROM model_observations),(SELECT count(*) FROM events WHERE type='model.activation_withheld' AND after->>'rule'='deny' AND after->>'cause'='shipped_catalog'),(SELECT count(*) FROM model_profiles p WHERE NOT EXISTS(SELECT 1 FROM model_observations o WHERE o.harness=p.harness AND o.model=p.model AND o.effort=p.effort))`).Scan(&routes, &observations, &audits, &unobserved); err != nil {
						return err
					}
					if routes != 0 || observations != len(catalogProfiles()) || audits != len(profiles) || unobserved != 0 {
						return fmt.Errorf("routes=%d observations=%d audits=%d profiles=%d unobserved=%d", routes, observations, audits, len(profiles), unobserved)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			assertState()
			again := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", 200)
			if len(again) != len(profiles) {
				t.Fatal("catalog replay duplicated pins")
			}
			assertState()
			// The existing person acceptance flow can enable a withheld pin.
			accepted := decode[Profile](t, &p, http.MethodPost, "/api/models/proposals/accept", `{"harness":"codex","model":"gpt-6.1-sol","effort":"high"}`, 200)
			if !accepted.Enabled {
				t.Fatal("authorized person acceptance stayed disabled")
			}
		})
	}
}

// Risk: denied vendor successors abort refresh or replace qualified ladder pins
// rather than remaining a pending observation for person acceptance.
func TestVendorSuccessorWithheldDoesNotRewireLadder(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "successor-deny", "person", "Admin", []string{"admin"})
	decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", 200)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		if err := catalogLock(t.Context(), tx); err != nil {
			return err
		}
		// A used board line supplies the successor authority independently of
		// the activation policy; deny must still win at the insert boundary.
		changes, err := prepareBoardDefaults(t.Context(), tx, p)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_rules(tenant_id,scope,column_key,line,lock,why) VALUES($1,'workspace','build','openai:sol','top','successor test')`, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE account_use_rules SET new_models='deny'`); err != nil {
			return err
		}
		accepted, err := acceptUsedSuccessor(t.Context(), tx, p, profileWrite{Slug: "discovered-sol", Version: "new", Harness: "codex", Family: "openai", Model: "gpt-6.2-sol", Effort: "high", Tier: "standard"})
		if err != nil {
			return err
		}
		if accepted {
			return fmt.Errorf("withheld successor reported accepted")
		}
		var enabled bool
		var routes int
		if err := tx.QueryRow(t.Context(), `SELECT p.enabled,(SELECT count(*) FROM model_role_routes r WHERE r.profile_id=p.id) FROM model_profiles p WHERE p.slug='discovered-sol'`).Scan(&enabled, &routes); err != nil {
			return err
		}
		if enabled || routes != 0 {
			return fmt.Errorf("withheld successor entered ladder")
		}
		for _, change := range changes {
			if _, err := events.Append(t.Context(), tx, p, change); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: one of the person registry writers omits the final additional
// permission check under deny, or a refusal leaves a partial pin/edit/audit.
func TestPersonModelWritersUnderDenyRecheckPermissions(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "person-deny", "person", "Admin", []string{"admin"})
	before := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", 200)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE account_use_rules SET new_models='deny'`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_observations(tenant_id,harness,model,effort,source) VALUES($1,'codex','gpt-6.8-sol','high','fixture')`, p.TenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `WITH r AS (INSERT INTO roles(tenant_id,key,name) VALUES($1,'model-only','Models only') RETURNING id) INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,id,unnest(ARRAY['models.manage','models.read']) FROM r`, p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='model-only') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	manual := `{"slug":"manual-deny","version":"1","harness":"codex","family":"openai","model":"gpt-6.9-sol","effort":"high","tier":"standard"}`
	proposal := `{"harness":"codex","model":"gpt-6.8-sol","effort":"high"}`
	edit := `{"display_name":"Sol revised","note":"","route":"openai","efforts":["high"]}`
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/api/models", manual},
		{"POST", "/api/models/proposals/accept", proposal},
		{"PUT", "/api/models/lines/codex/gpt-6.1-sol", edit},
	} {
		status, raw := call(t, &p, req.method, req.path, req.body)
		if status != 403 {
			t.Fatalf("%s: expected activation denial, got %d %s", req.path, status, raw)
		}
	}
	after := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", 200)
	if len(after) != len(before) || eventCount(t, p, evProfile) != 0 || eventCount(t, p, "model.proposal_accepted") != 0 || eventCount(t, p, "model.line_edited") != 0 {
		t.Fatal("refused writer left partial pin/edit/audit")
	}
	for _, profile := range after {
		if profile.Retired {
			t.Fatal("refused edit retired its input")
		}
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='admin') WHERE principal_id=$1 AND scope_type='workspace'`, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !decode[Profile](t, &p, "POST", "/api/models", manual, 201).Enabled {
		t.Fatal("authorized manual pin withheld")
	}
	if !decode[Profile](t, &p, "POST", "/api/models/proposals/accept", proposal, 200).Enabled {
		t.Fatal("authorized proposal withheld")
	}
	edited := decode[struct{ Profiles []Profile }](t, &p, "PUT", "/api/models/lines/codex/gpt-6.1-sol", edit, 200)
	if len(edited.Profiles) != 1 || !edited.Profiles[0].Enabled {
		t.Fatal("authorized line edit withheld")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func TestPreparationExactKeyExpiryAtEveryFence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		offsets     []time.Duration
		wantFailure bool
	}{
		{"before", []time.Duration{-time.Microsecond}, false},
		{"equal", []time.Duration{0}, true},
		{"after", []time.Duration{time.Microsecond}, true},
		{"cross_before_flush", []time.Duration{-time.Second, 0}, true},
		{"cross_after_flush", []time.Duration{-time.Second, -time.Microsecond, 0}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			person := makePrincipal(t, "expiry-"+tc.name, "person", "Owner", []string{"admin"})
			p := addPrincipal(t, person.TenantID, "agent", "Reader", []string{"admin"})
			expiry := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			prefix := strings.ReplaceAll(p.ID, "-", "")
			secret := "expiry-fixture"
			sum := sha256.Sum256([]byte(secret))
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,expires_at,created_by_principal_id) VALUES($1,$2,'expiry fixture',$3,$4,ARRAY['models.read'],$5,$6)`, p.TenantID, p.ID, prefix, hex.EncodeToString(sum[:]), expiry, person.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/api/models", nil)
			r.Header.Set("Authorization", "Bearer aeon_"+prefix+"_"+secret)
			calls := 0
			err := PrepareCatalog(t.Context(), appPool, p, CatalogPreparation{Operation: CatalogRead, Request: r, Clock: func(context.Context, pgx.Tx) (time.Time, error) {
				i := calls
				calls++
				if i >= len(tc.offsets) {
					i = len(tc.offsets) - 1
				}
				return expiry.Add(tc.offsets[i]), nil
			}})
			if tc.wantFailure {
				var refusal *workorders.Error
				if !errors.As(err, &refusal) || refusal.Status != 403 || refusal.Message != "key scope required: models.read" {
					t.Fatalf("wrong expiry refusal: %v", err)
				}
				var profiles, routes, events int
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_profiles),(SELECT count(*) FROM model_role_routes),(SELECT count(*) FROM events WHERE type='model.registry_seeded')`).Scan(&profiles, &routes, &events)
				}); err != nil {
					t.Fatal(err)
				}
				if profiles != 0 || routes != 0 || events != 0 {
					t.Fatalf("expired setup persisted: profiles=%d routes=%d seeds=%d", profiles, routes, events)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if eventCount(t, p, evSeeded) != 1 {
				t.Fatal("authorized setup did not commit")
			}
		})
	}
}

func TestInjectedResolverRefusesUnreadyWithoutCatalogWrites(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "pure-resolver", "person", "Owner", []string{"admin"})
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := ResolveReviewFor(t.Context(), tx, p, WorkQuery{AuthorFamily: "openai"}, time.Now())
		return err
	})
	if !errors.Is(err, ErrCatalogNotReady) {
		t.Fatalf("expected typed readiness error, got %v", err)
	}
	if eventCount(t, p, evSeeded) != 0 {
		t.Fatal("resolver initialized catalog")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_profiles`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("resolver persisted profiles")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTransaction(t.Context(), appPool, func(ctx context.Context) error {
		err := PrepareCatalog(ctx, appPool, p, CatalogPreparation{Operation: CatalogRead, Request: httptest.NewRequest("GET", "/api/models", nil)})
		if err == nil || !strings.Contains(err.Error(), "independent transaction") {
			t.Fatalf("retained transaction accepted: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedCatalogReadRequiresReadyAndCurrentAuthority(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "retained-ready", "person", "Owner", []string{"admin"})
	in := CatalogPreparation{Operation: CatalogRead, Request: httptest.NewRequest("GET", "/api/models", nil)}
	if err := PrepareCatalog(t.Context(), appPool, p, in); err != nil {
		t.Fatal(err)
	}
	before := catalogSetupState(t, p)
	if err := db.InTransaction(t.Context(), appPool, func(ctx context.Context) error {
		if err := PrepareCatalog(ctx, appPool, p, in); err != nil {
			t.Fatalf("ready catalog read rejected the retained transaction: %v", err)
		}
		if err := db.InTenant(dbtest.Seed(ctx), appPool, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, p.ID)
			return err
		}); err != nil {
			return err
		}
		if err := PrepareCatalog(ctx, appPool, p, in); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("ready retained read missed current authority loss: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if after := catalogSetupState(t, p); after != before {
		t.Fatal("retained ready read changed catalog or event state")
	}
}

func TestAdditivePreparationPreservesEmptySavedRole(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "empty-upgrade", "person", "Owner", []string{"admin"})
	var originalID string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		cp := catalogProfiles()[0]
		profile, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: cp.Slug, Version: cp.Version, Harness: cp.Harness, Family: cp.Family, Model: cp.Model, Effort: cp.Effort, Tier: cp.Tier})
		if err != nil {
			return err
		}
		originalID = profile.ID
		return insertRoute(t.Context(), tx, p.TenantID, Route{Role: "build", Priority: 17, ProfileID: profile.ID, State: "available"})
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/models", nil)
	for range 2 {
		if err := PrepareCatalog(t.Context(), appPool, p, CatalogPreparation{Operation: CatalogRead, Request: r}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var emptyCount, originalPriority int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM model_role_routes WHERE role='review-gate'),(SELECT priority FROM model_role_routes WHERE role='build' AND profile_id=$1)`, originalID).Scan(&emptyCount, &originalPriority); err != nil {
			return err
		}
		if emptyCount != 0 || originalPriority != 17 {
			t.Fatalf("saved policy changed: empty=%d priority=%d", emptyCount, originalPriority)
		}
		return requireCatalog(t.Context(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, p, evSeeded) != 1 {
		t.Fatal("warm preparation reseeded")
	}
}

func TestPreparationSeedEventFailureRollsBackColdAndAdditiveResources(t *testing.T) {
	for _, additive := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "additive"}[additive], func(t *testing.T) {
			reset(t)
			p := makePrincipal(t, "seed-rollback", "person", "Owner", []string{"admin"})
			if additive {
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					cp := catalogProfiles()[0]
					profile, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: cp.Slug, Version: cp.Version, Harness: cp.Harness, Family: cp.Family, Model: cp.Model, Effort: cp.Effort, Tier: cp.Tier})
					if err != nil {
						return err
					}
					return insertRoute(t.Context(), tx, p.TenantID, Route{Role: "build", Priority: 17, ProfileID: profile.ID, State: "available"})
				}); err != nil {
					t.Fatal(err)
				}
			}
			state := func() string {
				var out string
				if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT jsonb_build_object('profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM model_profiles p),'routes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY role,priority) FROM model_role_routes r),'seeds',(SELECT count(*) FROM events WHERE type='model.registry_seeded'))::text`).Scan(&out)
				}); err != nil {
					t.Fatal(err)
				}
				return out
			}
			before := state()
			if _, err := adminPool.Exec(t.Context(), `CREATE OR REPLACE FUNCTION reject_catalog_seed() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='model.registry_seeded' THEN RAISE EXCEPTION 'catalog seed injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_catalog_seed BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_catalog_seed()`); err != nil {
				t.Fatal(err)
			}
			err := PrepareCatalog(t.Context(), appPool, p, CatalogPreparation{Operation: CatalogRead, Request: httptest.NewRequest("GET", "/api/models", nil)})
			if err == nil || !strings.Contains(err.Error(), "catalog seed injected") {
				t.Fatalf("wrong seed failure: %v", err)
			}
			if before != state() {
				t.Fatal("seed event failure retained catalog changes")
			}
			if _, err := adminPool.Exec(t.Context(), `DROP TRIGGER reject_catalog_seed ON events`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

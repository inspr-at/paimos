// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func incompleteCatalogFixture(t *testing.T, p tenant.Principal) {
	t.Helper()
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

func catalogSetupState(t *testing.T, p tenant.Principal) string {
	t.Helper()
	var state string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT jsonb_build_object(
		 'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM model_profiles p),
		 'routes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY role,priority) FROM model_role_routes r),
		 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e WHERE type='model.registry_seeded'),
		 'counter',(SELECT to_jsonb(c) FROM event_counters c))::text`).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestInvalidInitializerStructureDoesNotPrepareCatalog(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	for _, additive := range []bool{false, true} {
		for _, tc := range []struct{ name, method, path, body, reason string }{
			{"null routes", "PUT", "/api/models/routes", `null`, "routes must be an array"},
			{"role", "PUT", "/api/models/routes", `[{"role":"invalid","priority":1,"profile_id":"` + id + `","state":"available"}]`, "invalid role"},
			{"priority", "PUT", "/api/models/routes", `[{"role":"build","priority":0,"profile_id":"` + id + `","state":"available"}]`, "priority must be positive"},
			{"profile id", "PUT", "/api/models/routes", `[{"role":"build","priority":1,"profile_id":"invalid","state":"available"}]`, "invalid profile id"},
			{"state", "PUT", "/api/models/routes", `[{"role":"build","priority":1,"profile_id":"` + id + `","state":"invalid"}]`, "invalid route state"},
			{"duplicate", "PUT", "/api/models/routes", `[{"role":"build","priority":1,"profile_id":"` + id + `","state":"available"},{"role":"build","priority":2,"profile_id":"` + strings.ToUpper(id) + `","state":"available"}]`, "duplicate route"},
			{"reason", "PUT", "/api/models/routes", `[{"role":"build","priority":1,"profile_id":"` + id + `","state":"unavailable","valid_until":"2030-01-01T00:00:00Z"}]`, "suppression requires a reason"},
			{"expiry", "PUT", "/api/models/routes", `[{"role":"build","priority":1,"profile_id":"` + id + `","state":"unavailable","reason":"hold"}]`, "suppression requires a future expiry"},
			{"resolver role", "GET", "/api/models/resolve?role=invalid", "", "unknown model role"},
			{"resolver family", "GET", "/api/models/resolve?role=build&author_family=invalid", "", "unknown author family"},
			{"resolver review family", "GET", "/api/models/resolve?role=review-gate", "", "review-gate requires author_family"},
			{"resolver harness", "GET", "/api/models/resolve?role=build&harness=invalid", "", "unsupported model harness"},
			{"placement role", "GET", "/api/models/resolve?role=invalid&area=backend", "", "unknown model role"},
			{"placement family", "GET", "/api/models/resolve?role=build&area=backend&author_family=invalid", "", "unknown author family"},
			{"placement harness", "GET", "/api/models/resolve?role=build&area=backend&harness=invalid", "", "unsupported model harness"},
			{"placement person", "GET", "/api/models/resolve?role=build&person_id=invalid", "", "invalid_placement"},
			{"placement complexity", "GET", "/api/models/resolve?role=build&complexity=invalid", "", "invalid_placement"},
			{"placement empty ticket", "GET", "/api/models/resolve?role=build&ticket=", "", "invalid_placement"},
			{"placement absent role", "GET", "/api/models/resolve?area=backend", "", "unknown model role"},
		} {
			t.Run(tc.name+map[bool]string{false: " cold", true: " additive"}[additive], func(t *testing.T) {
				reset(t)
				p := makePrincipal(t, "invalid-initializer", "person", "Owner", []string{"admin"})
				if additive {
					incompleteCatalogFixture(t, p)
				}
				before := catalogSetupState(t, p)
				status, body := call(t, &p, tc.method, tc.path, tc.body)
				if status != http.StatusBadRequest || !strings.Contains(string(body), tc.reason) {
					t.Fatalf("wrong validation refusal %d %s; want %s", status, body, tc.reason)
				}
				if after := catalogSetupState(t, p); after != before {
					t.Fatal("rejected request persisted setup")
				}
			})
		}
	}
}

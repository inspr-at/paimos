// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
)

var (
	adminPool *pgxpool.Pool
	appPool   *pgxpool.Pool
	testDB    *dbtest.DB
	setupOnce sync.Once
	setupErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testDB != nil {
		if err := testDB.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "dbtest cleanup: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func useDB(t *testing.T) {
	t.Helper()
	setupOnce.Do(func() { setupErr = setupDB() })
	if setupErr != nil {
		t.Fatalf("database: %v", setupErr)
	}
}

func setupDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	handle, err := dbtest.New(ctx)
	if err != nil {
		return err
	}
	testDB = handle
	adminPool = handle.Admin
	appPool = handle.App
	return nil
}

func reset(t *testing.T) {
	t.Helper()
	useDB(t)
	if _, err := adminPool.Exec(t.Context(), `TRUNCATE TABLE tenants CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
}

func makePrincipal(t *testing.T, slug, kind, name string, roles []string) tenant.Principal {
	t.Helper()
	var tenantID string
	if err := appPool.QueryRow(t.Context(), `INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&tenantID); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	return addPrincipal(t, tenantID, kind, name, roles)
}

func addPrincipal(t *testing.T, tenantID, kind, name string, roles []string) tenant.Principal {
	t.Helper()
	if roles == nil {
		roles = []string{}
	}
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			INSERT INTO principals (tenant_id, kind, name, roles)
			VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`, tenantID, kind, name, roles).Scan(&id)
	})
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	dbtest.BindLegacy(t, testDB, tenantID, id)
	if kind == "agent" && len(roles) > 0 {
		dbtest.BindRole(t, testDB, tenantID, id, roles[0])
	}
	k := tenant.Person
	if kind == "agent" {
		k = tenant.Agent
	}
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: k, Name: name, Roles: roles}
}

func call(t *testing.T, p *tenant.Principal, method, path, body string) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	// Legacy invariants remain tested during the read-only transition. Real
	// public retirement is covered separately with Module.Mount.
	module := &Module{pool: appPool}
	if strings.HasPrefix(path, "/api/model-preferences/levels/") {
		module.mount(mux, module.writePreferences)
	} else {
		module.Mount(mux)
	}
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if p != nil {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), *p))
		if p.Kind == tenant.Person && method != http.MethodGet && strings.HasPrefix(path, "/api/model-preferences/levels/person") {
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				person, err := modelprefs.CanonicalPerson(t.Context(), tx, p.ID)
				if err == nil && person != nil {
					r.Header.Set("If-Prefs-Person", *person)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if p.Kind == tenant.Agent {
			// A real scoped fixture key; current scopes are read from storage.
			prefix := strings.ReplaceAll(p.ID, "-", "")
			secret := "model-read-fixture"
			sum := sha256.Sum256([]byte(secret))
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'fixture',$3,$4,ARRAY['models.read']) ON CONFLICT(prefix) DO NOTHING`, p.TenantID, p.ID, prefix, hex.EncodeToString(sum[:]))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer aeon_"+prefix+"_"+secret)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.Bytes()
}

func decode[T any](t *testing.T, p *tenant.Principal, method, path, body string, want int) T {
	t.Helper()
	status, raw := call(t, p, method, path, body)
	if status != want {
		t.Fatalf("%s %s status %d, want %d: %s", method, path, status, want, raw)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json: %v body %s", err, raw)
	}
	return out
}

func eventCount(t *testing.T, p tenant.Principal, eventType string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type = $1`, eventType).Scan(&n)
	})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return n
}

func profileBySlug(profiles []Profile, slug string) Profile {
	for _, profile := range profiles {
		if profile.Slug == slug {
			return profile
		}
	}
	return Profile{}
}

func TestRegistrySeedsResolvesAndIsolates(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	member := addPrincipal(t, admin.TenantID, "person", "Mo", []string{"member"})
	agent := addPrincipal(t, admin.TenantID, "agent", "runner", []string{"admin"})
	other := makePrincipal(t, "beta", "person", "Bea", []string{"admin"})

	status, body := call(t, nil, http.MethodGet, "/api/models", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", status)
	}
	status, body = call(t, &member, http.MethodPost, "/api/models", `{"slug":"custom-local","version":"1","harness":"grok","family":"xai","model":"grok-4","effort":"high","tier":"frontier"}`)
	if status != http.StatusForbidden {
		t.Fatalf("member create %d %s", status, body)
	}

	profiles := decode[[]Profile](t, &admin, http.MethodGet, "/api/models", "", http.StatusOK)
	if len(profiles) != len(catalogProfiles()) {
		t.Fatalf("seeded %d profiles", len(profiles))
	}
	call(t, &agent, http.MethodGet, "/api/models", "")
	if eventCount(t, admin, evSeeded) != 1 || eventCount(t, other, evSeeded) != 0 {
		t.Fatal("seed event leaked or repeated")
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE model_profiles SET effort = 'low'`); err == nil {
		t.Fatal("immutable profile was updated")
	}

	created := decode[Profile](t, &admin, http.MethodPost, "/api/models", `{"slug":"custom-local","version":"1","harness":"grok","family":"xai","model":"grok-4","effort":"high","tier":"frontier"}`, http.StatusCreated)
	if !created.Enabled || created.Slug != "custom-local" || eventCount(t, admin, evProfile) != 1 {
		t.Fatalf("created %+v", created)
	}
	status, body = call(t, &admin, http.MethodPost, "/api/models", `{"slug":"custom-local","version":"1","harness":"grok","family":"xai","model":"grok-4","effort":"high","tier":"frontier"}`)
	if status != http.StatusConflict {
		t.Fatalf("duplicate %d %s", status, body)
	}
	status, _ = call(t, &admin, http.MethodPost, "/api/models", `{"slug":"custom-local","version":"2","harness":"grok","family":"xai","model":"grok-4;rm","effort":"high","tier":"frontier"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unsafe model %d", status)
	}

	luna := profileBySlug(profiles, "codex-luna-medium")
	minimalAccount(t, admin, luna)
	minimalAccount(t, admin, profileBySlug(profiles, "codex-astra-xhigh"))
	scout := decode[Resolution](t, &agent, http.MethodGet, "/api/models/resolve?role=scout", "", http.StatusOK)
	if scout.Source != "aeon" || scout.OwnerRequired || scout.Profile == nil || scout.Profile.ID != luna.ID ||
		scout.CommandTemplate != "codex exec -m gpt-6-luna -c model_reasoning_effort=medium '{prompt}'" {
		t.Fatalf("scout %+v", scout)
	}
	status, body = call(t, &agent, http.MethodGet, "/api/models/resolve?role=review-gate", "")
	if status != http.StatusBadRequest {
		t.Fatalf("review without author %d %s", status, body)
	}
	status, _ = call(t, &agent, http.MethodGet, "/api/models/resolve?role=scout&harness=cursor", "")
	if status != http.StatusBadRequest {
		t.Fatal("cursor scout should be unsupported")
	}
	gate := decode[Resolution](t, &agent, http.MethodGet, "/api/models/resolve?role=review-gate&author_family=anthropic", "", http.StatusOK)
	astra := profileBySlug(profiles, "codex-astra-xhigh")
	if gate.Profile == nil || gate.Profile.ID != astra.ID || gate.OwnerRequired ||
		gate.CommandTemplate != "codex exec -m gpt-6-astra -c model_reasoning_effort=xhigh --sandbox read-only '{prompt}'" ||
		len(gate.Ladder) != 7 || gate.Ladder[1].SkipReasons[0] != "author family" {
		t.Fatalf("gate %+v", gate)
	}
	otherProfiles := decode[[]Profile](t, &other, http.MethodGet, "/api/models", "", http.StatusOK)
	minimalAccount(t, other, profileBySlug(otherProfiles, "claude-fable-xhigh"))
	otherGate := decode[Resolution](t, &other, http.MethodGet, "/api/models/resolve?role=review-gate&author_family=openai&harness=claude", "", http.StatusOK)
	if otherGate.Profile == nil || otherGate.Profile.Slug != "claude-fable-xhigh" || !strings.Contains(otherGate.CommandTemplate, "--permission-mode plan") {
		t.Fatalf("other tenant gate %+v", otherGate)
	}
	if otherGate.Profile.ID == gate.Profile.ID {
		t.Fatal("profile ids leaked across tenants")
	}
}

func TestAuthorFamilyAliasesResolveAndReview(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "family-aliases", "person", "Ada", []string{"admin"})
	profiles := decode[[]Profile](t, &admin, http.MethodGet, "/api/models", "", http.StatusOK)
	now := time.Now()
	for _, tc := range []struct{ input, family string }{
		{"claude", "anthropic"}, {"codex", "openai"}, {"grok", "xai"},
		{"anthropic", "anthropic"}, {"openai", "openai"}, {"xai", "xai"}, {"cursor", "cursor"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			path := "/api/models/resolve?role=review-gate&author_family="
			got := decode[Resolution](t, &admin, http.MethodGet, path+tc.input, "", http.StatusOK)
			want := decode[Resolution](t, &admin, http.MethodGet, path+tc.family, "", http.StatusOK)
			if got.AuthorFamily != tc.family || !reflect.DeepEqual(got, want) || got.Profile == nil || got.Profile.Family == tc.family {
				t.Fatalf("alias resolution %+v differs from family resolution %+v", got, want)
			}
			err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
				got, err := ResolveReview(t.Context(), tx, admin, tc.input, "", now)
				if err != nil {
					return err
				}
				want, err := ResolveReview(t.Context(), tx, admin, tc.family, "", now)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(got, want) || len(got.Ladder) == 0 {
					t.Fatalf("review alias route %+v differs from family route %+v", got, want)
				}
				for _, candidate := range got.Ladder {
					for _, profile := range profiles {
						if profile.ID == candidate.ProfileID && profile.Family == tc.family && !strings.Contains(strings.Join(candidate.SkipReasons, ";"), "author family") {
							t.Errorf("review candidate from author's family was not excluded: %+v", candidate)
						}
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, input := range []string{"pi", "opencode", "unknown"} {
		status, body := call(t, &admin, http.MethodGet, "/api/models/resolve?role=review-gate&author_family="+input, "")
		if status != http.StatusBadRequest || !strings.Contains(string(body), "openai, anthropic, xai, cursor, google or local") {
			t.Fatalf("invalid family %q: status %d, body %s", input, status, body)
		}
	}
}

func TestRouteSuppressionAccountSkipsAndExpiry(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	agent := addPrincipal(t, admin.TenantID, "agent", "runner", []string{"admin"})
	profiles := decode[[]Profile](t, &admin, http.MethodGet, "/api/models", "", http.StatusOK)
	luna := profileBySlug(profiles, "codex-luna-medium")
	haiku := profileBySlug(profiles, "claude-haiku-medium")
	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`[{"role":"scout","priority":1,"profile_id":%q,"state":"conserved","reason":"hold","valid_until":%q},{"role":"scout","priority":2,"profile_id":%q,"state":"available","reason":"","valid_until":null}]`, luna.ID, until, haiku.ID)
	routes := decode[[]Route](t, &admin, http.MethodPut, "/api/models/routes", body, http.StatusOK)
	if len(routes) != 2 || routes[0].State != "conserved" {
		t.Fatalf("routes %+v", routes)
	}
	if eventCount(t, admin, evRoutes) != 1 {
		t.Fatal("missing route event")
	}
	scout := decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=scout", "", http.StatusOK)
	if scout.Profile == nil || scout.Profile.ID != haiku.ID || !strings.Contains(scout.Ladder[0].SkipReasons[0], "conserved until") {
		t.Fatalf("suppressed %+v", scout)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET valid_until = now() - interval '1 minute' WHERE state = 'conserved'`)
		return err
	})
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	scout = decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=scout", "", http.StatusOK)
	if scout.Profile == nil || scout.Profile.ID != luna.ID || len(scout.Ladder[0].SkipReasons) != 0 {
		t.Fatalf("expired suppression still skipped: %+v", scout)
	}

	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO agent_accounts
				(tenant_id, account_key, harness, daemon_id, registered_by_principal_id, label, state,
				 last_probe_at, last_probe_ok, last_daemon_generation)
			VALUES ($1::uuid, 'local-codex', 'codex', 'daemon-a', $2::uuid, 'Codex', 'available',
			        now() - interval '10 minutes', true, 'gen-1')`, admin.TenantID, agent.ID)
		return err
	})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	scout = decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=scout", "", http.StatusOK)
	if scout.Profile == nil || scout.Profile.ID != haiku.ID || scout.Ladder[0].SkipReasons[0] != "account availability" {
		t.Fatalf("stale probe %+v", scout)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			UPDATE agent_accounts SET last_probe_at = now() WHERE account_key = 'local-codex'`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `
			INSERT INTO account_allowance_windows
				(tenant_id, account_id, starts_at, ends_at, unit, allowance, used, pace_model, burst_ratio)
			SELECT $1::uuid, id, now() - interval '1 hour', now() + interval '1 hour', 'requests', 10, 10, 'unrestricted', 0
			FROM agent_accounts WHERE account_key = 'local-codex'`, admin.TenantID)
		return err
	})
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	scout = decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=scout", "", http.StatusOK)
	if scout.Profile == nil || scout.Profile.ID != haiku.ID || scout.Ladder[0].SkipReasons[0] != "allowance" {
		t.Fatalf("full allowance %+v", scout)
	}
	other := makePrincipal(t, "beta", "person", "Bea", []string{"admin"})
	status, raw := call(t, &other, http.MethodPut, "/api/models/routes", fmt.Sprintf(`[{"role":"scout","priority":1,"profile_id":%q,"state":"available"}]`, luna.ID))
	if status != http.StatusBadRequest {
		t.Fatalf("cross-tenant route %d %s", status, raw)
	}
}

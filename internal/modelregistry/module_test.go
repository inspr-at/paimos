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
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/capacity"
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "node", "../../api/generate.mjs", "--write").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare OpenAPI: %v\n%s", err, output)
		os.Exit(1)
	}
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
	module := &Module{pool: appPool, discovery: fixtureCatalogClient(t, nil)}
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
	if p != nil && want >= 200 && want < 300 && (strings.HasPrefix(path, "/api/models") || strings.HasPrefix(path, "/api/model-preferences")) && !strings.HasPrefix(path, "/api/models/catalog") && !strings.HasPrefix(path, "/api/models/routes?") {
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { return legacyCatalogFixture(t.Context(), tx, *p) }); err != nil {
			t.Fatal(err)
		}
	}

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

func profileByID(profiles []Profile, id string) Profile {
	for _, profile := range profiles {
		if profile.ID == id {
			return profile
		}
	}
	return Profile{}
}

// nativeGrokAccount enrolls a Grok account through a darwin/arm64 pairing:
// Grok reviews only through that native no-tools adapter.
func nativeGrokAccount(t *testing.T, p tenant.Principal, profile Profile) string {
	t.Helper()
	account := minimalAccount(t, p, profile)
	inRegistry(t, p, func(tx pgx.Tx) error {
		ctx := t.Context()
		var runner, key, request, computer string
		if err := tx.QueryRow(ctx, `SELECT registered_by_principal_id::text FROM agent_accounts WHERE id=$1`, account).Scan(&runner); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'Native Grok fixture',replace(gen_random_uuid()::text,'-',''),md5(random()::text)||md5(random()::text),ARRAY['models.read']) RETURNING id::text`, p.TenantID, runner).Scan(&key); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by)
 VALUES($1,gen_random_uuid(),lpad(floor(random()*1000000000)::bigint::text,9,'0'),md5(random()::text)||md5(random()::text),repeat('b',64),repeat('c',64),'{"platform":"darwin","arch":"arm64"}','fixture','redeemed',$2) RETURNING id::text`, p.TenantID, p.ID).Scan(&request); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash) VALUES($1,gen_random_uuid(),$2,$3,$4,'native-grok-'||gen_random_uuid(),repeat('c',64)) RETURNING id::text`, p.TenantID, request, runner, key).Scan(&computer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_pairing_requests SET computer_id=$2 WHERE id=$1`, request, computer); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at) VALUES($1,$2,$3,$4,$5,clock_timestamp()+interval '1 hour',clock_timestamp())`, p.TenantID, account, computer, request, profile.ID)
		return err
	})
	return account
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
	astra := profileBySlug(profiles, "codex-astra-xhigh")
	minimalAccount(t, admin, luna)
	minimalAccount(t, admin, astra)
	scout := decode[Resolution](t, &agent, http.MethodGet, "/api/models/resolve?role=scout", "", http.StatusOK)
	if scout.Source != "aeon" || scout.OwnerRequired || scout.Profile == nil || scout.Profile.ID != luna.ID ||
		scout.CommandTemplate != "codex exec -m gpt-6-luna -c model_reasoning_effort=medium '{prompt}'" {
		t.Fatalf("scout %+v", scout)
	}
	status, body = call(t, &agent, http.MethodGet, "/api/models/resolve?role=review-gate", "")
	if status != http.StatusBadRequest {
		t.Fatalf("review without author %d %s", status, body)
	}
	status, body = call(t, &agent, http.MethodGet, "/api/models/resolve?role=scout&harness=cursor", "")
	if status != http.StatusBadRequest || errorText(body) != "role and harness combination is unsupported" {
		t.Fatalf("unsupported cursor scout %d %s", status, body)
	}
	// AEON-1427 qualifies the printed isolated Codex command. Automatic review
	// takes that first cross-family route while retaining tenant/account gates.
	fable := profileBySlug(profiles, "claude-fable-xhigh")
	minimalAccount(t, admin, fable)
	gate := decode[Resolution](t, &agent, http.MethodGet, "/api/models/resolve?role=review-gate&author_family=xai", "", http.StatusOK)
	const isolated = "codex exec --ignore-user-config -s read-only -m gpt-6-astra -c model_reasoning_effort=xhigh '{prompt}'"
	if gate.Profile == nil || gate.Profile.ID != astra.ID || gate.OwnerRequired || gate.CommandTemplate != isolated || len(gate.Ladder) != 1 || !gate.Ladder[0].Selected {
		t.Fatalf("gate %+v", gate)
	}
	for _, c := range gate.Ladder {
		if family := profileByID(profiles, c.ProfileID).Family; family == "xai" || !c.Selected && len(c.SkipReasons) == 0 {
			t.Fatalf("gate considered the author's family or skipped without a reason: %+v", c)
		}
	}
	if err := ValidateReviewCommand(astra.Harness, astra.Model, astra.Effort, strings.Replace(isolated, "--ignore-user-config ", "", 1)); err == nil {
		t.Fatal("gate admitted the same pin without command isolation")
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
	// Models dispatches reviews only to enrolled accounts with a qualified
	// no-tools adapter: Claude reviews OpenAI, xAI and Cursor work, Grok
	// reviews Anthropic work (Models v2 has no account-less catalog preview).
	minimalAccount(t, admin, profileBySlug(profiles, "claude-fable-xhigh"))
	nativeGrokAccount(t, admin, profileBySlug(profiles, "grok-4-7-xhigh"))
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

// Risk: a model pause (the Models successor of a route hold) is ignored,
// outlives its expiry or reaches another tenant; account health stops skipping
// a step; or the retired ladder writer still stores or audits a hold. Build
// follows the workspace Default order, so the order fixes which step is first
// (Scout puts catalog-priced Quick-job steps ahead of it), and resolution runs
// on the Models board, which selects only a step with a qualified account.
func TestRouteSuppressionAccountSkipsAndExpiry(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	agent := addPrincipal(t, admin.TenantID, "agent", "runner", []string{"admin"})
	profiles := decode[[]Profile](t, &admin, http.MethodGet, "/api/models", "", http.StatusOK)
	luna := profileBySlug(profiles, "codex-luna-medium")
	haiku := profileBySlug(profiles, "claude-haiku-medium")
	if luna.ID == "" || haiku.ID == "" {
		t.Fatal("missing catalog fixture")
	}
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	before := modelsState(t, admin)
	body := fmt.Sprintf(`[{"role":"build","priority":1,"profile_id":%q,"state":"conserved","reason":"hold","valid_until":%q},{"role":"build","priority":2,"profile_id":%q,"state":"available","reason":"","valid_until":null}]`, luna.ID, until.Format(time.RFC3339), haiku.ID)
	if status, raw := call(t, &admin, http.MethodPut, "/api/models/routes", body); !retiredLadderRefusal(status, raw) {
		t.Fatalf("retired writer took the hold: %d %s", status, raw)
	}
	if modelsState(t, admin) != before || eventCount(t, admin, evRoutes) != 0 {
		t.Fatal("retired writer changed Models storage or audited a route change")
	}
	// Models selects only with a qualified account, so each step gets one.
	var codex string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		codex = runnableAccountFixture(t, tx, admin, agent.ID, "local-codex", "codex", luna.ID, now)
		runnableAccountFixture(t, tx, admin, agent.ID, "local-claude", "claude", haiku.ID, now)
		// Shared usage keeps the Codex account's exact wait reason visible.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=true WHERE id=$1`, codex); err != nil {
			return err
		}
		if err := dbtest.ModelsOrder(t.Context(), tx, admin.TenantID, "other", "first", "wait", luna.ID, haiku.ID); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by) VALUES($1,$2,$3,$4,'hold',$5)`, admin.TenantID, luna.Harness, luna.Model, until, admin.ID)
		return err
	})
	if err != nil {
		t.Fatalf("accounts, order and pause: %v", err)
	}
	firstReason := func(r Resolution) string {
		if len(r.Ladder) == 0 || r.Ladder[0].ProfileID != luna.ID || len(r.Ladder[0].SkipReasons) == 0 {
			return ""
		}
		return r.Ladder[0].SkipReasons[0]
	}
	build := decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=build", "", http.StatusOK)
	if build.Profile == nil || build.Profile.ID != haiku.ID || firstReason(build) != "paused until "+until.Format(time.RFC3339)+": hold" {
		t.Fatalf("paused %+v", build)
	}
	// While alpha's pause is active, another tenant's retired write is refused
	// and alpha's order and pause stay invisible to it: its Luna is not paused.
	other := makePrincipal(t, "beta", "person", "Bea", []string{"admin"})
	alpha := modelsState(t, admin)
	status, raw := call(t, &other, http.MethodPut, "/api/models/routes", fmt.Sprintf(`[{"role":"build","priority":1,"profile_id":%q,"state":"available"}]`, luna.ID))
	if !retiredLadderRefusal(status, raw) || modelsState(t, admin) != alpha {
		t.Fatalf("cross-tenant route %d %s", status, raw)
	}
	otherLuna := profileBySlug(decode[[]Profile](t, &other, http.MethodGet, "/api/models", "", http.StatusOK), "codex-luna-medium")
	if otherLuna.ID == "" || otherLuna.ID == luna.ID {
		t.Fatalf("tenant catalogs are not separate: %+v", otherLuna)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, other.TenantID, func(tx pgx.Tx) error {
		var pauses int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_pauses`).Scan(&pauses); err != nil {
			return err
		}
		step, err := preferenceStep(t.Context(), tx, otherLuna, "build")
		if err != nil {
			return err
		}
		if pauses != 0 || step.State != "available" || step.Reason != "" || step.ValidUntil != nil {
			t.Fatalf("alpha's pause reached beta: %d pauses, %+v", pauses, step.Route)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_pauses SET until_at = now() - interval '1 minute' WHERE harness=$1 AND model=$2`, luna.Harness, luna.Model)
		return err
	})
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	build = decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=build", "", http.StatusOK)
	if build.Profile == nil || build.Profile.ID != luna.ID || len(build.Ladder) == 0 || len(build.Ladder[0].SkipReasons) != 0 {
		t.Fatalf("expired pause still skipped: %+v", build)
	}

	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at = now() - interval '10 minutes' WHERE id = $1`, codex)
		return err
	})
	if err != nil {
		t.Fatalf("stale account: %v", err)
	}
	build = decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=build", "", http.StatusOK)
	if build.Profile == nil || build.Profile.ID != haiku.ID || firstReason(build) != "Codex account has no recent successful sign-in check" {
		t.Fatalf("stale probe %+v", build)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			UPDATE agent_accounts SET last_probe_at = now() WHERE id = $1`, codex)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `
			INSERT INTO account_allowance_windows
				(tenant_id, account_id, starts_at, ends_at, unit, allowance, used, pace_model, burst_ratio)
			VALUES ($1::uuid, $2::uuid, now() - interval '1 hour', now() + interval '1 hour', 'requests', 10, 10, 'unrestricted', 0)`, admin.TenantID, codex)
		return err
	})
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	build = decode[Resolution](t, &admin, http.MethodGet, "/api/models/resolve?role=build", "", http.StatusOK)
	if build.Profile == nil || build.Profile.ID != haiku.ID || !strings.HasPrefix(firstReason(build), "Codex account is at its allowance or floor until ") {
		t.Fatalf("full allowance %+v", build)
	}
}

func astra(profiles []Profile) Profile { return profileBySlug(profiles, "codex-astra-xhigh") }

func reviewAccount(t *testing.T, p tenant.Principal, harness string) string {
	t.Helper()
	var id string
	inRegistry(t, p, func(tx pgx.Tx) error {
		var runner string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Review runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin'`, p.TenantID, runner); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,$2,$2,'review-runner',$3,'Review account',now(),true,'generation',$4) RETURNING id::text`, p.TenantID, harness, runner, p.ID).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '1 day','requests',1000,'unrestricted')`, p.TenantID, id); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		for i := range schedule.Week {
			schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
		}
		schedule.Override, schedule.Reserve = "sprint", capacity.ReserveOff
		raw, _ := json.Marshal(schedule)
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, id, raw); err != nil {
			return err
		}
		if harness != "grok" {
			return nil
		}
		// Native Grok reviews only through a macOS arm64 pairing enrollment.
		var key, request, computer, profile string
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE harness='grok' AND enabled ORDER BY slug LIMIT 1`).Scan(&profile); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id) VALUES($1,$2,'Review fixture',gen_random_uuid()::text,'fixture-not-a-credential',$3) RETURNING id::text`, p.TenantID, runner, p.ID).Scan(&key); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by)
 VALUES($1,gen_random_uuid(),'887887887',repeat('a',64),repeat('b',64),repeat('c',64),'{"platform":"darwin","arch":"arm64"}','fixture','redeemed',$2) RETURNING id::text`, p.TenantID, p.ID).Scan(&request); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash) VALUES($1,gen_random_uuid(),$2,$3,$4,'review-runner',repeat('c',64)) RETURNING id::text`, p.TenantID, request, runner, key).Scan(&computer); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at) VALUES($1,$2,$3,$4,$5,now()+interval '1 hour',now())`, p.TenantID, id, computer, request, profile)
		return err
	})
	return id
}

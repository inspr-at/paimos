// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func inRegistry(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), p), appPool, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}

func grantModelReporter(t *testing.T, p, agent tenant.Principal) {
	t.Helper()
	inRegistry(t, p, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'model_reporter','Model reporter') RETURNING id::text`, p.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1::uuid,$2::uuid,unnest(ARRAY['nodes.read','models.read','models.report','models.refresh','models.manage'])`, p.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace') ON CONFLICT (tenant_id,principal_id) WHERE scope_type='workspace' DO UPDATE SET role_id=EXCLUDED.role_id`, p.TenantID, agent.ID, role)
		return err
	})
}

func registryRoutes(t *testing.T, p tenant.Principal) []Route {
	t.Helper()
	var out []Route
	inRegistry(t, p, func(tx pgx.Tx) error {
		var err error
		out, err = listRoutes(t.Context(), tx)
		return err
	})
	return out
}

func TestSecurityRoutesReplaceExpireAndRemainTenantScoped(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "security-routes", "person", "Owner", []string{"admin"})
	other := makePrincipal(t, "other-security-routes", "person", "Other", []string{"admin"})
	decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	decode[[]Profile](t, &other, "GET", "/api/models", "", 200)
	otherBefore := registryRoutes(t, other)
	routes := registryRoutes(t, p)
	var firstSecurity string
	until := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	var beforeRevision string
	inRegistry(t, p, func(tx pgx.Tx) error {
		var err error
		beforeRevision, err = Revision(t.Context(), tx)
		return err
	})
	for i := range routes {
		if routes[i].Role == "review-gate-security" && routes[i].Priority == 1 {
			firstSecurity = routes[i].ProfileID
			routes[i].State, routes[i].Reason, routes[i].ValidUntil = "conserved", "Owner security override", &until
		}
	}
	if firstSecurity == "" {
		t.Fatal("security ladder missing from current route reads")
	}
	raw, err := json.Marshal(routes)
	if err != nil {
		t.Fatal(err)
	}
	decode[[]Route](t, &p, "PUT", "/api/models/routes", string(raw), 200)
	if !routesEqual(registryRoutes(t, p), routes) {
		t.Fatal("route replacement lost the security override")
	}
	gate := decode[Resolution](t, &p, "GET", "/api/models/resolve?role=review-gate-security&author_family=openai", "", 200)
	if gate.Profile == nil || gate.Profile.ID == firstSecurity || gate.Profile.Harness != "cursor" {
		t.Fatalf("security override did not select the fallback: %+v", gate)
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		revision, err := Revision(t.Context(), tx)
		if err != nil {
			return err
		}
		if revision == beforeRevision || revision == "" {
			t.Fatal("revision ignored security route changes")
		}
		expired, err := resolveRole(t.Context(), tx, resolveQuery{Role: "review-gate-security", AuthorFamily: "openai"}, until.Add(time.Minute))
		if err != nil {
			return err
		}
		if expired.Profile == nil || expired.Profile.ID != firstSecurity {
			t.Fatal("security override did not expire")
		}
		return nil
	})
	withoutSecurity := []Route{}
	for _, route := range routes {
		if route.Role != "review-gate-security" {
			withoutSecurity = append(withoutSecurity, route)
		}
	}
	raw, err = json.Marshal(withoutSecurity)
	if err != nil {
		t.Fatal(err)
	}
	decode[[]Route](t, &p, "PUT", "/api/models/routes", string(raw), 200)
	if !reflect.DeepEqual(registryRoutes(t, p), withoutSecurity) {
		t.Fatal("replacement retained removed security routes or altered other roles")
	}
	gate = decode[Resolution](t, &p, "GET", "/api/models/resolve?role=review-gate-security&author_family=openai", "", 200)
	if gate.Profile != nil || !gate.OwnerRequired || len(gate.Ladder) != 0 {
		t.Fatalf("removed security ladder still resolves: %+v", gate)
	}
	if !reflect.DeepEqual(registryRoutes(t, other), otherBefore) {
		t.Fatal("route replacement changed another tenant")
	}
}

// Both route tables must retain independent retirement and health fences.
func TestRefreshLaddersPreserveRetirementAndSuppression(t *testing.T) {
	for _, role := range []string{"build", "review-gate-security"} {
		t.Run(role, func(t *testing.T) {
			reset(t)
			p := makePrincipal(t, "retired-refresh", "person", "Owner", []string{"admin"})
			decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
			inRegistry(t, p, func(tx pgx.Tx) error {
				now, err := dbNow(t.Context(), tx)
				if err != nil {
					return err
				}
				q := resolveQuery{Role: role, AuthorFamily: "openai"}
				before, err := resolveRole(t.Context(), tx, q, now)
				if err != nil {
					return err
				}
				if before.Profile == nil {
					t.Fatal("fixture must select the first route")
				}
				profile := before.Profile
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_profile_retirements(tenant_id,profile_id,reason,retired_by) VALUES($1,$2,'merge regression',$3)`, p.TenantID, profile.ID, p.ID); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO model_observations(tenant_id,harness,model,effort,failures,last_failing_at,suppressed_until,source) VALUES($1,$2,$3,$4,2,$5,$6,'agent')`, p.TenantID, profile.Harness, profile.Model, profile.Effort, now, now.Add(time.Hour)); err != nil {
					return err
				}
				for _, at := range []time.Time{now, now.Add(2 * time.Hour)} {
					out, err := resolveRole(t.Context(), tx, q, at)
					if err != nil {
						return err
					}
					if out.Profile == nil || out.Profile.ID == profile.ID {
						t.Fatalf("retired route selected or fallback lost: %+v", out)
					}
					first := out.Ladder[0]
					reasons := strings.Join(first.SkipReasons, "; ")
					if first.ProfileID != profile.ID || first.Selected || !strings.Contains(reasons, "retired") || strings.Contains(reasons, "model invalid until") != at.Before(now.Add(time.Hour)) {
						t.Fatalf("retirement and timed health fence diverged: %+v", first)
					}
				}
				return nil
			})
		})
	}
}

func TestV2UpgradePreservesPinsCustomRoutesAndOverrides(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		t.Run(fmt.Sprintf("additional-harnesses-%t", expanded), func(t *testing.T) {
			reset(t)
			p := makePrincipal(t, "upgrade", "person", "Owner", []string{"admin"})
			inRegistry(t, p, func(tx pgx.Tx) error {
				seeds := catalogProfiles()
				ids := map[string]string{}
				// Reproduce exact immutable v2 profiles and its old terra profile.
				for _, s := range seeds {
					if strings.HasPrefix(s.Slug, "codex-6-1-") || s.Harness == "grok" || (!expanded && (s.Harness == "gemini" || s.Harness == "opencode")) {
						continue
					}
					s.Version = "2"
					row, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: s.Slug, Version: s.Version, Harness: s.Harness, Family: s.Family, Model: s.Model, Effort: s.Effort, Tier: s.Tier})
					if err != nil {
						return err
					}
					ids[s.Slug] = row.ID
				}
				terra, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: "codex-terra-high", Version: "2", Harness: "codex", Family: "openai", Model: "gpt-6-terra", Effort: "high", Tier: "standard"})
				if err != nil {
					return err
				}
				ids[terra.Slug] = terra.ID
				until := time.Now().Add(time.Hour)
				for role, slugs := range v2Ladders {
					if expanded {
						slugs = append([]string(nil), slugs...)
						for _, route := range defaultRoutes(seeds) {
							if route.Role == role && (strings.HasPrefix(route.Slug, "gemini-") || strings.HasPrefix(route.Slug, "opencode-")) {
								slugs = append(slugs, route.Slug)
							}
						}
					}
					for i, slug := range slugs {
						r := Route{Role: role, Priority: i + 1, ProfileID: ids[slug], State: "available"}
						if role == "review-gate" && i == 1 {
							r.State = "conserved"
							r.Reason = "owner preference"
							r.ValidUntil = &until
						}
						if err := insertRoute(t.Context(), tx, p.TenantID, r); err != nil {
							return err
						}
					}
				}
				// Custom build-hard order must survive. Both versions match seed values,
				// but it is structurally different from the v2 default.
				_, err = tx.Exec(t.Context(), `UPDATE model_role_routes SET priority=priority+10 WHERE role='build-hard'`)
				return err
			})
			var before []Route
			inRegistry(t, p, func(tx pgx.Tx) error { var err error; before, err = listRoutes(t.Context(), tx); return err })
			NewWithVault(appPool, nil).sweep(t.Context())
			profiles := decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
			if profileBySlug(profiles, "codex-6-1-sol-high").ID == "" || profileBySlug(profiles, "grok-4-7-xhigh").ID == "" {
				t.Fatal("v3 profiles missing")
			}
			inRegistry(t, p, func(tx pgx.Tx) error {
				steps, err := loadLadder(t.Context(), tx, "build")
				if err != nil {
					return err
				}
				if steps[0].Profile.Model != "gpt-6.1-sol" {
					t.Fatalf("build still selects %s", steps[0].Profile.Model)
				}
				profile := steps[0].Profile
				if profile.FullName() != "Codex Sol 6.1" || profile.ShortName != "Sol" || profile.Provider != "openai" || profile.EffortLevel == nil || *profile.EffortLevel != 3 {
					t.Fatalf("upgraded ladder lost display or effort metadata: %+v", profile)
				}
				after, err := listRoutes(t.Context(), tx)
				if err != nil {
					return err
				}
				filter := func(in []Route, role string) []Route {
					out := []Route{}
					for _, r := range in {
						if r.Role == role {
							out = append(out, r)
						}
					}
					return out
				}
				oldHard, newHard := filter(before, "build-hard"), filter(after, "build-hard")
				additional := 2
				if expanded {
					additional = 0
				}
				if len(newHard) != len(oldHard)+additional || !reflect.DeepEqual(oldHard, newHard[:len(oldHard)]) {
					t.Fatal("custom routes changed instead of appending additional harnesses")
				}
				oldGate, newGate := filter(before, "review-gate"), filter(after, "review-gate")
				if !reflect.DeepEqual(oldGate[1], newGate[1]) {
					t.Fatal("active override changed")
				}
				return nil
			})
			decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
			if eventCount(t, p, "model.catalog_upgraded") != 1 {
				t.Fatal("upgrade not idempotent")
			}
			gate := decode[Resolution](t, &p, "GET", "/api/models/resolve?role=review-gate-security&author_family=xai", "", 200)
			if gate.Profile == nil || gate.Profile.Family != "openai" || !strings.Contains(gate.CommandTemplate, "--sandbox read-only") {
				t.Fatalf("security route: %+v", gate)
			}
		})
	}
}

func TestReportsAreScopedIdempotentAndPreserveOverrides(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "reports", "person", "Owner", []string{"admin"})
	agent := addPrincipal(t, p.TenantID, "agent", "Worker", nil)
	other := makePrincipal(t, "other-reports", "person", "Other", []string{"admin"})
	o := Observation{ReportID: EvidenceID("failure-one"), Harness: "codex", Model: "gpt-6.1-sol", Effort: "high", Status: "invalid"}
	send := func(p tenant.Principal, o Observation, want int) ReportResult {
		raw, _ := json.Marshal([]Observation{o})
		return decode[ReportResult](t, &p, "POST", "/api/models/reports", string(raw), want)
	}
	raw, _ := json.Marshal([]Observation{o})
	status, _ := call(t, &agent, "POST", "/api/models/reports", string(raw))
	if status != 403 {
		t.Fatalf("ungranted agent: %d", status)
	}
	grantModelReporter(t, p, agent)
	agent.Scopes = []string{"models.read", "models.report", "models.refresh", "models.manage"}
	account := enrollEvidenceHarness(t, p, agent, "codex")
	seedEvidenceSession(t, p, agent, "codex", "gpt-6.1-sol", "high")
	profiles := decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	sol := profileBySlug(profiles, "codex-6-1-sol-high")
	until := time.Now().Add(time.Hour)
	inRegistry(t, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=now(),last_probe_ok=true,last_daemon_generation='fixture',capacity_owner=$2 WHERE id=$1`, account, p.ID); err != nil {
			return err
		}
		// Model-health expiry must not depend on the real weekday or hour.
		schedule := capacity.DefaultSchedule("UTC")
		for i := range schedule.Week {
			schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
		}
		schedule.Reserve = capacity.ReserveOff
		raw, err := json.Marshal(schedule)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,now()-interval '1 hour',now()+interval '2 days','requests',1000,'unrestricted')`, p.TenantID, account); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE model_role_routes SET state='conserved',reason='owner override',valid_until=$2 WHERE profile_id=$1`, sol.ID, until)
		return err
	})
	if got := send(agent, o, 200); got.Recorded != 1 {
		t.Fatal(got)
	}
	if got := send(agent, o, 200); got.Recorded != 0 {
		t.Fatal("replayed failure counted")
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_observations SET last_failing_at=now()-interval '6 minutes'`)
		return err
	})
	o.ReportID = EvidenceID("failure-two")
	send(agent, o, 200)
	inRegistry(t, p, func(tx pgx.Tx) error {
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		out, err := resolveRole(t.Context(), tx, resolveQuery{Role: "build"}, now)
		if err != nil {
			return err
		}
		if len(out.Ladder[0].SkipReasons) < 2 {
			t.Fatalf("lost independent override/suppression: %+v", out.Ladder)
		}
		// Account readiness is independent of model health. Keep the account's
		// probe current at the simulated horizon to test only suppression expiry.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=$2 WHERE id=$1`, account, now.Add(25*time.Hour)); err != nil {
			return err
		}
		health, err := agentaccounts.HarnessHealthAt(t.Context(), tx, now.Add(25*time.Hour))
		if err != nil {
			return err
		}
		if health["codex"].Dispatchable != 1 {
			t.Fatalf("expiry fixture account unavailable: %+v", health["codex"])
		}
		out, err = resolveRole(t.Context(), tx, resolveQuery{Role: "build"}, now.Add(25*time.Hour))
		if err != nil {
			return err
		}
		if out.Profile == nil || out.Profile.ID != sol.ID {
			t.Fatalf("suppression/override did not expire: %+v", out.Ladder)
		}
		return nil
	})
	o.ReportID = EvidenceID("working")
	o.Status = "working"
	send(agent, o, 200)
	inRegistry(t, p, func(tx pgx.Tx) error {
		var count int
		var suppressed *time.Time
		if err := tx.QueryRow(t.Context(), `SELECT failures,suppressed_until FROM model_observations WHERE model=$1`, o.Model).Scan(&count, &suppressed); err != nil {
			return err
		}
		if count != 0 || suppressed != nil {
			t.Fatal("working did not reset failures")
		}
		return nil
	})
	changed := o
	changed.Status = "invalid"
	raw, _ = json.Marshal([]Observation{changed})
	status, _ = call(t, &agent, "POST", "/api/models/reports", string(raw))
	if status != 409 {
		t.Fatalf("conflicting replay %d", status)
	}
	inRegistry(t, other, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_observations`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("report leaked tenant")
		}
		return nil
	})
	enrollEvidenceHarness(t, p, agent, "grok")
	o = Observation{ReportID: EvidenceID("new-model"), Harness: "grok", Model: "grok-next", Effort: "xhigh", Status: "advertised"}
	if got := send(agent, o, 200); got.Added != 1 {
		t.Fatal(got)
	}
	if got := send(agent, o, 200); got.Added != 0 {
		t.Fatal("added duplicate profile")
	}
}

func TestDefaultRefreshNoNetworkAndOneAuditPerRun(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "no-network", "person", "Owner", []string{"admin"})
	called := false
	m := NewWithVault(appPool, []byte(strings.Repeat("x", 32)))
	m.discovery = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		t.Error("unexpected outbound call")
		return nil, io.EOF
	})}
	if _, err := m.runRefresh(tenant.WithPrincipal(t.Context(), p), p, false); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("default called vendor")
	}
	if eventCount(t, p, "model.catalog_refreshed") != 1 {
		t.Fatal("refresh audit missing")
	}
	if _, err := m.runRefresh(tenant.WithPrincipal(t.Context(), p), p, true); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, p, "model.catalog_refreshed") != 1 {
		t.Fatal("scheduled interval ignored")
	}
	m.sweep(t.Context())
	if eventCount(t, p, "model.catalog_refreshed") != 1 {
		t.Fatal("startup sweep ignored interval")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVendorListBoundsPaginationRedirectionAndSanitizedErrors(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.anthropic.com" || r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatal("vendor binding")
		}
		body := `{"data":[{"id":"claude-new"}],"has_more":true,"last_id":"claude-new"}`
		if calls == 2 {
			if r.URL.Query().Get("after_id") != "claude-new" {
				t.Fatal("pagination")
			}
			body = `{"data":[{"id":"claude-other"},{"id":"claude-new"}],"has_more":false}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	ids, err := listVendorModels(context.Background(), client, "anthropic", "fixture-key")
	if err != nil || !reflect.DeepEqual(ids, []string{"claude-new", "claude-other"}) {
		t.Fatalf("%v %v", ids, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://example.invalid", 302) }))
	defer server.Close()
	dc := discoveryClient()
	dc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("fixture-key")), Header: http.Header{"Location": []string{server.URL}}}, nil
	})
	_, err = listVendorModels(t.Context(), dc, "openai", "fixture-key")
	if err == nil || strings.Contains(err.Error(), "fixture-key") {
		t.Fatal("unsafe vendor error")
	}
	bad := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"unsafe;command"}]}`))}, nil
	})}
	if _, err := listVendorModels(t.Context(), bad, "xai", "fixture-key"); err == nil {
		t.Fatal("unsafe identifier")
	}
}

func TestDiscoveryVaultIsAccountBoundAndOutageKeepsPolicy(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "vault-refresh", "person", "Owner", []string{"admin"})
	worker := addPrincipal(t, p.TenantID, "agent", "Worker", []string{"admin"})
	other := makePrincipal(t, "vault-other", "person", "Other", []string{"admin"})
	profiles := decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	before := registryRoutes(t, p)
	var account string
	inRegistry(t, p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,'fixture','codex','fixture',$2,'Fixture') RETURNING id::text`, p.TenantID, worker.ID).Scan(&account)
	})
	m := NewWithVault(appPool, []byte(strings.Repeat("x", 32)))
	mux := http.NewServeMux()
	m.Mount(mux)
	send := func(actor tenant.Principal, method, path, body string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), actor))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status %d, want %d", method, path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	key := "synthetic-model-discovery-credential"
	body, _ := json.Marshal(map[string]string{"vendor": "openai", "api_key": key})
	credentialPath := "/api/models/refresh/credentials/" + account
	send(other, "PUT", credentialPath, string(body), 404)
	send(worker, "PUT", credentialPath, string(body), 403)
	response := send(p, "PUT", credentialPath, string(body), 200)
	if strings.Contains(string(response), key) {
		t.Fatal("credential returned")
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		var cipher []byte
		if err := tx.QueryRow(t.Context(), `SELECT ciphertext FROM model_discovery_credentials WHERE account_id=$1`, account).Scan(&cipher); err != nil {
			return err
		}
		if strings.Contains(string(cipher), key) {
			t.Fatal("plaintext credential stored")
		}
		plain, err := linkvault.Decrypt(m.vaultKey, p.TenantID, "models/"+account+"/openai", cipher)
		if err != nil || plain != key {
			t.Fatal("vault round trip")
		}
		if _, err := linkvault.Decrypt(m.vaultKey, other.TenantID, "models/"+account+"/openai", cipher); err == nil {
			t.Fatal("tenant binding missing")
		}
		if _, err := linkvault.Decrypt(m.vaultKey, p.TenantID, "models/other/openai", cipher); err == nil {
			t.Fatal("account binding missing")
		}
		return nil
	})
	cfg := `{"agent_reports_enabled":true,"auto_add_profiles":true,"api_enabled":true,"interval_minutes":60}`
	send(worker, "PUT", "/api/models/refresh/settings", cfg, 403)
	send(p, "PUT", "/api/models/refresh/settings", cfg, 200)
	send(p, "PUT", "/api/models/refresh/settings", cfg, 200)
	if eventCount(t, p, "model.refresh_settings_changed") != 1 {
		t.Fatal("settings replay wrote an event")
	}
	outage := false
	m.discovery = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != vendorURLs["openai"] || r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatal("unexpected discovery destination")
		}
		status, raw := 200, `{"data":[{"id":"gpt-6.1-sol"},{"id":"gpt-future"}]}`
		if outage {
			status, raw = 503, key
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(raw)), Header: http.Header{}}, nil
	})}
	var result RefreshResult
	if err := json.Unmarshal(send(p, "POST", "/api/models/refresh", "{}", 200), &result); err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || len(result.Sources) != 1 || result.Sources[0].State != "fresh" || result.LadderChanged {
		t.Fatalf("refresh result %+v", result)
	}
	current := decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	if len(current) != len(profiles)+1 {
		t.Fatal("advertised API profiles missing")
	}
	send(p, "POST", "/api/models/refresh", "{}", 429)
	outage = true
	inRegistry(t, p, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET last_run_at=now()-interval '61 minutes'`)
		return err
	})
	if err := json.Unmarshal(send(p, "POST", "/api/models/refresh", "{}", 200), &result); err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Sources[0].State != "stale" {
		t.Fatal("outage did not retain last good catalog")
	}
	after := registryRoutes(t, p)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(current, decode[[]Profile](t, &p, "GET", "/api/models", "", 200)) {
		t.Fatal("discovery changed immutable pins or ladders")
	}
	status := send(p, "GET", "/api/models/refresh", "", 200)
	if strings.Contains(string(status), key) {
		t.Fatal("status exposed credential")
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		var leaked bool
		if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE after::text LIKE '%'||$1||'%')`, key).Scan(&leaked); err != nil {
			return err
		}
		if leaked {
			t.Fatal("audit exposed credential")
		}
		return nil
	})
	if eventCount(t, p, "model.catalog_refreshed") != 2 {
		t.Fatal("refresh audit not once per run")
	}
}

func TestAutoAcceptOffRequiresPersonAndDoesNotWriteRoutes(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "proposal-policy", "person", "Owner", []string{"admin"})
	worker := addPrincipal(t, p.TenantID, "agent", "Worker", []string{"admin"})
	grantModelReporter(t, p, worker)
	worker.Scopes = []string{"models.read", "models.report", "models.refresh", "models.manage"}
	decode[[]Profile](t, &p, "GET", "/api/models", "", 200)
	before := registryRoutes(t, p)
	cfg := `{"agent_reports_enabled":true,"auto_add_profiles":false,"api_enabled":false,"interval_minutes":1440}`
	decode[RefreshSettings](t, &p, "PUT", "/api/models/refresh/settings", cfg, 200)
	decode[RefreshResult](t, &worker, "POST", "/api/models/refresh", "{}", 200)
	enrollEvidenceHarness(t, p, worker, "grok")
	o := Observation{ReportID: EvidenceID("pending-grok"), Harness: "grok", Model: "grok-next", Effort: "xhigh", Status: "advertised"}
	raw, _ := json.Marshal([]Observation{o})
	result := decode[ReportResult](t, &worker, "POST", "/api/models/reports", string(raw), 200)
	if result.Added != 0 || result.Proposed != 1 {
		t.Fatal("auto-accept off ignored")
	}
	accept := `{"harness":"grok","model":"grok-next","effort":"xhigh"}`
	status, _ := call(t, &worker, "POST", "/api/models/proposals/accept", accept)
	if status != 403 {
		t.Fatal("agent accepted a proposal")
	}
	first := decode[Profile](t, &p, "POST", "/api/models/proposals/accept", accept, 200)
	second := decode[Profile](t, &p, "POST", "/api/models/proposals/accept", accept, 200)
	if first.ID != second.ID || eventCount(t, p, "model.proposal_accepted") != 1 {
		t.Fatal("acceptance not idempotent")
	}
	if !reflect.DeepEqual(before, registryRoutes(t, p)) {
		t.Fatal("proposal changed role order")
	}
	status, _ = call(t, &p, "POST", "/api/models/reports", "null")
	if status != 400 {
		t.Fatal("null report array accepted")
	}
	cfg = `{"agent_reports_enabled":false,"auto_add_profiles":false,"api_enabled":false,"interval_minutes":1440}`
	decode[RefreshSettings](t, &p, "PUT", "/api/models/refresh/settings", cfg, 200)
	o.ReportID = EvidenceID("disabled-report")
	o.Model = "grok-other"
	raw, _ = json.Marshal([]Observation{o})
	result = decode[ReportResult](t, &worker, "POST", "/api/models/reports", string(raw), 200)
	if result.Recorded != 0 {
		t.Fatal("disabled agent reports recorded")
	}
}

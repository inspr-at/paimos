// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func catalogFixture(t *testing.T, source string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/catalog/" + source + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func fixtureCatalogClient(t *testing.T, seen *[]string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		source := ""
		for name, endpoint := range vendorURLs {
			if strings.HasPrefix(r.URL.String(), endpoint) {
				source = name
				break
			}
		}
		if source == "" {
			t.Fatalf("unexpected catalog origin %s", r.URL.Host)
		}
		if source == "openrouter" || source == "modelsdev" || source == "litellm" {
			if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" || r.URL.Query().Get("key") != "" {
				t.Fatal("keyless discovery sent credentials")
			}
		}
		if seen != nil {
			*seen = append(*seen, source)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(catalogFixture(t, source)))), Header: make(http.Header)}, nil
	})}
}

// Risk: an aggregator, missing native field, or old seed invents model levels.
func TestCatalogSourcesPreserveNativePrecedenceAndUnknown(t *testing.T) {
	for _, seed := range seedModels {
		if !validHarness(seed.Harness) || !modelRE.MatchString(seed.ID) {
			t.Fatalf("offline seed is not a model identity: %+v", seed)
		}
	}

	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	native, err := ParseProviderCatalog("anthropic", catalogFixture(t, "anthropic"), at)
	if err != nil {
		t.Fatal(err)
	}
	aggregated, err := ParseProviderCatalog("openrouter", catalogFixture(t, "openrouter"), at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	facts := append([]CatalogFact{}, native.Facts...)
	for _, f := range aggregated.Facts {
		if f.Provider == "anthropic" && f.ModelID == "claude-opus-5" {
			facts = append(facts, f)
		}
	}
	got := ResolveCatalogFacts(facts, "pi/anthropic/claude-opus-5")
	if f := got["native_levels"]; f.SourceKind != "native" || f.Source != vendorURLs["anthropic"] || string(f.Value) != `["high","low","max","medium","xhigh"]` {
		t.Fatalf("native fields lost precedence: %+v", f)
	}
	// Route evidence wins only on that exact route, and preserves new level names.
	harness := CatalogFact{Provider: "anthropic", ModelID: "claude-opus-5", Route: "pi/anthropic/claude-opus-5", Fact: "native_levels", Value: json.RawMessage(`["ultra"]`), Source: "harness:pi", SourceKind: "harness", FieldPath: "levels", FetchedAt: at}
	facts = append(facts, harness)
	if f := ResolveCatalogFacts(facts, harness.Route)["native_levels"]; f.SourceKind != "harness" {
		t.Fatalf("route probe did not win: %+v", f)
	}
	if f := ResolveCatalogFacts(facts, "claude/claude-opus-5")["native_levels"]; f.SourceKind != "native" {
		t.Fatal("another route inherited pi's capabilities")
	}
	for _, source := range []string{"openai", "google"} {
		c, err := ParseProviderCatalog(source, catalogFixture(t, source), at)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range c.Facts {
			if f.Fact == "native_levels" && string(f.Value) != "null" {
				t.Fatalf("%s invented levels: %s", source, f.Value)
			}
		}
		if len(catalogObservations(c, "fixture")) != 0 {
			t.Fatalf("%s manufactured dispatch efforts", source)
		}
	}
	if len(discoveryObservations("openai", "fixture", []string{"gpt-6.1-sol"})) != 0 {
		t.Fatal("ID-only discovery borrowed seed levels")
	}
	xai, err := ParseProviderCatalog("xai", catalogFixture(t, "xai"), at)
	if err != nil {
		t.Fatal(err)
	}
	chosen := ResolveCatalogFacts(xai.Facts, "")
	if string(chosen["native_levels"].Value) != `["low","medium","high","xhigh"]` || string(chosen["default_level"].Value) != `"high"` {
		t.Fatalf("xAI native fields lost: %+v", chosen)
	}
	dev, err := ParseProviderCatalog("modelsdev", catalogFixture(t, "modelsdev"), at)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range dev.Facts {
		if f.Provider == "openai" && f.ModelID == "gpt-5.4" && f.Fact == "native_levels" && string(f.Value) != `["none","low","medium","high","xhigh"]` {
			t.Fatalf("models.dev effort values lost: %s", f.Value)
		}
		if f.Provider == "anthropic" && f.Fact == "native_levels" && string(f.Value) != "null" {
			t.Fatal("models.dev budget range was expanded into invented levels")
		}
	}

	pin, ok := observedPin(Observation{Harness: "claude", Model: "claude-opus-5", Effort: "ultra"})
	if !ok || pin.Effort != "ultra" {
		t.Fatal("bounded native name rejected by old allowlist")
	}
}

// Risk: route-specific IDs split a provider model or fabricate Gemini budgets.
func TestCatalogIdentityFoldsCursorEffortsAndProviderRoutes(t *testing.T) {
	for _, e := range []string{"low", "medium", "high", "xhigh", "ultra"} {
		for _, tail := range []string{"", "-fast"} {
			provider, id := CatalogIdentity("xai", "cursor", "grok-4.7-"+e+tail, e)
			if provider != "xai" || id != "grok-4.7" {
				t.Fatalf("route split identity: %s %s", provider, id)
			}
		}
	}
	_, id := CatalogIdentity("xai", "cursor", "grok-4.7-build-fast", "")
	if id != "grok-4.7-build-fast" {
		t.Fatal("fold guessed an unreported effort")
	}
	p1, m1 := CatalogIdentity("", "gemini", "gemini-2.5-pro", "")
	p2, m2 := CatalogIdentity("", "opencode", "google/gemini-2.5-pro", "")
	if p1 != p2 || m1 != m2 {
		t.Fatalf("Google routes have different identities %s/%s %s/%s", p1, m1, p2, m2)
	}
}

// Risk: discovery remains gated on a vault/key, leaks between tenants, or
// enables a discovered model on an account. No live network is permitted.
func TestCatalogKeylessRefreshPersistsFactsWithoutAccountAuthority(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "catalog-keyless", "person", "Owner", []string{"admin"})
	other := makePrincipal(t, "catalog-other", "person", "Other", []string{"admin"})
	seen := []string{}
	m := &Module{pool: appPool, discovery: fixtureCatalogClient(t, &seen)}
	out, err := m.runRefresh(tenant.WithPrincipal(t.Context(), p), p, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, publicCatalogSources) || len(out.Sources) != 3 {
		t.Fatalf("keyless sources not fetched: %v %+v", seen, out)
	}
	for _, source := range out.Sources {
		if source.State != "fresh" || source.Seen == 0 {
			t.Fatalf("source was not discovered: %+v", source)
		}
	}
	inRegistry(t, p, func(tx pgx.Tx) error {
		var n, enabled int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_catalog_facts WHERE source_kind='aggregator'`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			t.Fatal("no public provenance persisted")
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_profiles WHERE enabled`).Scan(&enabled); err != nil {
			return err
		}
		if enabled != 0 {
			t.Fatal("public discovery granted profile/account authority")
		}
		return nil
	})
	inRegistry(t, other, func(tx pgx.Tx) error {
		var n int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_catalog_facts`).Scan(&n)
		if err == nil && n != 0 {
			t.Fatal("catalog facts crossed tenant RLS")
		}
		return err
	})
	mux := http.NewServeMux()
	m.Mount(mux)
	r := httptest.NewRequest("GET", "/api/models/catalog?limit=1", nil).WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("catalog read %d %s", w.Code, w.Body.String())
	}
	var first catalogPage
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Models) != 1 || first.NextCursor == "" || first.Truncated {
		t.Fatalf("keyset page %+v", first)
	}
	r = httptest.NewRequest("GET", "/api/models/catalog?limit=1&cursor="+first.NextCursor, nil).WithContext(tenant.WithPrincipal(t.Context(), p))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var second catalogPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &second) != nil || len(second.Models) != 1 || second.Models[0].Identity <= first.Models[0].Identity {
		t.Fatal("keyset page repeated or lost model")
	}
}

// Risk: missing source fields still become guessed metadata on new pins,
// while retiring the scale destroys the read path of existing pins.
func TestCatalogNewPinsKeepUnknownAndLegacyMetadataReadable(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "catalog-compatible", "person", "Owner", []string{"admin"})
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		pin, err := insertActivatedProfile(t.Context(), tx, p, profileWrite{Slug: "unknown-native", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-opus-5", Effort: "ultra", Tier: "strong"}, true, "")
		if err != nil {
			return err
		}
		if pin.EffortLevel != nil || pin.DisplayName != "claude-opus-5" || pin.ModelVersion != "" {
			t.Fatalf("new metadata invented: %+v", pin)
		}
		level := 3
		old, err := insertActivatedProfile(t.Context(), tx, p, profileWrite{Slug: "legacy-pin", Version: "2", Harness: "codex", Family: "openai", Model: "gpt-6-sol", Effort: "high", Tier: "strong", RegisteredEffortLevel: &level, Display: Display{DisplayName: "Stored Sol", ShortName: "Sol", ModelVersion: "6"}}, true, "")
		if err != nil {
			return err
		}
		if _, err := prepareCatalogDeferred(t.Context(), tx, p); err != nil {
			return err
		}
		profiles, err := listProfiles(t.Context(), tx)
		if err != nil {
			return err
		}
		persisted := profileBySlug(profiles, "legacy-pin")
		if persisted.ID != old.ID || persisted.Display != old.Display || persisted.EffortLevel == nil || *persisted.EffortLevel != 3 {
			t.Fatalf("legacy stored metadata lost: %+v", persisted)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestExistingTenantGetsAdditionalRoutesWithoutReplacingPolicy(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "upgrade", "person", "Ada", []string{"admin"})
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var first string
		for _, profile := range catalogProfiles() {
			if profile.Harness == "gemini" || profile.Harness == "opencode" {
				continue
			}
			r, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: profile.Slug, Version: profile.Version, Harness: profile.Harness, Family: profile.Family, Model: profile.Model, Effort: profile.Effort, Tier: profile.Tier})
			if err != nil {
				return err
			}
			if profile.Slug == "codex-6-1-sol-high" {
				first = r.ID
			}
		}
		return insertRoute(t.Context(), tx, p.TenantID, Route{Role: "build", Priority: 40, ProfileID: first, State: "available", Reason: "saved person policy"})
	})
	if err != nil {
		t.Fatal(err)
	}
	profiles := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", http.StatusOK)
	if len(profiles) != 51 {
		t.Fatal("upgrade profiles", len(profiles))
	}
	readRoutes := func() []Route {
		t.Helper()
		var rows []Route
		if err := db.InTenant(t.Context(), appPool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			rows, err = listRoutes(t.Context(), tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	routes := readRoutes()
	foundPolicy := false
	for _, r := range routes {
		if r.Role == "build" && r.Priority == 40 {
			foundPolicy = r.Reason == "saved person policy"
		}
	}
	if !foundPolicy {
		t.Fatal("saved person policy missing or replaced", routes)
	}
	for _, h := range []string{"gemini", "opencode"} {
		r := decode[Resolution](t, &p, http.MethodGet, "/api/models/resolve?role=build&harness="+h, "", http.StatusOK)
		if r.Profile == nil || r.Profile.Harness != h {
			t.Fatal("new harness not routable", r)
		}
		if h == "gemini" && (r.Profile.EffortLevel == nil || *r.Profile.EffortLevel != 3) {
			t.Fatal("budget level missing", r.Profile)
		}
	}
	again := readRoutes()
	if len(again) != len(routes) || eventCount(t, p, evSeeded) != 1 {
		t.Fatal("upgrade replay changed policy")
	}
}

func TestNewHarnessProfileValidation(t *testing.T) {
	for _, tc := range []struct {
		harness, family, model, effort string
		valid                          bool
	}{
		{"gemini", "google", "gemini-2.5-pro", "16384", true},
		{"gemini", "google", "gemini-2.5-flash", "0", true},
		{"gemini", "google", "gemini-2.5-pro", "0", false},
		{"gemini", "google", "gemini-2.5-pro", "high", false},
		{"opencode", "local", "ollama/qwen3-coder", "default", true},
		{"opencode", "unknown", "custom/private-model", "custom", true},
		{"opencode", "local", "model-only", "default", false},
	} {
		err := validateProfile(profileWrite{Slug: "fixture", Version: "1", Harness: tc.harness, Family: tc.family, Model: tc.model, Effort: tc.effort, Tier: "standard"})
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
	if family, err := NormalizeAuthorFamily("gemini"); err != nil || family != "google" {
		t.Fatal(family, err)
	}
	if _, err := NormalizeAuthorFamily("opencode"); err == nil {
		t.Fatal("multi-provider harness assumed one family")
	}
}

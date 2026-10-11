// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"net/http"
	"testing"
)

// Risk: reading catalog facts appends guessed harness pins or replaces a saved Models choice.
func TestExistingTenantCatalogFactsPreservePolicyAndPins(t *testing.T) {
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
		// The saved policy: a Default order of one step that ends in Wait.
		return dbtest.ModelsOrder(t.Context(), tx, p.TenantID, "other", "first", "wait", first)
	})
	if err != nil {
		t.Fatal(err)
	}
	readOrders := func() string {
		t.Helper()
		var orders string
		if err := db.InTenant(t.Context(), appPool, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT jsonb_agg(jsonb_build_array(column_key,situation,rank,efforts,end_mode) ORDER BY column_key,situation)::text FROM (`+modelprefs.OrdersSQL+`) o`).Scan(&orders)
		}); err != nil {
			t.Fatal(err)
		}
		return orders
	}
	saved := readOrders()
	if saved != `[["other", "first", ["openai:sol"], {"openai:sol": "high"}, "wait"]]` {
		t.Fatal("unexpected saved policy fixture", saved)
	}
	profiles := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", http.StatusOK)
	if len(profiles) != 40 {
		t.Fatal("upgrade profiles", len(profiles))
	}
	if got := readOrders(); got != saved {
		t.Fatal("saved policy missing or replaced", got)
	}
	for _, profile := range profiles {
		if profile.Harness == "gemini" || profile.Harness == "opencode" {
			t.Fatal("catalog fabricated additional harness pin", profile)
		}
	}
	decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", http.StatusOK)
	if readOrders() != saved || eventCount(t, p, evSeeded) != 1 {
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

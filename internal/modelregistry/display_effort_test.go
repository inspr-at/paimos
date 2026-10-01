// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestRegistryEffortScale(t *testing.T) {
	useDB(t)
	for _, tc := range []struct {
		family, effort string
		level          int
	}{
		{"openai", "minimal", 0}, {"openai", "low", 1}, {"openai", "medium", 2}, {"openai", "high", 3}, {"openai", "xhigh", 4},
		{"anthropic", "low", 1}, {"anthropic", "medium", 2}, {"anthropic", "high", 3}, {"anthropic", "xhigh", 4}, {"anthropic", "max", 5},
		{"xai", "low", 1}, {"xai", "medium", 2}, {"xai", "high", 3}, {"xai", "xhigh", 4},
		{"google", "off", 0}, {"google", "0", 0}, {"google", "1", 1}, {"google", "1024", 1}, {"google", "1025", 2}, {"google", "4096", 2}, {"google", "4097", 3}, {"google", "16384", 3}, {"google", "16385", 4}, {"google", "32768", 4}, {"google", "32769", 5},
		{"openai", "ultra", -1}, {"openai", "max", -1}, {"anthropic", "minimal", -1}, {"xai", "max", -1}, {"cursor", "low", -1}, {"unknown", "high", -1}, {"google", "-1", -1}, {"google", "auto", -1}, {"google", "high", -1}, {"openai", "", -1},
	} {
		t.Run(tc.family+"/"+tc.effort, func(t *testing.T) {
			var got *int
			if err := appPool.QueryRow(t.Context(), `SELECT aeon_model_effort_level($1,$2)`, tc.family, tc.effort).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if tc.level < 0 {
				if got != nil {
					t.Fatalf("unknown guessed as %d", *got)
				}
			} else if got == nil || *got != tc.level {
				t.Fatalf("got %v, want %d", got, tc.level)
			}
		})
	}
}

func TestStoredModelDisplayAndEffort(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "display-effort", "person", "Admin", []string{"admin"})
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		for i, tc := range []struct {
			harness, family, model, effort, name, short, version string
			level                                                int
		}{
			{"codex", "openai", "gpt-6.1-sol", "minimal", "Codex Sol", "Sol", "6.1", 0},
			{"claude", "anthropic", "claude-opus-5-5", "max", "Claude Opus", "Opus", "5.5", 5},
			{"claude", "anthropic", "opus", "high", "Claude Opus", "Opus", "", 3},
			{"cursor", "xai", "grok-4.7-xhigh", "xhigh", "Grok", "Grok", "4.7", 4},
			{"cursor", "cursor", "composer-2.5", "default", "Cursor Composer", "Composer", "2.5", -1},
			{"pi", "google", "gemini-3.1-pro", "32769", "Gemini Pro", "Gemini Pro", "3.1", 5},
		} {
			got, err := insertProfile(t.Context(), tx, p.TenantID, profileWrite{Slug: "display-" + itoa(i), Version: "registry-revision-99", Harness: tc.harness, Family: tc.family, Model: tc.model, Effort: tc.effort, Tier: "standard"})
			if err != nil {
				return err
			}
			if got.DisplayName != tc.name || got.ShortName != tc.short || got.ModelVersion != tc.version {
				t.Fatalf("stored model metadata: %+v", got)
			}
			if tc.level < 0 {
				if got.EffortLevel != nil {
					t.Fatal("default guessed")
				}
			} else if got.EffortLevel == nil || *got.EffortLevel != tc.level {
				t.Fatalf("stored effort: %+v", got)
			}
		}
		got, err := createProfile(t.Context(), tx, p, profileWrite{Slug: "explicit-opus", Version: "100", Harness: "claude", Family: "anthropic", Model: "opus", Effort: "high", Tier: "strong", Display: Display{DisplayName: "Claude Opus", ShortName: "Opus", ModelVersion: "5.5"}})
		if err != nil {
			return err
		}
		if got.FullName() != "Claude Opus 5.5" || got.Version == got.ModelVersion {
			t.Fatalf("model version confused with revision: %+v", got)
		}
		profiles, err := listProfiles(t.Context(), tx)
		if err != nil {
			return err
		}
		for _, profile := range profiles {
			if profile.ID == got.ID && profile.FullName() != got.FullName() {
				t.Fatal("read lost display metadata")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

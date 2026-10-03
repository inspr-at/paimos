// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

func TestNormalizeAuthorFamily(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"claude", "anthropic"}, {"codex", "openai"}, {"grok", "xai"},
		{"anthropic", "anthropic"}, {"openai", "openai"}, {"xai", "xai"}, {"cursor", "cursor"},
		{"", ""}, {"  ", ""}, {" codex ", "openai"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeAuthorFamily(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeAuthorFamily(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestAuthorFamilyRejections(t *testing.T) {
	for _, input := range []string{"pi", "unknown"} {
		t.Run(input, func(t *testing.T) {
			got, err := NormalizeAuthorFamily(input)
			if err == nil || got != "" {
				t.Fatalf("invalid family accepted: %q, %v", got, err)
			}
			for _, accepted := range []string{"openai", "anthropic", "xai", "cursor", "codex", "claude", "grok"} {
				if !strings.Contains(err.Error(), accepted) {
					t.Errorf("error %q omits accepted value %q", err, accepted)
				}
			}
			if input == "pi" && (!strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "pass the model family")) {
				t.Fatalf("pi must request an explicit family: %v", err)
			}
			// Both entry points reject invalid families before accessing a DB.
			_, resolveErr := resolveRole(context.Background(), nil, resolveQuery{Role: "review-gate", AuthorFamily: input}, time.Now())
			_, reviewErr := ResolveReview(context.Background(), nil, tenant.Principal{}, input, "", time.Now())
			for _, serverErr := range []error{resolveErr, reviewErr} {
				var he *httpError
				if !errors.As(serverErr, &he) || he.status != http.StatusBadRequest || he.msg != err.Error() {
					t.Errorf("server error = %v; want HTTP 400 with %q", serverErr, err)
				}
			}
		})
	}
}

func TestProfileFamilyMustMatchProvider(t *testing.T) {
	for _, tc := range []struct{ harness, model, family string }{
		{"grok", "grok-4.7", "anthropic"},
		{"claude", "fable", "xai"},
		{"codex", "gpt-6-astra", "anthropic"},
		{"gemini", "gemini-2.5-pro", "openai"},
		{"cursor", "grok-4.7-xhigh", "anthropic"},
		{"cursor", "auto", "openai"},
		{"pi", "anthropic/claude-opus-5", "xai"},
		{"pi", "openrouter/stealth/space-bunny-alpha", "anthropic"},
		{"opencode", "custom/private-model", "openai"},
		{"opencode", "google/gemini-2.5-pro", "openai"},
	} {
		t.Run(tc.harness+"/"+tc.model, func(t *testing.T) {
			effort := "xhigh"
			if tc.harness == "gemini" {
				effort = "16384"
			}
			if err := validateProfile(profileWrite{Slug: "fixture", Version: "1", Harness: tc.harness, Model: tc.model, Family: tc.family, Effort: effort, Tier: "frontier"}); err == nil {
				t.Fatal("mislabelled family accepted")
			}
			role, _ := roleByName("review-gate")
			step := ladderStep{Profile: Profile{Harness: tc.harness, Model: tc.model, Family: tc.family, Enabled: true}}
			reasons := skipReasons(step, role, resolveQuery{AuthorFamily: "local"}, time.Now(), nil)
			if len(reasons) == 0 {
				t.Fatal("legacy mislabelled profile remained routable")
			}
		})
	}
	for _, profile := range catalogProfiles() {
		if err := validateProfile(profileWrite{Slug: profile.Slug, Version: profile.Version, Harness: profile.Harness, Model: profile.Model, Family: profile.Family, Effort: profile.Effort, Tier: profile.Tier}); err != nil {
			t.Fatalf("existing catalog pin %s rejected: %v", profile.Slug, err)
		}
	}
}

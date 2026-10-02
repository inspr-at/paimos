// SPDX-License-Identifier: AGPL-3.0-only
package servicetier

import (
	"strings"
	"testing"
)

func TestAdapterReportsPublishedPriceAndExactModel(t *testing.T) {
	for _, tc := range []struct {
		harness, model, version string
	}{
		{"codex", "gpt-6.1-sol", "codex-cli 0.159.2"},
		{"codex", "gpt-6.1-sol", "0.159.1"},
		{"codex", "unknown", "0.159.2"},
		{"claude", "claude-opus-5-5", "3.2.0"},
		{"claude", "claude-sonnet-5", "3.2.0"},
		{"claude", "claude-opus-5-5", "unknown"},
		{"cursor", "grok-4.7", "2026.09.24"},
		{"pi", "claude-opus-5-5", "1.0.0"},
		{"grok", "grok-4.7", "1.0.0"},
		{"gemini", "gemini-model", "1.0.0"},
		{"opencode", "claude-opus-5-5", "1.0.0"},
	} {
		t.Run(tc.harness+"/"+tc.model+"/"+tc.version, func(t *testing.T) {
			r := Advertised(tc.harness, tc.model, tc.version)
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, tier := range []string{"fast", "fastest"} {
				paid, offered := r.Find(tier)
				if offered || paid.Offered || paid.PriceMultiplier != nil || paid.SpeedFactor != nil || paid.UsageMultiplier != nil || paid.Reason != "not offered: no published price" {
					t.Fatalf("%s without a cited exact-model price was offered: %+v", tier, paid)
				}
			}
			if r.CheckedAt.IsZero() || r.AdapterVersion == "" || r.Tiers[2].Reason == "" {
				t.Fatal("missing provenance or unavailability reason")
			}
		})
	}
}
func TestOfferedRequiresPriceAndSupportedMechanism(t *testing.T) {
	r := Advertised("codex", "gpt-6.1-sol", "0.159.2")
	r.Tiers[1].Offered = true
	if _, ok := r.Find("fast"); ok {
		t.Fatal("tier without price offered")
	}
	if r.Validate() == nil {
		t.Fatal("missing price accepted")
	}
	r = Advertised("codex", "gpt-6.1-sol", "0.159.2")
	r.Tiers[0].Mechanism = "arbitrary-command"
	if r.Validate() == nil {
		t.Fatal("arbitrary launch mechanism accepted")
	}
}

func TestReportFactorsMustMatchPinnedCatalog(t *testing.T) {
	for _, harness := range []string{"codex", "claude", "cursor"} {
		for _, tier := range []int{0, 1, 2} {
			for _, factor := range []string{"speed", "price", "usage"} {
				t.Run(harness+"/"+[]string{"default", "fast", "fastest"}[tier]+"/"+factor, func(t *testing.T) {
					r := Advertised(harness, "gpt-6.1-sol", "3.2.0")
					switch factor {
					case "speed":
						r.Tiers[tier].SpeedFactor = number(2.5)
					case "price":
						r.Tiers[tier].Offered = true
						r.Tiers[tier].PriceMultiplier = number(6)
						r.Tiers[tier].Mechanism = map[string]string{"codex": "service_tier=", "claude": "fastMode=", "cursor": ""}[harness] + []string{"default", "fast", "ultrafast"}[tier]
					case "usage":
						r.Tiers[tier].UsageMultiplier = number(2.5)
					}
					if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "pinned vendor catalog") {
						t.Fatalf("expected a catalog mismatch for the worker-supplied factor, got %v", err)
					}
				})
			}
		}
	}
}

func TestCodexInstructionsNameOnlyServiceTier(t *testing.T) {
	r := Advertised("codex", "gpt-6.1-sol", "0.159.2")
	if !strings.Contains(r.ChangeInstructions, "service_tier") || strings.Contains(r.ChangeInstructions, "/fast") {
		t.Fatalf("incorrect Codex control instructions: %q", r.ChangeInstructions)
	}
}

func TestEveryAdvertisedModelFailsClosedWithoutPricePins(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		for _, r := range Reports(harness, "unknown", "3.2.0") {
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, tier := range []string{"fast", "fastest"} {
				if _, offered := r.Find(tier); offered {
					t.Fatalf("%s/%s/%s offered without a vendor price pin", harness, r.Model, tier)
				}
			}
		}
	}
}

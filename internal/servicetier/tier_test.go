// SPDX-License-Identifier: AGPL-3.0-only
package servicetier

import "testing"

func TestAdapterReportsPublishedPriceAndExactModel(t *testing.T) {
	for _, tc := range []struct {
		harness, model, version string
		fast                    bool
	}{
		{"codex", "gpt-6.1-sol", "codex-cli 0.159.2", true},
		{"codex", "gpt-6.1-sol", "0.159.1", false},
		{"codex", "unknown", "0.159.2", false},
		{"claude", "claude-opus-5-5", "3.2.0", true},
		{"claude", "claude-sonnet-5", "3.2.0", false},
		{"claude", "claude-opus-5-5", "unknown", false},
		{"cursor", "grok-4.7", "2026.09.24", false},
		{"pi", "claude-opus-5-5", "1.0.0", false},
		{"grok", "grok-4.7", "1.0.0", false},
		{"gemini", "gemini-model", "1.0.0", false},
		{"opencode", "claude-opus-5-5", "1.0.0", false},
	} {
		t.Run(tc.harness+"/"+tc.model+"/"+tc.version, func(t *testing.T) {
			r := Advertised(tc.harness, tc.model, tc.version)
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			fast, ok := r.Find("fast")
			if ok != tc.fast {
				t.Fatalf("fast offered=%t", ok)
			}
			if tc.fast && *fast.PriceMultiplier != 2 {
				t.Fatal("fragment placeholders became prices")
			}
			if _, ok = r.Find("fastest"); ok {
				t.Fatal("access-controlled tier assumed available")
			}
			if r.CheckedAt.IsZero() || r.AdapterVersion == "" || r.Tiers[2].Reason == "" {
				t.Fatal("missing provenance or unavailability reason")
			}
		})
	}
}
func TestOfferedRequiresPriceAndSupportedMechanism(t *testing.T) {
	r := Advertised("codex", "gpt-6.1-sol", "0.159.2")
	r.Tiers[1].PriceMultiplier = nil
	if _, ok := r.Find("fast"); ok {
		t.Fatal("tier without price offered")
	}
	if r.Validate() == nil {
		t.Fatal("missing price accepted")
	}
	r = Advertised("codex", "gpt-6.1-sol", "0.159.2")
	r.Tiers[1].Mechanism = "arbitrary-command"
	if r.Validate() == nil {
		t.Fatal("arbitrary launch mechanism accepted")
	}
}

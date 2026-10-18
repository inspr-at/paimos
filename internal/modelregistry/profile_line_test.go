// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import "testing"

func TestProfileLine(t *testing.T) {
	for _, tt := range []struct{ h, id, line, version string }{
		{"codex", "gpt-6.1-sol", "sol", "6.1"}, {"codex", "gpt-6.2-sol", "sol", "6.2"}, {"codex", "gpt-6-sol", "sol", "6"},
		{"claude", "opus", "opus", "alias"}, {"claude", "claude-opus-5-5", "opus", "5.5"},
		{"pi", "anthropic/claude-opus-5", "opus", "5"}, {"grok", "grok-4.7", "grok", "4.7"},
		{"grok", "grok-4.7-build-fast", "grok-build-fast", "4.7"}, {"cursor", "grok-4.7-xhigh", "grok", "4.7"},
		{"cursor", "grok-4.7-xhigh-fast", "grok-fast", "4.7"}, {"cursor", "grok-4.7-low-fast", "grok-fast", "4.7"},
		{"cursor", "composer-2.5", "composer", "2.5"}, {"cursor", "composer-2.5-fast", "composer-fast", "2.5"},
		{"gemini", "gemini-2.5-pro", "gemini-pro", "2.5"},
		{"opencode", "google/gemini-2.5-pro", "gemini-pro", "2.5"},
		{"opencode", "ollama/qwen3-coder", "qwen-coder", "3"},
		{"pi", "openrouter/stealth/space-bunny-alpha", "openrouter/stealth/space-bunny-alpha", ""},
	} {
		t.Run(tt.h+"/"+tt.id, func(t *testing.T) {
			f, l, v := ProfileLine(Profile{Harness: tt.h, Model: tt.id, Family: "test"})
			if f != "test" || l != tt.line || v != tt.version {
				t.Fatalf("%s %s %s", f, l, v)
			}
		})
	}
	for _, p := range catalogProfiles() {
		_, line, v := ProfileLine(Profile{Harness: p.Harness, Model: p.Model, Family: p.Family})
		if line == "" || v == "" && p.Family != "unknown" {
			t.Fatalf("seed not recognized: %+v", p)
		}
	}
	for _, tt := range []struct {
		a, b string
		want int
	}{{"6.10", "6.2", 1}, {"6", "6.0", 0}, {"alias", "99.9", 1}, {"5.5", "alias", -1}} {
		if got := CompareModelVersions(tt.a, tt.b); got != tt.want {
			t.Fatal(tt, got)
		}
	}
}

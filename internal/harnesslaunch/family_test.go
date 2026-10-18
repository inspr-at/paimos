// SPDX-License-Identifier: AGPL-3.0-only
package harnesslaunch

import "testing"

func TestModelFamilyUsesProviderBinding(t *testing.T) {
	for _, tc := range []struct{ harness, model, family string }{
		{"grok", "grok-4.7", "xai"}, {"claude", "fable", "anthropic"}, {"codex", "gpt-6-astra", "openai"}, {"gemini", "gemini-2.5-pro", "google"},
		{"cursor", "grok-4.7-xhigh", "xai"}, {"cursor", "composer-2.5", "cursor"}, {"cursor", "gpt-6-astra", "openai"}, {"cursor", "claude-opus-5", "anthropic"}, {"cursor", "gemini-2.5-pro", "google"},
		{"pi", "anthropic/claude-opus-5", "anthropic"}, {"opencode", "google/gemini-2.5-pro", "google"}, {"opencode", "ollama/qwen3-coder", "local"},
		{"pi", "openrouter/anthropic/claude-opus-5", "unknown"}, {"opencode", "custom/claude-opus-5", "unknown"}, {"cursor", "auto", "unknown"}, {"cursor", "grok-", "unknown"}, {"pi", "anthropic/", "unknown"}, {"unknown", "grok-4.7", "unknown"}, {"grok", "", "unknown"},
	} {
		if got := ModelFamily(tc.harness, tc.model); got != tc.family {
			t.Errorf("%s/%s = %s, want %s", tc.harness, tc.model, got, tc.family)
		}
		if got := FamilyMatches(tc.harness, tc.model, tc.family); got != (tc.family != "unknown") {
			t.Errorf("family certainty for %s/%s = %v", tc.harness, tc.model, got)
		}
	}
}

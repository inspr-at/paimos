// SPDX-License-Identifier: AGPL-3.0-only

package harnesslaunch

import "strings"

// ModelFamily derives identity from the launcher/provider binding, never from
// a registry's declared family. Unknown aggregators and automatic model choices
// cannot establish independence. Keep this shared by routing and evidence gates.
func ModelFamily(harness, model string) string {
	if model == "" {
		return "unknown"
	}
	switch harness {
	case "codex":
		return "openai"
	case "claude":
		return "anthropic"
	case "grok":
		return "xai"
	case "gemini":
		return "google"
	case "pi", "opencode":
		provider, name, ok := strings.Cut(model, "/")
		if !ok || name == "" {
			return "unknown"
		}
		switch provider {
		case "openai", "anthropic", "xai", "google":
			return provider
		case "ollama":
			return "local"
		}
	case "cursor":
		// Cursor routes several providers. Its explicit vendor namespaces are
		// the identity; neither the harness name nor a tenant label is enough.
		for prefix, family := range map[string]string{
			"gpt-": "openai", "claude-": "anthropic", "grok-": "xai",
			"gemini-": "google", "composer-": "cursor",
		} {
			if strings.HasPrefix(model, prefix) && len(model) > len(prefix) {
				return family
			}
		}
	}
	return "unknown"
}

// FamilyMatches also refuses unknown identity: unknown is a usable build pin,
// but must never establish either side of a cross-family review.
func FamilyMatches(harness, model, family string) bool {
	actual := ModelFamily(harness, model)
	return actual != "unknown" && actual == family
}

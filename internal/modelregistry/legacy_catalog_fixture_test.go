// SPDX-License-Identifier: AGPL-3.0-only
// Historical v3 pins are fixtures for compatibility tests, never production authority.
package modelregistry

import "strings"

type legacySeedModel struct {
	Harness string
	ID      string
	Family  string
	Tier    string
	Efforts []string
}

// Pinned model authority. Slugs follow the paimos catalog ids. Dots are
// rewritten to hyphens because an AEON slug cannot contain '.'; the vendor
// model id is stored unchanged.
var legacySeedModels = []legacySeedModel{
	{"codex", "gpt-6-luna", "openai", "fast", []string{"medium", "high", "xhigh"}},
	{"codex", "gpt-6.1-sol", "openai", "strong", []string{"medium", "high", "xhigh"}},
	{"grok", "grok-4.7", "xai", "frontier", []string{"medium", "high", "xhigh"}},
	{"grok", "grok-4.7-build-fast", "xai", "standard", []string{"high"}},
	{"grok", "grok-4.6", "xai", "strong", []string{"high", "xhigh"}},
	{"grok", "grok-4.5", "xai", "standard", []string{"high"}},
	{"codex", "gpt-6-sol", "openai", "strong", []string{"medium", "high", "xhigh"}},
	{"codex", "gpt-6-astra", "openai", "frontier", []string{"medium", "high", "xhigh"}},
	{"claude", "haiku", "anthropic", "fast", []string{"medium", "high"}},
	{"claude", "sonnet", "anthropic", "standard", []string{"high"}},
	{"claude", "opus", "anthropic", "strong", []string{"high", "xhigh"}},
	{"claude", "fable", "anthropic", "frontier", []string{"high", "xhigh"}},
	{"pi", "anthropic/claude-sonnet-5", "anthropic", "standard", []string{"high"}},
	{"pi", "anthropic/claude-opus-5", "anthropic", "strong", []string{"high", "xhigh"}},
	{"cursor", "composer-2.5", "cursor", "standard", []string{"default"}},
	{"cursor", "composer-2.5-fast", "cursor", "standard", []string{"default"}},
	{"cursor", "grok-4.7-low", "xai", "frontier", []string{"low"}},
	{"cursor", "grok-4.7-medium", "xai", "frontier", []string{"medium"}},
	{"cursor", "grok-4.7-high", "xai", "frontier", []string{"high"}},
	{"cursor", "grok-4.7-xhigh", "xai", "frontier", []string{"xhigh"}},
	{"cursor", "grok-4.7-low-fast", "xai", "frontier", []string{"low"}},
	{"cursor", "grok-4.7-medium-fast", "xai", "frontier", []string{"medium"}},
	{"cursor", "grok-4.7-high-fast", "xai", "frontier", []string{"high"}},
	{"cursor", "grok-4.7-xhigh-fast", "xai", "frontier", []string{"xhigh"}},
	// Fixed thinking-budget pins, compatible with AEON-511C's Google buckets.
	// Gemini 2.5 Pro cannot disable thinking; Flash has a lower maximum.
	{"gemini", "gemini-2.5-flash", "google", "fast", []string{"0", "1024", "4096", "16384", "24576"}},
	{"gemini", "gemini-2.5-pro", "google", "strong", []string{"1024", "4096", "16384", "32768"}},
	{"opencode", "google/gemini-2.5-pro", "google", "strong", []string{"default"}},
	{"opencode", "ollama/qwen3-coder", "local", "standard", []string{"default"}},
}

func catalogProfiles() []seedProfile {
	out := []seedProfile{{Slug: "pi-openrouter-space-bunny-alpha-off", Version: CatalogVersion, Harness: "pi", Family: "unknown", Model: "openrouter/stealth/space-bunny-alpha", Effort: "off", Tier: "standard"}}
	for _, model := range legacySeedModels {
		for _, effort := range model.Efforts {
			out = append(out, seedProfile{
				Slug:    catalogSlug(model, effort),
				Version: CatalogVersion,
				Harness: model.Harness,
				Family:  model.Family,
				Model:   model.ID,
				Effort:  effort,
				Tier:    model.Tier,
			})
		}
	}
	return out
}

func catalogSlug(model legacySeedModel, effort string) string {
	id := model.Harness + "-" + model.ID + "-" + effort
	switch model.Harness {
	case "grok":
		id = model.ID + "-" + effort
	case "codex":
		id = "codex-" + strings.TrimPrefix(strings.TrimPrefix(model.ID, "gpt-6-"), "gpt-") + "-" + effort
	case "pi":
		id = "pi-anthropic-" + strings.TrimSuffix(strings.TrimPrefix(model.ID, "anthropic/claude-"), "-5") + "-" + effort
	case "cursor":
		id = "cursor-" + model.ID
		switch model.ID {
		case "composer-2.5":
			id = "cursor-composer"
		case "grok-4.7-high":
			id = "cursor-grok"
		case "grok-4.7-xhigh":
			id = "cursor-grok-xhigh"
		}
	}
	id = strings.ReplaceAll(id, "/", "-")
	return strings.ReplaceAll(id, ".", "-")
}

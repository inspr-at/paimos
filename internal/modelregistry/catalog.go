// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import "strings"

// CatalogVersion is the classic dispatch catalog pin seeded for a new tenant.
const CatalogVersion = "2"

type seedProfile struct {
	Slug    string
	Version string
	Harness string
	Family  string
	Model   string
	Effort  string
	Tier    string
}

type seedModel struct {
	Harness string
	ID      string
	Family  string
	Tier    string
	Efforts []string
}

type seedRoute struct {
	Role     string
	Priority int
	Slug     string
}

// Classic model authority. Slugs follow the paimos catalog ids. Dots are
// rewritten to hyphens because an AEON slug cannot contain '.'; the vendor
// model id is stored unchanged.
var seedModels = []seedModel{
	{"codex", "gpt-6-luna", "openai", "fast", []string{"medium", "high", "xhigh"}},
	{"codex", "gpt-6-terra", "openai", "standard", []string{"medium", "high", "xhigh"}},
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
	for _, model := range seedModels {
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

func catalogSlug(model seedModel, effort string) string {
	id := model.Harness + "-" + model.ID + "-" + effort
	switch model.Harness {
	case "codex":
		id = "codex-" + strings.TrimPrefix(model.ID, "gpt-6-") + "-" + effort
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

type roleDef struct {
	name     string
	tier     string
	effort   string
	cross    bool
	readOnly bool
	ladder   []string
}

func roleByName(name string) (roleDef, bool) {
	for _, role := range roleLadder {
		if role.name == name {
			return role, true
		}
	}
	return roleDef{}, false
}

var roleLadder = []roleDef{
	{name: "scout", tier: "fast", effort: "medium"},
	{name: "mechanical", tier: "fast", effort: "high"},
	{name: "build", tier: "standard", effort: "high"},
	{name: "build-hard", tier: "strong", effort: "xhigh"},
	{name: "review-gate", tier: "frontier", effort: "xhigh", cross: true, readOnly: true,
		ladder: []string{"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "cursor-grok-xhigh"}},
}

func defaultRoutes(profiles []seedProfile) []seedRoute {
	sorted := append([]seedProfile(nil), profiles...)
	sortProfiles(sorted)
	var out []seedRoute
	for _, role := range roleLadder {
		var slugs []string
		if role.cross {
			slugs = append(slugs, role.ladder...)
			slugs = append(slugs, "gemini-gemini-2-5-pro-32768", "opencode-google-gemini-2-5-pro-default")
		} else {
			for _, harness := range []string{"codex", "claude", "pi", "cursor"} {
				for _, profile := range sorted {
					if profile.Harness == harness && profile.Tier == role.tier && profile.Effort == role.effort {
						slugs = append(slugs, profile.Slug)
					}
				}
			}
			// Numeric Google budgets use AEON-511c's provider mapping, never
			// Anthropic/OpenAI names sent to the vendor. Existing order stays first.
			switch role.name {
			case "scout":
				slugs = append(slugs, "gemini-gemini-2-5-flash-4096")
			case "mechanical":
				slugs = append(slugs, "gemini-gemini-2-5-flash-16384")
			case "build":
				slugs = append(slugs, "gemini-gemini-2-5-pro-16384", "opencode-ollama-qwen3-coder-default")
			case "build-hard":
				slugs = append(slugs, "gemini-gemini-2-5-pro-32768", "opencode-google-gemini-2-5-pro-default")
			}
		}
		for i, slug := range slugs {
			out = append(out, seedRoute{Role: role.name, Priority: i + 1, Slug: slug})
		}
	}
	return out
}

func sortProfiles(profiles []seedProfile) {
	for i := 1; i < len(profiles); i++ {
		j := i
		for j > 0 && profiles[j].Slug < profiles[j-1].Slug {
			profiles[j], profiles[j-1] = profiles[j-1], profiles[j]
			j--
		}
	}
}

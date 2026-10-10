// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

// CatalogVersion is the dispatch catalog pin seeded and upgraded automatically.
const CatalogVersion = "4"

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
}

// IDs only: offline bootstrap advertises identity, never capabilities or levels.
var seedModels = []seedModel{
	{"cursor", "grok-4.7"},
	{"codex", "gpt-6-luna"},
	{"medium", "high"},
	{"codex", "gpt-6.1-sol"},
	{"medium", "high"},
	{"grok", "grok-4.7"},
	{"medium", "high"},
	{"grok", "grok-4.7-build-fast"},
	{"grok", "grok-4.6"},
	{"grok", "grok-4.5"},
	{"codex", "gpt-6-sol"},
	{"medium", "high"},
	{"codex", "gpt-6-astra"},
	{"medium", "high"},
	{"claude", "haiku"},
	{"claude", "sonnet"},
	{"claude", "opus"},
	{"claude", "fable"},
	{"pi", "anthropic/claude-sonnet-5"},
	{"pi", "anthropic/claude-opus-5"},
	{"cursor", "composer-2.5"},
	{"cursor", "composer-2.5-fast"},
	{"gemini", "gemini-2.5-flash"},
	{"0", "1024"},
	{"gemini", "gemini-2.5-pro"},
	{"1024", "4096"},
	{"opencode", "google/gemini-2.5-pro"},
	{"opencode", "ollama/qwen3-coder"},
}

type seedRoute struct {
	Role     string
	Priority int
	Slug     string
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
	{name: "build", tier: "standard", effort: "high", ladder: []string{"codex-6-1-sol-high", "claude-sonnet-high", "pi-anthropic-sonnet-high"}},
	{name: "build-hard", tier: "strong", effort: "xhigh", ladder: []string{"codex-6-1-sol-xhigh", "codex-sol-xhigh", "claude-opus-xhigh", "pi-anthropic-opus-xhigh"}},
	{name: "review-gate", tier: "frontier", effort: "xhigh", cross: true, readOnly: true,
		ladder: []string{"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "grok-4-7-xhigh", "cursor-grok-xhigh"}},
	{name: "review-gate-security", tier: "frontier", effort: "xhigh", cross: true, readOnly: true,
		ladder: []string{"grok-4-7-xhigh", "cursor-grok-xhigh", "codex-6-1-sol-xhigh"}},
}

func defaultRoutes(profiles []seedProfile) []seedRoute {
	sorted := append([]seedProfile(nil), profiles...)
	sortProfiles(sorted)
	var out []seedRoute
	for _, role := range roleLadder {
		var slugs []string
		if len(role.ladder) > 0 {
			slugs = append(slugs, role.ladder...)
		} else {
			for _, harness := range []string{"codex", "claude", "pi", "cursor"} {
				for _, profile := range sorted {
					if profile.Harness == harness && profile.Tier == role.tier && profile.Effort == role.effort {
						slugs = append(slugs, profile.Slug)
					}
				}
			}
		}
		// Keep the additional harnesses behind the role's original candidates.
		switch role.name {
		case "scout":
			slugs = append(slugs, "gemini-gemini-2-5-flash-4096")
		case "mechanical":
			slugs = append(slugs, "gemini-gemini-2-5-flash-16384")
		case "build":
			slugs = append(slugs, "gemini-gemini-2-5-pro-16384", "opencode-ollama-qwen3-coder-default")
		case "build-hard", "review-gate":
			slugs = append(slugs, "gemini-gemini-2-5-pro-32768", "opencode-google-gemini-2-5-pro-default")
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

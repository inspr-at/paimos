// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

// CatalogVersion identifies the ID-only offline evidence bootstrap.
const CatalogVersion = "5"

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
	{"codex", "gpt-6.1-sol"},
	{"grok", "grok-4.7"},
	{"grok", "grok-4.7-build-fast"},
	{"grok", "grok-4.6"},
	{"grok", "grok-4.5"},
	{"codex", "gpt-6-sol"},
	{"codex", "gpt-6-astra"},
	{"claude", "haiku"},
	{"claude", "sonnet"},
	{"claude", "opus"},
	{"claude", "fable"},
	{"pi", "anthropic/claude-sonnet-5.5"},
	{"pi", "anthropic/claude-opus-5.5"},
	{"cursor", "composer-2.5"},
	{"cursor", "composer-2.5-fast"},
	{"gemini", "gemini-2.5-flash"},
	{"gemini", "gemini-2.5-pro"},
	{"opencode", "google/gemini-2.5-pro"},
	{"opencode", "ollama/qwen3-coder"},
}

type roleDef struct {
	name     string
	tier     string
	effort   string
	cross    bool
	readOnly bool
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
	{name: "review-gate", tier: "frontier", effort: "xhigh", cross: true, readOnly: true},
	{name: "review-gate-security", tier: "frontier", effort: "xhigh", cross: true, readOnly: true},
}

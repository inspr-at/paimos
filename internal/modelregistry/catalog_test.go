// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"regexp"
	"testing"
)

func TestCatalogV3Ladders(t *testing.T) {
	profiles := catalogProfiles()
	if len(profiles) != 39 {
		t.Fatalf("catalog has %d profiles", len(profiles))
	}
	slugOK := regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	seen := map[string]seedProfile{}
	for _, profile := range profiles {
		if !slugOK.MatchString(profile.Slug) || profile.Version != CatalogVersion {
			t.Fatalf("bad slug %q", profile.Slug)
		}
		if KnownInvalid(profile.Model) {
			t.Fatalf("known-invalid seed: %s", profile.Model)
		}
		seen[profile.Slug] = profile
	}
	if seen["cursor-grok-4-7-low"].Model != "grok-4.7-low" {
		t.Fatal("dotted vendor model was not preserved")
	}
	if seen["pi-anthropic-opus-xhigh"].Model != "anthropic/claude-opus-5" {
		t.Fatal("pi model pin drifted")
	}
	routes := defaultRoutes(profiles)
	got := map[string][]string{}
	for _, route := range routes {
		got[route.Role] = append(got[route.Role], route.Slug)
		if route.Priority != len(got[route.Role]) {
			t.Fatalf("priority gap for %s", route.Role)
		}
	}
	want := map[string][]string{
		"scout":                {"codex-luna-medium", "claude-haiku-medium"},
		"mechanical":           {"codex-luna-high", "claude-haiku-high"},
		"build":                {"codex-6-1-sol-high", "claude-sonnet-high", "pi-anthropic-sonnet-high"},
		"build-hard":           {"codex-6-1-sol-xhigh", "codex-sol-xhigh", "claude-opus-xhigh", "pi-anthropic-opus-xhigh"},
		"review-gate":          {"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "grok-4-7-xhigh", "cursor-grok-xhigh"},
		"review-gate-security": {"grok-4-7-xhigh", "cursor-grok-xhigh", "codex-6-1-sol-xhigh"},
	}
	for role, slugs := range want {
		if stringsJoin(got[role]) != stringsJoin(slugs) {
			t.Fatalf("%s ladder = %v", role, got[role])
		}
	}
}

func TestCommandTemplates(t *testing.T) {
	tests := []struct {
		harness, model, effort string
		readOnly               bool
		want                   string
	}{
		{"codex", "gpt-6-astra", "xhigh", true, "codex exec -m gpt-6-astra -c model_reasoning_effort=xhigh --sandbox read-only '{prompt}'"},
		{"codex", "gpt-6.1-sol", "high", false, "codex exec -m gpt-6.1-sol -c model_reasoning_effort=high '{prompt}'"},
		{"claude", "fable", "xhigh", true, "claude -p --model fable --effort xhigh --permission-mode plan '{prompt}'"},
		{"cursor", "grok-4.7-xhigh", "xhigh", false, "cursor-agent --trust --model grok-4.7-xhigh -p '{prompt}'"},
		{"cursor", "grok-4.7-xhigh", "xhigh", true, "cursor-agent --trust --mode ask --model grok-4.7-xhigh -p '{prompt}'"},
		{"pi", "anthropic/claude-opus-5", "xhigh", true, "pi --model anthropic/claude-opus-5:xhigh --tools read,grep,find,ls -p '{prompt}'"},
		{"grok", "grok-4", "high", false, "grok --model grok-4 --reasoning-effort high -p '{prompt}'"},
	}
	for _, test := range tests {
		got, err := commandTemplate(test.harness, test.model, test.effort, test.readOnly)
		if err != nil || got != test.want {
			t.Fatalf("%s readOnly=%v: %q %v", test.harness, test.readOnly, got, err)
		}
	}
}

func stringsJoin(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += "|"
		}
		out += part
	}
	return out
}

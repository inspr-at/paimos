// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillRenderHeaderAndNativeFrontmatter(t *testing.T) {
	raw := []byte(`{
		"canonical_schema_version": "1.0.0",
		"project": {"id": "11111111-1111-4111-8111-111111111111", "name": "Acme", "key": "ACME"},
		"agent": {
			"name": "ops",
			"description": "Keeps the lights on.",
			"slash_command_name": "ops",
			"lane_tags": [],
			"metadata": {},
			"body": "Body.",
			"bootstrap_steps": [{"title": "Probe", "command": "true", "rationale": "sanity"}],
			"non_negotiable_rules": [{"title": "No floats", "body": "money is minor units", "memory_ref": "money"}]
		},
		"repos": [{"label": "INSPR", "url": "https://example.test/inspr", "default_branch": "main"}],
		"environments": [{"name": "Sentry", "url": "https://sentry.example", "host_alias": "", "host_ip": ""}],
		"deploy_recipes": []
	}`)
	rendered, err := renderThroughHarness(raw, "claude-code", "ACME", "ops")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered.Body, "<!-- paimos: rendered from ACME/ops@") || !strings.Contains(rendered.Body, "harness=claude-code -->") {
		t.Fatalf("header missing:\n%s", rendered.Body)
	}
	if !strings.Contains(rendered.Body, "You are operating as the **ops session** for Acme") || !strings.Contains(rendered.Body, "## Bootstrap") || !strings.Contains(rendered.Body, "money") || !strings.Contains(rendered.Body, "https://sentry.example") {
		t.Fatalf("body missing sections:\n%s", rendered.Body)
	}
	if rendered.SuggestedPath != filepath.Join(".claude", "commands", "ops.md") || len(rendered.Rev) != 12 {
		t.Fatalf("path %q rev %q", rendered.SuggestedPath, rendered.Rev)
	}
	native, err := renderThroughHarness(raw, "codex", "ACME", "ops")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(native.Body, "---\nname: \"ops\"\n") || !strings.Contains(native.Body, "<!-- paimos: rendered from ACME/ops@") || !strings.Contains(native.Body, "harness=codex -->") {
		t.Fatalf("native header:\n%s", native.Body)
	}
	if native.SuggestedPath != filepath.Join(".agents", "skills", "ops", "SKILL.md") {
		t.Fatalf("native path %q", native.SuggestedPath)
	}
	for _, harness := range []string{"gemini", "opencode"} {
		got, err := renderThroughHarness(raw, harness, "ACME", "ops")
		if err != nil || got.SuggestedPath != filepath.Join("."+harness, "skills", "ops", "SKILL.md") || !strings.Contains(got.Body, "harness="+harness+" -->") || !strings.HasPrefix(got.Body, "---\nname: \"ops\"\n") {
			t.Fatal("vendor skill render", harness, got.SuggestedPath, err)
		}
	}
	if _, err := renderThroughHarness(raw, "nope", "ACME", "ops"); err == nil {
		t.Fatal("unknown harness should fail")
	}
}

func TestCompareRenderedHeaderCases(t *testing.T) {
	rendered := buildHeader("AEON", "ops", "abc", "claude-code") + "\n\nbody\n"
	if compareRendered(rendered, rendered) != checkIdentical {
		t.Fatal("identical")
	}
	edited := buildHeader("AEON", "ops", "stale", "claude-code") + "\n\nchanged\n"
	if compareRendered(rendered, edited) != checkDiff {
		t.Fatal("diff")
	}
	if compareRendered(rendered, "# hand written\n") != checkHeaderMissing {
		t.Fatal("header")
	}
	if managedHeader("---\nname: \"ops\"\n---\n"+rendered) == "" {
		t.Fatal("frontmatter should still expose the managed header")
	}
}

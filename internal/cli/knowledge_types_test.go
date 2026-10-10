// SPDX-License-Identifier: AGPL-3.0-only

package cli

import "testing"

func TestKnowledgeKindSlug(t *testing.T) {
	cases := map[string]string{
		"decision":        "decision",
		"memory":          "memory",
		"runbook":         "runbook",
		"guideline":       "guideline",
		"external-system": "external_system",
		"external_system": "external_system",
		"related-project": "related_project",
		"related_project": "related_project",
	}
	for in, want := range cases {
		got, ok := knowledgeKindSlug(in)
		if !ok || got != want {
			t.Fatalf("knowledgeKindSlug(%q) = %q, %v; want %q", in, got, ok, want)
		}
		if cli := knowledgeCLIType(got); in == "external_system" || in == "related_project" {
			if cli != knowledgeCLIType(want) {
				t.Fatalf("cli type %q", cli)
			}
		}
	}
	if knowledgeCLIType("external_system") != "external-system" || knowledgeCLIType("related_project") != "related-project" {
		t.Fatal("cli type mapping")
	}
	if _, ok := knowledgeKindSlug("nope"); ok || knowledgeSupported("nope") {
		t.Fatal("unknown type should be rejected")
	}
	if !knowledgeSupported("external-system") || !knowledgeSupported("related_project") {
		t.Fatal("new knowledge types should be supported")
	}
}

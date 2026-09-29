// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The contract is edited by hand in several packages; it must stay valid YAML
// (it was broken twice on 2026-09-26: an unclosed brace and braces in a flow map).
func TestOpenAPISpecParses(t *testing.T) {
	b, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(b, &spec); err != nil {
		t.Fatalf("api/openapi.yaml does not parse: %v", err)
	}
	if spec["openapi"] == nil || spec["paths"] == nil {
		t.Fatal("api/openapi.yaml lacks openapi or paths")
	}
	paths, _ := spec["paths"].(map[string]any)
	outcomes, _ := paths["/outcomes"].(map[string]any)
	post, _ := outcomes["post"].(map[string]any)
	description, ok := post["description"].(string)
	if !ok || !strings.Contains(description, "auto:") || strings.Contains(description, "\n") {
		t.Fatalf("recordOutcome description = %#v", post["description"])
	}
}

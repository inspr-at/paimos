// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCommandPaletteOpenAPIClosesSettingsAndGroupedSearch(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/command-palette/v1/settings", "/api/command-palette/v1/instance-settings", "/api/projects/{id}/command-palette/v1"} {
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing path %s", path)
		}
		for method, rawOperation := range pathItem {
			if method == "parameters" {
				continue
			}
			responses := rawOperation.(map[string]any)["responses"].(map[string]any)
			for status, rawResponse := range responses {
				response := rawResponse.(map[string]any)
				headers, ok := response["headers"].(map[string]any)
				if !ok || headers["Cache-Control"] == nil {
					t.Fatalf("%s %s response %s lacks private Cache-Control", method, path, status)
				}
			}
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"CommandPaletteSettings", "CommandPaletteSettingWrite", "CommandPaletteSessionResult",
		"CommandPaletteNodeResult", "CommandPaletteKnowledgeResult", "CommandPaletteSearchResponse"} {
		schema, ok := schemas[name].(map[string]any)
		if !ok {
			t.Fatalf("missing schema %s", name)
		}
		if closed, ok := schema["additionalProperties"].(bool); !ok || closed {
			t.Fatalf("schema %s is not closed", name)
		}
	}
	search := schemas["CommandPaletteSearchResponse"].(map[string]any)["properties"].(map[string]any)
	if len(search) != 5 || search["schema_version"] == nil || search["query"] == nil || search["sessions"] == nil || search["nodes"] == nil || search["knowledge"] == nil {
		t.Fatalf("grouped response fields=%v", search)
	}
	for name, forbidden := range map[string][]string{
		"CommandPaletteSessionResult":   {"project_id"},
		"CommandPaletteNodeResult":      {"project_id", "description"},
		"CommandPaletteKnowledgeResult": {"project_id", "body", "description", "metadata"},
	} {
		properties := schemas[name].(map[string]any)["properties"].(map[string]any)
		for _, field := range forbidden {
			if properties[field] != nil {
				t.Fatalf("%s exposes forbidden field %s", name, field)
			}
		}
	}
}

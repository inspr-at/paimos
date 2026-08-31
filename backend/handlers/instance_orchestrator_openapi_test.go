// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <camyb@users.noreply.github.com>

package handlers

import (
	"encoding/json"
	"os"
	"testing"
)

func TestOrchestratorOpenAPIClosesPublicAndAdministrativeShapes(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/orchestrator/v1", "/api/orchestrator/v1/config", "/api/orchestrator/v1/events"} {
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing path %s", path)
		}
		for method, rawOperation := range pathItem {
			operation := rawOperation.(map[string]any)
			responses := operation["responses"].(map[string]any)
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
	for _, name := range []string{"OrchestratorProjection", "OrchestratorConfigTarget", "OrchestratorAuditTarget",
		"OrchestratorProjectionResponse", "OrchestratorConfigResponse", "OrchestratorPutRequest", "OrchestratorEvent", "OrchestratorEventsResponse"} {
		schema, ok := schemas[name].(map[string]any)
		if !ok {
			t.Fatalf("missing schema %s", name)
		}
		if closed, ok := schema["additionalProperties"].(bool); !ok || closed {
			t.Fatalf("schema %s is not closed", name)
		}
	}
	projection := schemas["OrchestratorProjection"].(map[string]any)["properties"].(map[string]any)
	if len(projection) != 1 || projection["display_label"] == nil {
		t.Fatalf("ordinary projection exposes more than display_label: %v", projection)
	}
	operation := schemas["OrchestratorEvent"].(map[string]any)["properties"].(map[string]any)["operation"].(map[string]any)
	values := operation["enum"].([]any)
	if len(values) != 5 {
		t.Fatalf("operation enum=%v", values)
	}
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestAgentMessageOpenAPIPreservesV1AndDeclaresV2(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/projects/{id}/messages", "/api/v2/projects/{id}/messages", "/api/v2/projects/{id}/messages/listen", "/api/v2/issues/{id}/messages"} {
		if paths[path] == nil {
			t.Fatalf("missing agent message contract path %s", path)
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"AgentMessageV1", "AgentMessageV2", "AgentMessageSendV1", "AgentMessageSendV2"} {
		schema := schemas[name].(map[string]any)
		if closed, ok := schema["additionalProperties"].(bool); !ok || closed {
			t.Fatalf("schema %s is not closed", name)
		}
	}
	v1 := schemas["AgentMessageV1"].(map[string]any)
	v1Properties := v1["properties"].(map[string]any)
	if v1Properties["expects_reply"] != nil || v1Properties["human_resolution_outcome"] != nil {
		t.Fatalf("v2 fields leaked into OpenAPI v1: %v", v1Properties)
	}
	v2 := schemas["AgentMessageV2"].(map[string]any)
	required := v2["required"].([]any)
	requiredNames := make([]string, len(required))
	for i := range required {
		requiredNames[i] = required[i].(string)
	}
	if !slices.Contains(requiredNames, "expects_reply") {
		t.Fatalf("OpenAPI v2 does not require expects_reply: %v", requiredNames)
	}
}

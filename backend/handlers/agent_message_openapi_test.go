// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
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
	responseSchemaRef := func(path, method, status string) string {
		t.Helper()
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing agent message contract path %s", path)
		}
		operation, ok := pathItem[method].(map[string]any)
		if !ok {
			t.Fatalf("missing %s operation for %s", method, path)
		}
		response := operation["responses"].(map[string]any)[status].(map[string]any)
		schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		return schema["$ref"].(string)
	}
	readContracts := []struct {
		path, method, status, schema string
	}{
		{"/api/projects/{id}/messages", "post", "201", "#/components/schemas/AgentMessageV1"},
		{"/api/v2/projects/{id}/messages", "post", "201", "#/components/schemas/AgentMessageV2"},
		{"/api/projects/{id}/messages", "get", "200", "#/components/schemas/AgentMessageListV1"},
		{"/api/v2/projects/{id}/messages", "get", "200", "#/components/schemas/AgentMessageListV2"},
		{"/api/projects/{id}/messages/listen", "get", "200", "#/components/schemas/AgentMessageInboxV1"},
		{"/api/v2/projects/{id}/messages/listen", "get", "200", "#/components/schemas/AgentMessageInboxV2"},
		{"/api/projects/{id}/messages/{messageID}", "get", "200", "#/components/schemas/AgentMessageV1"},
		{"/api/v2/projects/{id}/messages/{messageID}", "get", "200", "#/components/schemas/AgentMessageV2"},
		{"/api/issues/{id}/messages", "get", "200", "#/components/schemas/AgentMessageIssueListV1"},
		{"/api/v2/issues/{id}/messages", "get", "200", "#/components/schemas/AgentMessageIssueListV2"},
	}
	for _, contract := range readContracts {
		if got := responseSchemaRef(contract.path, contract.method, contract.status); got != contract.schema {
			t.Fatalf("%s %s response schema = %q, want %q", contract.method, contract.path, got, contract.schema)
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
	for wrapper, envelope := range map[string]string{
		"AgentMessageListV1":      "AgentMessageV1",
		"AgentMessageListV2":      "AgentMessageV2",
		"AgentMessageInboxV1":     "AgentMessageV1",
		"AgentMessageInboxV2":     "AgentMessageV2",
		"AgentMessageIssueListV1": "AgentMessageV1",
		"AgentMessageIssueListV2": "AgentMessageV2",
	} {
		schema := schemas[wrapper].(map[string]any)
		var items map[string]any
		if schema["type"] == "array" {
			items = schema["items"].(map[string]any)
		} else {
			if closed, ok := schema["additionalProperties"].(bool); !ok || closed {
				t.Fatalf("wrapper schema %s is not closed", wrapper)
			}
			items = schema["properties"].(map[string]any)["messages"].(map[string]any)["items"].(map[string]any)
		}
		if got, want := items["$ref"], "#/components/schemas/"+envelope; got != want {
			t.Fatalf("wrapper schema %s items = %v, want %s", wrapper, got, want)
		}
	}
	resolution := paths["/api/projects/{id}/messages/{messageID}/resolution"].(map[string]any)["post"].(map[string]any)
	security := resolution["security"].([]any)
	if len(security) != 1 || security[0].(map[string]any)["sessionCookie"] == nil {
		t.Fatalf("human resolution OpenAPI is not session-only: %v", security)
	}
	description := resolution["description"].(string)
	for _, requiredPhrase := range []string{"API-key", "does not release", "idempotency", "concealed 404"} {
		if !strings.Contains(description, requiredPhrase) {
			t.Fatalf("human resolution OpenAPI omits %q semantics: %s", requiredPhrase, description)
		}
	}
}

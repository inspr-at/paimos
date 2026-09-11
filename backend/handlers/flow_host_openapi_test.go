// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestFlowHostOpenAPIDocumentsReadAndIntentRoutes(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	paths := document["paths"].(map[string]any)
	statePath := "/api/projects/{id}/baseline-batches/flow-state"
	intentPath := "/api/projects/{id}/baseline-batches/flow-intents"
	if _, ok := paths[statePath].(map[string]any)["get"]; !ok {
		t.Fatalf("missing GET %s", statePath)
	}
	post := paths[intentPath].(map[string]any)["post"].(map[string]any)
	description := post["description"].(string)
	for _, phrase := range []string{
		"?tab=overview",
	} {
		if !strings.Contains(description, phrase) {
			t.Fatalf("flow-intents OpenAPI omits %q", phrase)
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	identity := schemas["FlowHostIdentityContext"].(map[string]any)
	if identity["additionalProperties"] != false {
		t.Fatal("identity context must close additional properties")
	}
	props := identity["properties"].(map[string]any)
	if _, ok := props["issuer_descriptor"]; ok {
		t.Fatal("Paimos local_host identity must not document an issuer descriptor")
	}
	kind := props["principal_kind"].(map[string]any)
	enums, _ := kind["enum"].([]any)
	if len(enums) != 1 || enums[0] != "local_host" {
		t.Fatalf("principal_kind=%v", kind)
	}
}

func TestFlowHostOpenAPIIntentResultNeverExecutes(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	result := document["components"].(map[string]any)["schemas"].(map[string]any)["FlowHostIntentResult"].(map[string]any)
	executed := result["properties"].(map[string]any)["executed"].(map[string]any)
	if executed["const"] != false {
		t.Fatalf("executed const=%v", executed["const"])
	}
}

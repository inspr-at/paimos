package handlers

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLifecycleOpenAPIClosedContract(t *testing.T) {
	raw, e := os.ReadFile("openapi.json")
	if e != nil {
		t.Fatal(e)
	}
	var d map[string]any
	if json.Unmarshal(raw, &d) != nil {
		t.Fatal("invalid OpenAPI")
	}
	schemas := d["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"LifecycleRequestV1", "LifecycleIntentV1", "LifecycleRuntimeV1", "LifecycleTransitionV1", "LifecycleEventV1", "LifecycleRuntimeRegistrationV1", "LifecycleSessionRegistrationV1", "LifecycleSessionProjectionV1", "LifecycleAccountChoiceV2", "RuntimeHealthPageV1", "RuntimeHealthStatusV1", "RuntimeLayerHealthV1"} {
		schema, ok := schemas[name].(map[string]any)
		if !ok || schema["additionalProperties"] != false {
			t.Fatalf("schema %s not closed", name)
		}
	}
	request := schemas["LifecycleRequestV1"].(map[string]any)
	if len(request["oneOf"].([]any)) != 5 {
		t.Fatal("missing operation-specific field restrictions")
	}
	required := map[string]bool{}
	for _, field := range request["required"].([]any) {
		required[field.(string)] = true
	}
	if required["account_key"] {
		t.Fatal("account_key must stay optional for class-only clients")
	}
	if _, ok := request["properties"].(map[string]any)["account_key"]; !ok {
		t.Fatal("named account_key missing from request contract")
	}
	registration := schemas["LifecycleRuntimeRegistrationV1"].(map[string]any)
	regRequired := map[string]bool{}
	for _, field := range registration["required"].([]any) {
		regRequired[field.(string)] = true
	}
	if regRequired["accounts"] || regRequired["schema_version"] {
		t.Fatal("v1 registration required fields silently gained account choices")
	}
	if _, ok := registration["properties"].(map[string]any)["accounts"]; !ok {
		t.Fatal("registration accounts advertisement missing")
	}
	intent := schemas["LifecycleIntentV1"].(map[string]any)
	version := intent["properties"].(map[string]any)["schema_version"].(map[string]any)["enum"].([]any)
	if len(version) != 2 || version[0] != float64(1) || version[1] != float64(2) {
		t.Fatalf("intent schema_version=%v", version)
	}
	paths := d["paths"].(map[string]any)
	for _, suffix := range []string{"/runtime-health", "/runtimes", "/runtimes/{runtimeID}/sessions", "/runtimes/{runtimeID}/claim", "/intents", "/intents/{intentID}", "/intents/{intentID}/events", "/intents/{intentID}/cancel", "/intents/{intentID}/transition"} {
		path, ok := paths["/api/projects/{id}/lifecycle/v1"+suffix].(map[string]any)
		if !ok {
			t.Fatalf("missing route %s", suffix)
		}
		for _, value := range path {
			op := value.(map[string]any)
			if len(op["security"].([]any)) != 1 {
				t.Fatal("mixed browser/reporter security")
			}
			for _, r := range op["responses"].(map[string]any) {
				headers := r.(map[string]any)["headers"].(map[string]any)
				if headers["Cache-Control"] == nil {
					t.Fatal("missing no-store")
				}
			}
		}
	}
}

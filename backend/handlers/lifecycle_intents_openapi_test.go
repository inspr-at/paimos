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
	for _, name := range []string{"LifecycleRequestV1", "LifecycleIntentV1", "LifecycleRuntimeV1", "LifecycleTransitionV1", "LifecycleEventV1", "LifecycleRuntimeRegistrationV1", "LifecycleSessionRegistrationV1", "LifecycleSessionProjectionV1", "RuntimeHealthPageV1", "RuntimeHealthStatusV1", "RuntimeLayerHealthV1"} {
		schema, ok := schemas[name].(map[string]any)
		if !ok || schema["additionalProperties"] != false {
			t.Fatalf("schema %s not closed", name)
		}
	}
	request := schemas["LifecycleRequestV1"].(map[string]any)
	if len(request["oneOf"].([]any)) != 5 {
		t.Fatal("missing operation-specific field restrictions")
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

package handlers

import (
	"encoding/json"
	"os"
	"testing"
)

func TestReleaseAcceptanceOpenAPIIsSessionOnly(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{
		"/api/projects/{id}/release-records/{releaseID}/acceptance/confirm",
		"/api/projects/{id}/release-records/{releaseID}/acceptance/email/authorize-send",
		"/api/projects/{id}/baseline-batches/batches/{batchID}/release-record",
	} {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing path %s", path)
		}
		post, ok := item["post"].(map[string]any)
		if !ok {
			t.Fatalf("%s is not POST", path)
		}
		security, ok := post["security"].([]any)
		if !ok || len(security) != 1 {
			t.Fatalf("%s security=%v", path, post["security"])
		}
		if _, ok := security[0].(map[string]any)["sessionCookie"]; !ok {
			t.Fatalf("%s must be session-only: %v", path, security)
		}
		if _, ok := security[0].(map[string]any)["bearerAPIKey"]; ok {
			t.Fatalf("%s must not accept API keys", path)
		}
	}
}

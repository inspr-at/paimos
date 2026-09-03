// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"encoding/json"
	"os"
	"testing"
)

func TestWorkerFleetOpenAPIIsClosedVersionedAndBounded(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	schema := components["WorkerFleetSnapshotV1"].(map[string]any)
	if schema["additionalProperties"] != false {
		t.Fatal("worker fleet top-level schema is open")
	}
	properties := schema["properties"].(map[string]any)
	if properties["schema_version"].(map[string]any)["const"] != float64(1) ||
		properties["sample_limit"].(map[string]any)["maximum"] != float64(100) ||
		properties["workers"].(map[string]any)["maxItems"] != float64(100) {
		t.Fatalf("worker fleet version/bound drift: %+v", properties)
	}
	worker := properties["workers"].(map[string]any)["items"].(map[string]any)
	if worker["additionalProperties"] != false {
		t.Fatal("worker fleet node schema is open")
	}
	workerProperties := worker["properties"].(map[string]any)
	communications := workerProperties["recent_communication"].(map[string]any)
	if communications["maxItems"] != float64(4) {
		t.Fatalf("communication bound=%v", communications["maxItems"])
	}
	communicationProperties := communications["items"].(map[string]any)["properties"].(map[string]any)
	if communicationProperties["attribution"].(map[string]any)["const"] != "project_agent" {
		t.Fatal("communication attribution contract drifted")
	}
	provenance := properties["provenance"].(map[string]any)["properties"].(map[string]any)
	if provenance["terminal_generations_per_agent"].(map[string]any)["const"] != float64(1) {
		t.Fatal("terminal history bound is not explicit")
	}
	for _, forbidden := range []string{"body", "parts", "metadata", "target_ref", "host", "message_target_id"} {
		if _, exists := workerProperties[forbidden]; exists {
			t.Fatalf("private worker property %q entered public schema", forbidden)
		}
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/agent-mode/worker-fleet/v1", "/api/agent-mode/projects/{projectID}/worker-fleet/v1"} {
		if _, exists := paths[path]; !exists {
			t.Fatalf("OpenAPI path %s missing", path)
		}
	}
}

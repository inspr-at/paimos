// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWorkerFleetOpenAPIPreservesV1AndClosesV2(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	v1 := components["WorkerFleetSnapshotV1"].(map[string]any)
	v2 := components["WorkerFleetSnapshotV2"].(map[string]any)
	assertFleetBounds := func(name string, schema map[string]any, version float64) map[string]any {
		t.Helper()
		if schema["additionalProperties"] != false {
			t.Fatalf("%s top-level schema is open", name)
		}
		properties := schema["properties"].(map[string]any)
		if properties["schema_version"].(map[string]any)["const"] != version ||
			properties["sample_limit"].(map[string]any)["maximum"] != float64(100) ||
			properties["workers"].(map[string]any)["maxItems"] != float64(100) {
			t.Fatalf("%s version/bound drift: %+v", name, properties)
		}
		provenance := properties["provenance"].(map[string]any)["properties"].(map[string]any)
		if provenance["projection_version"].(map[string]any)["const"] != version ||
			provenance["terminal_generations_per_agent"].(map[string]any)["const"] != float64(1) {
			t.Fatalf("%s provenance version/bound drift: %+v", name, provenance)
		}
		worker := properties["workers"].(map[string]any)["items"].(map[string]any)
		if worker["additionalProperties"] != false {
			t.Fatalf("%s worker schema is open", name)
		}
		return worker
	}
	v1Worker := assertFleetBounds("v1", v1, 1)
	v1Properties := v1Worker["properties"].(map[string]any)
	for _, v2Only := range []string{"machine_id", "workspace_provenance", "dispatch_profile", "account_label", "runtime_provenance_trust", "work_shape", "work_contract"} {
		if _, exists := v1Properties[v2Only]; exists {
			t.Fatalf("v2-only field %q entered frozen v1 schema", v2Only)
		}
	}

	v2Worker := assertFleetBounds("v2", v2, 2)
	v2Properties := v2Worker["properties"].(map[string]any)
	required := map[string]bool{}
	for _, field := range v2Worker["required"].([]any) {
		required[field.(string)] = true
	}
	for _, field := range []string{"machine_id", "workspace_provenance", "dispatch_profile", "account_label", "management_mode", "runtime_provenance_trust", "work_shape", "work_contract"} {
		if !required[field] {
			t.Fatalf("v2 worker field %q is optional", field)
		}
	}
	shape := v2Properties["work_shape"].(map[string]any)["enum"].([]any)
	if len(shape) != 3 || shape[0] != "unknown" || shape[1] != "ship" || shape[2] != "scout" ||
		v2Properties["work_contract"].(map[string]any)["$ref"] != "#/components/schemas/WorkShapeContract" {
		t.Fatalf("v2 work-shape contract drift: %+v", v2Properties)
	}
	trust := v2Properties["runtime_provenance_trust"].(map[string]any)["enum"].([]any)
	if len(trust) != 2 || trust[0] != "managed_reporter" || trust[1] != "untrusted" {
		t.Fatalf("v2 runtime trust is not closed: %v", trust)
	}
	if description, _ := v2Properties["runtime_provenance_trust"].(map[string]any)["description"].(string); !strings.Contains(description, "Untrusted rows suppress") {
		t.Fatalf("v2 runtime trust does not document suppression: %q", description)
	}
	machineTypes := v2Properties["machine_id"].(map[string]any)["type"].([]any)
	if len(machineTypes) != 2 || machineTypes[1] != "null" || v2Properties["machine_id"].(map[string]any)["maxLength"] != float64(128) {
		t.Fatalf("v2 machine suppression/bound drift: %+v", v2Properties["machine_id"])
	}
	workspaceOptions := v2Properties["workspace_provenance"].(map[string]any)["oneOf"].([]any)
	if workspaceOptions[0].(map[string]any)["$ref"] != "#/components/schemas/WorkerWorkspaceProvenance" {
		t.Fatalf("v2 workspace provenance ref=%v", workspaceOptions)
	}
	workspace := components["WorkerWorkspaceProvenance"].(map[string]any)
	if len(workspace["properties"].(map[string]any)) != 2 || workspace["additionalProperties"] != false {
		t.Fatalf("v2 workspace provenance leaked local values: %+v", workspace)
	}
	dispatch := components["HarnessDispatchProfile"].(map[string]any)["properties"].(map[string]any)
	if dispatch["model"].(map[string]any)["maxLength"] != float64(128) || len(dispatch["effort"].(map[string]any)["enum"].([]any)) != 5 {
		t.Fatalf("v2 dispatch provenance is unbounded: %+v", dispatch)
	}
	if v2Properties["recent_communication"].(map[string]any)["maxItems"] != float64(4) {
		t.Fatal("v2 communication bound drifted")
	}
	for _, forbidden := range []string{"body", "parts", "metadata", "target_ref", "host", "message_target_id"} {
		if _, exists := v2Properties[forbidden]; exists {
			t.Fatalf("private worker property %q entered v2 schema", forbidden)
		}
	}
	paths := document["paths"].(map[string]any)
	for path, schema := range map[string]string{
		"/api/agent-mode/worker-fleet/v1":                      "WorkerFleetSnapshotV1",
		"/api/agent-mode/projects/{projectID}/worker-fleet/v1": "WorkerFleetSnapshotV1",
		"/api/agent-mode/worker-fleet/v2":                      "WorkerFleetSnapshotV2",
		"/api/agent-mode/projects/{projectID}/worker-fleet/v2": "WorkerFleetSnapshotV2",
	} {
		operation, exists := paths[path]
		if !exists {
			t.Fatalf("OpenAPI path %s missing", path)
		}
		responseRef := operation.(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
		if responseRef != "#/components/schemas/"+schema {
			t.Fatalf("OpenAPI path %s response=%v, want %s", path, responseRef, schema)
		}
	}
}

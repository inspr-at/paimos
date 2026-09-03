// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSessionHomeZoomOpenAPIClosesWireContract(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	snapshot := schemas["SessionHomeZoomSnapshotV1"].(map[string]any)
	if snapshot["additionalProperties"] != false {
		t.Fatalf("snapshot is not closed: %v", snapshot["additionalProperties"])
	}
	wantRequired := map[string]bool{
		"schema_version": true, "project_id": true, "zoom": true, "band": true,
		"sample_limit": true, "sample_truncated": true, "sessions": true,
		"selected_session": true, "totals": true,
	}
	for _, field := range snapshot["required"].([]any) {
		delete(wantRequired, field.(string))
	}
	if len(wantRequired) != 0 {
		t.Fatalf("snapshot required fields missing: %v", wantRequired)
	}
	properties := snapshot["properties"].(map[string]any)
	zoom := properties["zoom"].(map[string]any)
	if zoom["pattern"] != "^[1-9][0-9]{0,63}$" || zoom["maxLength"] != float64(64) || zoom["default"] != "10" {
		t.Fatalf("zoom string contract=%v", zoom)
	}
	band := properties["band"].(map[string]any)["enum"].([]any)
	if len(band) != 4 || band[0] != "detail" || band[1] != "overview" || band[2] != "aggregate" || band[3] != "far" {
		t.Fatalf("band enum=%v", band)
	}
	if properties["sample_limit"].(map[string]any)["maximum"] != float64(100) ||
		properties["sessions"].(map[string]any)["maxItems"] != float64(100) {
		t.Fatalf("sample bound limit=%v sessions=%v", properties["sample_limit"], properties["sessions"])
	}
	selected := properties["selected_session"].(map[string]any)["oneOf"].([]any)
	if len(selected) != 2 || selected[1].(map[string]any)["type"] != "null" {
		t.Fatalf("selected nullability=%v", selected)
	}

	totals := schemas["SessionHomeZoomTotalsV1"].(map[string]any)
	if totals["additionalProperties"] != false || len(totals["required"].([]any)) != 7 {
		t.Fatalf("totals closure=%v", totals)
	}
	status := schemas["SessionHomeStatusV1"].(map[string]any)
	if status["additionalProperties"] != false || len(status["required"].([]any)) != 6 {
		t.Fatalf("session activity status closure=%v", status)
	}
	statusProperties := status["properties"].(map[string]any)
	if len(statusProperties["activity_state"].(map[string]any)["enum"].([]any)) != 4 ||
		len(statusProperties["activity_age_seconds"].(map[string]any)["type"].([]any)) != 2 {
		t.Fatalf("session activity status=%v", statusProperties)
	}
	if snapshot["properties"].(map[string]any)["schema_version"].(map[string]any)["const"] != float64(2) {
		t.Fatalf("session home zoom schema version=%v", snapshot["properties"])
	}
	rootSnapshot := schemas["SessionHomeSnapshotV1"].(map[string]any)
	if rootSnapshot["properties"].(map[string]any)["schema_version"].(map[string]any)["const"] != float64(2) {
		t.Fatalf("session home schema version=%v", rootSnapshot["properties"])
	}
	heartbeat := schemas["HarnessHeartbeat"].(map[string]any)
	if heartbeat["additionalProperties"] != false || heartbeat["properties"].(map[string]any)["activity"] == nil {
		t.Fatalf("heartbeat activity evidence=%v", heartbeat)
	}
	paths := document["paths"].(map[string]any)
	path := paths["/api/projects/{id}/session-home/zoom/v1"].(map[string]any)
	parameters := path["parameters"].([]any)
	if len(parameters) != 3 || parameters[1].(map[string]any)["name"] != "zoom" ||
		parameters[2].(map[string]any)["name"] != "selected_session_id" {
		t.Fatalf("query parameters=%v", parameters)
	}
	selectedParameter := parameters[2].(map[string]any)["schema"].(map[string]any)
	if selectedParameter["pattern"] != "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$" {
		t.Fatalf("selected canonical UUID schema=%v", selectedParameter)
	}
}

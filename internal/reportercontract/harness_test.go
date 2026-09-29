// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Status and heartbeat share HarnessSession. Callers that never send an estimate
// must keep the attention-reason enum and the required payload unchanged; ETA
// fields stay optional and eta_stale is not an attention kind.
func TestHarnessStatusAndHeartbeatContract(t *testing.T) {
	openAPI, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	current, err := Current(openAPI)
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := current["harness-session"]
	if !ok || pin.Version != HarnessSession {
		t.Fatalf("harness-session pin = %+v", pin)
	}
	var shape map[string]any
	if err := json.Unmarshal(pin.Shape, &shape); err != nil {
		t.Fatal(err)
	}
	wantOps := []string{
		"GET /projects/{projectId}/harness-sessions/{sessionId} 200",
		"POST /projects/{projectId}/harness-sessions/{sessionId}/heartbeat 200",
	}
	legacyKinds := []any{"approval", "held_action", "reply", "reply_due", "run_waiting", "session_yielded"}
	for _, op := range wantOps {
		body, ok := shape[op].(map[string]any)
		if !ok {
			t.Fatalf("missing %s in %#v", op, shape)
		}
		props, _ := body["properties"].(map[string]any)
		reasons, _ := props["attention_reasons"].(map[string]any)
		items, _ := reasons["items"].(map[string]any)
		kindProps, _ := items["properties"].(map[string]any)
		kind, _ := kindProps["kind"].(map[string]any)
		if !reflect.DeepEqual(kind["enum"], legacyKinds) {
			t.Fatalf("%s attention kinds = %#v", op, kind["enum"])
		}
		required, _ := body["required"].([]any)
		for _, field := range []string{"eta_ready_at", "eta_live_at", "progress_pct", "eta_reported_at", "eta_stale"} {
			if _, exists := props[field]; !exists {
				t.Fatalf("%s missing optional %s", op, field)
			}
			if isRequired(required, field) {
				t.Fatalf("%s requires %s", op, field)
			}
		}
	}
}

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
		watch, _ := props["watch"].(map[string]any)
		watchProps, _ := watch["properties"].(map[string]any)
		watchRequired, _ := watch["required"].([]any)
		for _, field := range []string{"mode", "process_state"} {
			if _, exists := watchProps[field]; !exists || isRequired(watchRequired, field) {
				t.Fatalf("%s watch.%s must be optional", op, field)
			}
		}
		watchMode, _ := watchProps["mode"].(map[string]any)
		if watchMode["const"] != "lease" {
			t.Fatalf("%s watch.mode must describe status-only leases", op)
		}
		watchState, _ := watchProps["state"].(map[string]any)
		if !reflect.DeepEqual(watchState["enum"], []any{"active", "detached", "unreachable"}) {
			t.Fatalf("%s changed the existing watch state enum", op)
		}
		reasons, _ := props["attention_reasons"].(map[string]any)
		items, _ := reasons["items"].(map[string]any)
		kindProps, _ := items["properties"].(map[string]any)
		kind, _ := kindProps["kind"].(map[string]any)
		if !reflect.DeepEqual(kind["enum"], legacyKinds) {
			t.Fatalf("%s attention kinds = %#v", op, kind["enum"])
		}
		required, _ := body["required"].([]any)
		for _, field := range []string{"eta_ready_at", "eta_live_at", "progress_pct", "eta_reported_at", "eta_stale", "controls", "has_vendor_session_ref"} {
			if _, exists := props[field]; !exists {
				t.Fatalf("%s missing optional %s", op, field)
			}
			if isRequired(required, field) {
				t.Fatalf("%s requires %s", op, field)
			}
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
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
		finished, _ := props["finished"].(map[string]any)
		if finished["type"] != "boolean" || !isRequired(required, "finished") {
			t.Fatalf("%s finished must be a required response boolean", op)
		}
		for _, field := range []string{"eta_ready_at", "eta_live_at", "progress_pct", "eta_reported_at", "eta_stale", "controls", "has_vendor_session_ref", "warnings"} {
			if _, exists := props[field]; !exists {
				t.Fatalf("%s missing optional %s", op, field)
			}
			if isRequired(required, field) {
				t.Fatalf("%s requires %s", op, field)
			}
		}
	}
}

// Compare the real response pin to its pre-finished shape, rather than only
// checking a regenerated pin against itself. Both operations must stay additive.
func TestHarnessFinishedResponseAdditionIsMinor(t *testing.T) {
	openAPI, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	current, err := Current(openAPI)
	if err != nil {
		t.Fatal(err)
	}
	now := current["harness-session"]
	var beforeShape map[string]any
	if err := json.Unmarshal(now.Shape, &beforeShape); err != nil {
		t.Fatal(err)
	}
	for op, value := range beforeShape {
		body := value.(map[string]any)
		props := body["properties"].(map[string]any)
		if _, exists := props["finished"]; !exists || !isRequired(body["required"], "finished") {
			t.Fatalf("%s must include required finished before comparing", op)
		}
		delete(props, "finished")
		var required []any
		for _, field := range body["required"].([]any) {
			if field != "finished" {
				required = append(required, field)
			}
		}
		body["required"] = required
	}
	before, err := json.Marshal(beforeShape)
	if err != nil {
		t.Fatal(err)
	}
	bump, err := RequiredBump(Pin{Version: "harness-session/1.6", SHA256: "before-finished", Shape: before}, now)
	if err != nil || bump != "minor" {
		t.Fatalf("finished response addition: got %q, %v; want minor", bump, err)
	}
}

// Compare pins from the real OpenAPI operations, including the request side of
// components expanded into the response. A new request requirement is breaking
// even when the same component also appears in a server response.
func TestHarnessSharedComponentAddition(t *testing.T) {
	openAPI, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, component, want      string
		required, nested, indirect bool
	}{
		{"required commit", "HarnessCommit", "major", true, false, false},
		{"required ownership", "HarnessProcessOwnership", "major", true, false, false},
		{"optional commit", "HarnessCommit", "minor", false, false, false},
		{"nested request requirement", "HarnessCommit", "major", true, true, false},
		{"referenced request body and transitive schema", "HarnessCommit", "major", true, false, true},
		{"response-only session", "HarnessSession", "minor", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := yaml.Unmarshal(openAPI, &doc); err != nil {
				t.Fatal(err)
			}
			schemas, err := child(doc, "components", "schemas")
			if err != nil {
				t.Fatal(err)
			}
			component := tc.component
			if tc.indirect {
				// This copy has no other request users, so the scanner must follow
				// both the requestBody reference and the nested schema reference.
				component = "SharedCommit"
				schemas[component] = schemas[tc.component]
				schemas["CommitEnvelope"] = map[string]any{
					"allOf": []any{map[string]any{"$ref": "#/components/schemas/" + component}},
				}
				post, err := child(doc, "paths", "/projects/{projectId}/harness-sessions/{sessionId}/heartbeat", "post")
				if err != nil {
					t.Fatal(err)
				}
				bodies, err := child(doc, "components", "requestBodies")
				if err != nil {
					t.Fatal(err)
				}
				bodies["CommitHeartbeat"] = map[string]any{"content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/CommitEnvelope"}},
				}}
				post["requestBody"] = map[string]any{"$ref": "#/components/requestBodies/CommitHeartbeat"}
				commits, err := child(schemas, "HarnessSession", "properties", "commits")
				if err != nil {
					t.Fatal(err)
				}
				commits["items"] = map[string]any{"$ref": "#/components/schemas/" + component}
			}
			body, err := child(schemas, component)
			if err != nil {
				t.Fatal(err)
			}
			if tc.nested {
				body["properties"].(map[string]any)["nested"] = map[string]any{
					"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"},
				}
				body = body["properties"].(map[string]any)["nested"].(map[string]any)
			}
			pin := func() Pin {
				t.Helper()
				raw, err := yaml.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				pins, err := Current(raw)
				if err != nil {
					t.Fatal(err)
				}
				// Exercise serialized pins as used by the contract comparison CLI.
				raw, err = json.Marshal(pins["harness-session"])
				if err != nil {
					t.Fatal(err)
				}
				var result Pin
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			before := pin()
			body["properties"].(map[string]any)["extra"] = map[string]any{"type": "string"}
			if tc.required {
				body["required"] = append(body["required"].([]any), "extra")
			}
			after := pin()
			got, err := RequiredBump(before, after)
			if err != nil || got != tc.want {
				t.Fatalf("%s addition: got %q, %v; want %s", component, got, err, tc.want)
			}
			// Historical pins lack provenance. The new pin must still classify
			// shared requirements conservatively during that transition.
			before.RequestPaths = nil
			got, err = RequiredBump(before, after)
			if err != nil || got != tc.want {
				t.Fatalf("%s legacy pin addition: got %q, %v; want %s", component, got, err, tc.want)
			}
		})
	}
}

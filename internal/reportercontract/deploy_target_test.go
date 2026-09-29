// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

// Exercise the actual producer schema. Response validation remains separate so
// optional target metadata never makes historical requests or rows invalid.
func TestDeployTargetProducerSchemas(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schemas, err := child(doc, "components", "schemas")
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(name string) *jsonschema.Resolved {
		t.Helper()
		shape, err := expand(schemas[name], schemas, map[string]bool{})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(shape)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	target := map[string]any{"hosts": []any{"edge-1"}, "service": "aeon", "change": "upgrade"}
	approval := resolve("ApprovalProposal")
	for _, scope := range []string{"journey.deploy", "stage.deploy", "nodes.read"} {
		body := map[string]any{"scope": scope, "resource_kind": "node", "resource_id": "11111111-1111-4111-8111-111111111111", "rationale": "review", "expires_at": "2999-01-01T00:00:00Z"}
		deploy := scope != "nodes.read"
		if err := approval.Validate(body); err != nil {
			t.Errorf("%s targetless request: %v", scope, err)
		}
		if deploy {
			body["target"] = target
			if err := approval.Validate(body); err != nil {
				t.Errorf("%s explicit request: %v", scope, err)
			}
		}
		if deploy {
			body["resource_kind"] = "run"
			if err := approval.Validate(body); err != nil {
				t.Error("legacy run resource rejected", err)
			}
		}
	}
	handoff := resolve("StageHandoffWrite")
	for _, operation := range []string{"deploy", "verify", "prepare", "apply"} {
		body := map[string]any{"project_node_id": "11111111-1111-4111-8111-111111111111", "release_node_id": "22222222-2222-4222-8222-222222222222", "stage": "deploy", "operation": operation, "expected_journey_revision": float64(1), "idempotency_key": "test"}
		if err := handoff.Validate(body); err != nil {
			t.Errorf("%s targetless handoff: %v", operation, err)
		}
		if operation == "deploy" {
			body["target"] = target
			if err := handoff.Validate(body); err != nil {
				t.Errorf("%s explicit handoff: %v", operation, err)
			}
		}
	}
	destination := resolve("DeployTarget")
	for _, body := range []map[string]any{
		{"service": "aeon", "change": "upgrade"},
		{"hosts": []any{}, "service": "aeon", "change": "upgrade"},
		{"environment": "", "service": "aeon", "change": "upgrade"},
	} {
		if err := destination.Validate(body); err == nil {
			t.Errorf("target without destination accepted: %+v", body)
		}
	}
	legacy := map[string]any{"id": "11111111-1111-4111-8111-111111111111", "agent_principal_id": "22222222-2222-4222-8222-222222222222", "scope": "journey.deploy", "resource_kind": "node", "rationale": "historic", "expires_at": "2026-01-01T00:00:00Z", "proposed_at": "2025-01-01T00:00:00Z", "decision": "approved", "risk": "high"}
	if err := resolve("Approval").Validate(legacy); err != nil {
		t.Fatalf("legacy approval response no longer valid: %v", err)
	}
}

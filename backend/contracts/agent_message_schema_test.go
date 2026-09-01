package contracts_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestAgentMessageV1SchemaIsValidAndClosed(t *testing.T) {
	raw, err := os.ReadFile("agent-message-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" || schema["additionalProperties"] != false {
		t.Fatalf("agent message schema identity or closed-world guard drifted: %#v", schema)
	}
}

func TestAgentMessageDeliveryWorkSchemaAcceptsReachableTargetMissing(t *testing.T) {
	raw, err := os.ReadFile("agent-message-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	fixture := struct {
		FallbackReason string `json:"fallback_reason"`
	}{FallbackReason: "target_missing"}
	allowed := schema.Properties["delivery_work"].Properties["fallback_reason"].Enum
	if !slices.Contains(allowed, fixture.FallbackReason) {
		t.Fatalf("reachable delivery_work fixture rejected: fallback_reason=%q enum=%v", fixture.FallbackReason, allowed)
	}
	want := []string{"idle", "unsupported", "policy_capped", "target_missing", "not_steerable", "transport_error"}
	for _, value := range want {
		if !slices.Contains(allowed, value) {
			t.Fatalf("canonical fallback reason %q missing from enum %v", value, allowed)
		}
	}
}

func TestAgentMessageDeliveryWorkSchemaIncludesBothOwnedAgentdAdapters(t *testing.T) {
	raw, err := os.ReadFile("agent-message-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	allowed := schema.Properties["delivery_work"].Properties["adapter"].Enum
	for _, adapter := range []string{"agentd_codex", "agentd_claude", "managed_harness"} {
		if !slices.Contains(allowed, adapter) {
			t.Fatalf("owned adapter %q missing from enum %v", adapter, allowed)
		}
	}
}

func TestAgentMessageDeliveryWorkSchemaRequiresControlScope(t *testing.T) {
	raw, err := os.ReadFile("agent-message-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Required []string `json:"required"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	required := schema.Properties["delivery_work"].Required
	for _, field := range []string{"delivery_id", "instance", "project_id", "state", "requested_level"} {
		if !slices.Contains(required, field) {
			t.Fatalf("delivery_work scope field %q missing from required=%v", field, required)
		}
	}
}

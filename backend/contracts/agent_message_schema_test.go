package contracts_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/backend/agentmessage"
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
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "bf7c051591d23a7fe0898a534384d1dcf0dec65edaaeef47c0ee3dac1e2c43e2" {
		t.Fatalf("frozen agent-message v1 bytes changed: sha256=%s", got)
	}
}

func TestAgentMessageV1SchemaRemainsFrozenBeforeReplyObligations(t *testing.T) {
	raw, err := os.ReadFile("agent-message-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(schema.Required, "expects_reply") || schema.Properties["expects_reply"].Type != "" || schema.Properties["human_resolution_outcome"].Type != "" {
		t.Fatalf("v2 reply fields leaked into frozen v1: required=%v properties=%#v", schema.Required, schema.Properties)
	}
}

func TestAgentMessageV2SchemaRequiresReplyExpectationFact(t *testing.T) {
	raw, err := os.ReadFile("agent-message-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		AdditionalProperties bool     `json:"additionalProperties"`
		Required             []string `json:"required"`
		Properties           map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties || !slices.Contains(schema.Required, "expects_reply") || schema.Properties["expects_reply"].Type != "boolean" {
		t.Fatalf("v2 expects_reply contract drifted: required=%v property=%#v", schema.Required, schema.Properties["expects_reply"])
	}
	if !slices.Equal(schema.Properties["human_resolution_outcome"].Enum, []string{"resolved", "dismissed"}) {
		t.Fatalf("v2 human resolution outcomes=%v", schema.Properties["human_resolution_outcome"].Enum)
	}
}

func TestAgentMessageVersionProjectionsMatchClosedSchemaProperties(t *testing.T) {
	envelope := agentmessage.Envelope{
		Cursor: 1, MessageID: "message", ContextID: "PAI", Role: "agent",
		Parts: []agentmessage.TextPart{{Kind: "text", Text: "body"}}, Metadata: map[string]any{},
		From: "codex:sender", To: "codex:receiver", ThreadID: "thread", Hop: 1,
		Delivered: true, ExpectsReply: true, HumanResolutionOutcome: "resolved",
		CreatedAt: "2026-09-04T00:00:00Z", DeliveryLevel: "simple", DeliveryFallback: "simple",
	}
	for _, tc := range []struct {
		name, path string
		value      any
	}{
		{name: "v1", path: "agent-message-v1.schema.json", value: envelope.V1()},
		{name: "v2", path: "agent-message-v2.schema.json", value: envelope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rawSchema, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Required   []string       `json:"required"`
				Properties map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(rawSchema, &schema); err != nil {
				t.Fatal(err)
			}
			rawValue, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(rawValue, &value); err != nil {
				t.Fatal(err)
			}
			for key := range value {
				if schema.Properties[key] == nil {
					t.Fatalf("projection emitted property outside closed schema: %s", key)
				}
			}
			for _, key := range schema.Required {
				if _, ok := value[key]; !ok {
					t.Fatalf("projection omitted required property: %s", key)
				}
			}
		})
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

package contracts_test

import (
	"encoding/json"
	"os"
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

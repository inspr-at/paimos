// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"encoding/json"
	"os"
	"testing"
)

func TestMachineNotifierOpenAPIClosesCredentialSurface(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	contracts := []struct {
		path, method, status, schema string
	}{
		{"/api/auth/machine-notifiers", "post", "201", "MachineNotifierEnrollmentResponse"},
		{"/api/machine-notifier/messages", "post", "201", "MachineNotifierMessageID"},
		{"/api/machine-notifier/messages/{messageID}/receipt", "get", "200", "MachineNotifierReceipt"},
	}
	for _, contract := range contracts {
		operation := paths[contract.path].(map[string]any)[contract.method].(map[string]any)
		response := operation["responses"].(map[string]any)[contract.status].(map[string]any)
		got := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
		if got != "#/components/schemas/"+contract.schema {
			t.Fatalf("%s %s response=%v", contract.method, contract.path, got)
		}
	}

	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{
		"MachineNotifierBinding", "MachineNotifierEnrollmentRequest", "MachineNotifierEnrollmentResponse",
		"MachineNotifierSendRequest", "MachineNotifierMessageID", "MachineNotifierReceipt",
	} {
		schema := schemas[name].(map[string]any)
		if closed, ok := schema["additionalProperties"].(bool); !ok || closed {
			t.Fatalf("schema %s is not closed", name)
		}
	}
	send := schemas["MachineNotifierSendRequest"].(map[string]any)
	if properties := send["properties"].(map[string]any); len(properties) != 1 || properties["body"] == nil {
		t.Fatalf("send request permits routing fields: %v", properties)
	}
	receipt := schemas["MachineNotifierReceipt"].(map[string]any)
	properties := receipt["properties"].(map[string]any)
	for _, forbidden := range []string{"body", "content", "sender", "metadata"} {
		if properties[forbidden] != nil {
			t.Fatalf("receipt exposes %q", forbidden)
		}
	}
}

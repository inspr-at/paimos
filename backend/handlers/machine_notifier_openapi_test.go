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
		"MachineNotifierBinding", "MachineNotifierEnrollmentRequest", "MachineNotifierEnrollmentResponse", "MachineNotifierEncryptedEnrollmentResponse",
		"MachineNotifierSendRequest", "MachineNotifierMessageID", "MachineNotifierReceipt",
	} {
		schema := schemas[name].(map[string]any)
		if closed, ok := schema["additionalProperties"].(bool); !ok || closed {
			t.Fatalf("schema %s is not closed", name)
		}
	}
	enrollmentOperation := paths["/api/auth/machine-notifiers"].(map[string]any)["post"].(map[string]any)
	response := enrollmentOperation["responses"].(map[string]any)["201"].(map[string]any)
	variants := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["oneOf"].([]any)
	wantVariants := []string{
		"#/components/schemas/MachineNotifierEnrollmentResponse",
		"#/components/schemas/MachineNotifierEncryptedEnrollmentResponse",
	}
	if len(variants) != len(wantVariants) {
		t.Fatalf("enrollment response variants=%v", variants)
	}
	for i, want := range wantVariants {
		if got := variants[i].(map[string]any)["$ref"]; got != want {
			t.Fatalf("enrollment response variant %d=%v want=%s", i, got, want)
		}
	}
	security := enrollmentOperation["security"].([]any)
	if len(security) != 1 || security[0].(map[string]any)["sessionCookie"] == nil {
		t.Fatalf("enrollment security=%v", security)
	}
	parameters := enrollmentOperation["parameters"].([]any)
	if len(parameters) != 1 || parameters[0].(map[string]any)["name"] != "X-CSRF-Token" || parameters[0].(map[string]any)["required"] != true {
		t.Fatalf("enrollment CSRF parameter=%v", parameters)
	}

	enrollmentRequest := schemas["MachineNotifierEnrollmentRequest"].(map[string]any)
	required := enrollmentRequest["required"].([]any)
	for _, field := range required {
		if field == "age_recipients" {
			t.Fatal("age_recipients must remain optional for plaintext compatibility")
		}
	}
	recipients := enrollmentRequest["properties"].(map[string]any)["age_recipients"].(map[string]any)
	if recipients["minItems"] != float64(1) || recipients["maxItems"] != float64(8) || recipients["uniqueItems"] != true {
		t.Fatalf("age recipient bounds=%v", recipients)
	}
	plaintext := schemas["MachineNotifierEnrollmentResponse"].(map[string]any)["properties"].(map[string]any)
	if plaintext["key"] == nil || plaintext["credential_delivery"] != nil || plaintext["key_age_base64"] != nil {
		t.Fatalf("plaintext enrollment response=%v", plaintext)
	}
	encrypted := schemas["MachineNotifierEncryptedEnrollmentResponse"].(map[string]any)["properties"].(map[string]any)
	if encrypted["key"] != nil || encrypted["credential_delivery"] == nil || encrypted["key_age_base64"] == nil {
		t.Fatalf("encrypted enrollment response=%v", encrypted)
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

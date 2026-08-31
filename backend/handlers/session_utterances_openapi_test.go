package handlers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSessionUtteranceOpenAPIClosesTranscriptOnlyContract(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	request := schemas["SessionUtteranceRequestV1"].(map[string]any)
	if request["additionalProperties"] != false || len(request["required"].([]any)) != 4 {
		t.Fatalf("request is not closed: %v", request)
	}
	properties := request["properties"].(map[string]any)
	if _, found := properties["audio"]; found {
		t.Fatal("raw audio appeared in the public utterance request")
	}
	if properties["utterance_id"].(map[string]any)["pattern"] != "^utt_[0-9a-f]{32}$" {
		t.Fatalf("utterance identity is not frozen: %v", properties["utterance_id"])
	}
	selected := properties["selected_session"].(map[string]any)["oneOf"].([]any)
	if len(selected) != 2 || selected[1].(map[string]any)["type"] != "null" {
		t.Fatalf("selected session is not explicitly nullable: %v", selected)
	}
	result := schemas["SessionUtteranceResultV1"].(map[string]any)
	if result["additionalProperties"] != false || len(result["required"].([]any)) != 9 {
		t.Fatalf("result is not the exact nine-field contract: %v", result)
	}
	post := document["paths"].(map[string]any)["/api/projects/{id}/session-utterances/v1"].(map[string]any)["post"].(map[string]any)
	responses := post["responses"].(map[string]any)
	for _, status := range []string{"201", "400", "401", "403", "404", "409", "413", "500"} {
		if _, found := responses[status]; !found {
			t.Fatalf("response %s is undocumented", status)
		}
	}
	payloadTooLarge := responses["413"].(map[string]any)["description"].(string)
	if !strings.Contains(payloadTooLarge, "52 KiB") || !strings.Contains(payloadTooLarge, "8,192 UTF-8 bytes") {
		t.Fatalf("wire and decoded transcript limits diverged: %q", payloadTooLarge)
	}
}

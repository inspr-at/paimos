// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"testing"
)

func TestHarnessBrowserMessageOpenAPIClosesHumanGenerationContract(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	request := schemas["HarnessBrowserMessageRequestV1"].(map[string]any)
	result := schemas["HarnessBrowserMessageResponseV1"].(map[string]any)
	if request["additionalProperties"] != false || len(request["required"].([]any)) != 5 {
		t.Fatalf("request is not closed: %v", request)
	}
	if result["additionalProperties"] != false || len(result["required"].([]any)) != 8 {
		t.Fatalf("result is not closed: %v", result)
	}
	properties := request["properties"].(map[string]any)
	if _, found := properties["agent_name"]; found {
		t.Fatal("human route accepts an agent impersonation field")
	}
	if properties["utterance_id"].(map[string]any)["pattern"] != "^utt_[0-9a-f]{32}$" {
		t.Fatal("utterance identity changed")
	}
	levels := properties["delivery_level"].(map[string]any)["enum"].([]any)
	if len(levels) != 2 || levels[0] != "simple" || levels[1] != "steer" {
		t.Fatalf("delivery levels=%v", levels)
	}
	post := document["paths"].(map[string]any)["/api/projects/{id}/harness-sessions/{sessionID}/messages/v1"].(map[string]any)["post"].(map[string]any)
	responses := post["responses"].(map[string]any)
	for _, status := range []string{"201", "400", "401", "403", "409", "503"} {
		if _, found := responses[status]; !found {
			t.Fatalf("response %s is undocumented", status)
		}
	}
}

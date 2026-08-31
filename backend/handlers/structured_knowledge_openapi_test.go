// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestStructuredKnowledgeOpenAPIClosesActivationAuthorityAndPrivacy(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	paths := document["paths"].(map[string]any)
	operations := map[string]string{
		"/api/projects/{id}/structured-knowledge/v1":                                      "get",
		"/api/projects/{id}/structured-knowledge/v1/validate":                             "post",
		"/api/projects/{id}/structured-knowledge/v1/remember":                             "post",
		"/api/projects/{id}/structured-knowledge/v1/compact":                              "put",
		"/api/projects/{id}/structured-knowledge/v1/entries":                              "post",
		"/api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/adopt":          "post",
		"/api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}":                "put",
		"/api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/links":          "post",
		"/api/projects/{id}/structured-knowledge/v1/entries/{knowledgeID}/links/{linkID}": "delete",
		"/api/structured-knowledge/v1/entries/{knowledgeID}/promote":                      "post",
	}
	for path, method := range operations {
		item, ok := paths[path].(map[string]any)
		if !ok || item[method] == nil {
			t.Fatalf("missing %s %s", method, path)
		}
		responses := item[method].(map[string]any)["responses"].(map[string]any)
		for status, rawResponse := range responses {
			response := rawResponse.(map[string]any)
			headers, ok := response["headers"].(map[string]any)
			if !ok || headers["Cache-Control"] == nil {
				t.Fatalf("%s %s response %s lacks private Cache-Control", method, path, status)
			}
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{
		"StructuredKnowledgeValidation", "StructuredKnowledgeLink", "StructuredKnowledgeEntry",
		"LegacyStructuredKnowledgeEntry", "StructuredKnowledgeProposal", "StructuredKnowledgeSnapshotV1",
		"StructuredKnowledgeCandidate", "StructuredKnowledgeCreate", "StructuredKnowledgeUpdate",
		"StructuredKnowledgeCompactBind", "StructuredKnowledgeCompactBinding", "StructuredKnowledgeAdopt",
		"StructuredKnowledgeLinkCreate", "StructuredKnowledgeLinkCreateResult", "StructuredKnowledgePromote",
		"StructuredKnowledgePromotionLinkResult", "StructuredKnowledgePromotionResult",
	} {
		schema, ok := schemas[name].(map[string]any)
		if !ok || schema["additionalProperties"] != false {
			t.Fatalf("schema %s is missing or not closed", name)
		}
	}
	entry := schemas["StructuredKnowledgeEntry"].(map[string]any)["properties"].(map[string]any)
	if entry["short_body"].(map[string]any)["maxLength"] != float64(1200) {
		t.Fatal("durable entry does not pin 1,200 bytes")
	}
	proposal := schemas["StructuredKnowledgeProposal"].(map[string]any)["properties"].(map[string]any)
	if proposal["candidate_body"].(map[string]any)["maxLength"] != float64(65536) {
		t.Fatal("proposal does not pin 64 KiB")
	}
	promote := schemas["StructuredKnowledgePromote"].(map[string]any)
	description, _ := promote["description"].(string)
	for _, phrase := range []string{"project to instance", "instance to kernel or vision", "Direct project to a terminal level is prohibited"} {
		if !strings.Contains(description, phrase) {
			t.Fatalf("promotion transition contract missing %q: %s", phrase, description)
		}
	}
}

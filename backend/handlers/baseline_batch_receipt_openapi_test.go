// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/baselinebatch"
)

func TestBaselineBatchBuiltReceiptOpenAPIClosesTypedProducer(t *testing.T) {
	raw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	path := "/api/projects/{id}/baseline-batches/batches/{batchID}/built-receipt"
	item, ok := document["paths"].(map[string]any)[path].(map[string]any)
	if !ok {
		t.Fatalf("missing path %s", path)
	}
	post, ok := item["post"].(map[string]any)
	if !ok {
		t.Fatal("built-receipt is not POST")
	}
	security, ok := post["security"].([]any)
	if !ok || len(security) != 2 {
		t.Fatalf("built-receipt security=%v", post["security"])
	}
	seenSession, seenKey := false, false
	for _, entry := range security {
		scheme := entry.(map[string]any)
		if _, ok := scheme["sessionCookie"]; ok {
			seenSession = true
		}
		if _, ok := scheme["bearerAPIKey"]; ok {
			seenKey = true
		}
	}
	if !seenSession || !seenKey {
		t.Fatalf("built-receipt security must offer session or API key: %v", security)
	}
	description := post["description"].(string)
	for _, phrase := range []string{
		"agent-controls:write",
		"Unknown and duplicate JSON fields",
		"never exposes generic ReportStage",
		"never starts deployment",
		"CSRF",
	} {
		if !strings.Contains(description, phrase) {
			t.Fatalf("built-receipt OpenAPI omits %q", phrase)
		}
	}
	requestRef := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
	if requestRef != "#/components/schemas/BaselineBatchBuiltReceiptRequest" {
		t.Fatalf("request schema ref=%s", requestRef)
	}
	responses := post["responses"].(map[string]any)
	for _, status := range []string{"200", "400", "401", "403", "404", "409", "503"} {
		if responses[status] == nil {
			t.Fatalf("response %s is undocumented", status)
		}
	}
	responseRef := responses["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
	if responseRef != "#/components/schemas/BaselineBatch" {
		t.Fatalf("200 schema ref=%s", responseRef)
	}

	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	request := schemas["BaselineBatchBuiltReceiptRequest"].(map[string]any)
	if request["additionalProperties"] != false {
		t.Fatalf("request is not closed: %v", request["additionalProperties"])
	}
	batch := schemas["BaselineBatch"].(map[string]any)
	if batch["additionalProperties"] != false {
		t.Fatalf("response batch is not closed: %v", batch["additionalProperties"])
	}
	properties := request["properties"].(map[string]any)
	required := map[string]bool{}
	for _, name := range request["required"].([]any) {
		required[name.(string)] = true
	}
	wantFields, wantRequired := builtReceiptJSONFields()
	if len(properties) != len(wantFields) {
		t.Fatalf("request properties=%d want %d", len(properties), len(wantFields))
	}
	for name := range wantFields {
		if properties[name] == nil {
			t.Fatalf("request missing field %s", name)
		}
		if wantRequired[name] != required[name] {
			t.Fatalf("required mismatch for %s: schema=%v go=%v", name, required[name], wantRequired[name])
		}
	}
	for _, forbidden := range []string{"evidence", "stage_key", "kind", "deployment", "verification", "handoff", "expected_current_execution"} {
		if properties[forbidden] != nil {
			t.Fatalf("request leaked generic field %s", forbidden)
		}
	}
	scheme := properties["version_scheme"].(map[string]any)["enum"].([]any)
	if len(scheme) != 2 || scheme[0] != "legacy" || scheme[1] != "inspr-calendar-v1" {
		t.Fatalf("version_scheme enum=%v", scheme)
	}
	cas, ok := request["oneOf"].([]any)
	if !ok || len(cas) != 2 {
		t.Fatalf("implementation CAS branches=%v", request["oneOf"])
	}
	zero := cas[0].(map[string]any)["properties"].(map[string]any)
	positive := cas[1].(map[string]any)["properties"].(map[string]any)
	if zero["expected_implementation_execution"].(map[string]any)["const"] != float64(0) ||
		zero["expected_implementation_authority_epoch"].(map[string]any)["const"] != float64(0) ||
		positive["expected_implementation_execution"].(map[string]any)["minimum"] != float64(1) ||
		positive["expected_implementation_authority_epoch"].(map[string]any)["minimum"] != float64(1) {
		t.Fatalf("implementation CAS pairing zero=%v positive=%v", zero, positive)
	}

	implement := document["paths"].(map[string]any)["/api/issues/{id}/implement"].(map[string]any)["post"].(map[string]any)
	implementResponses := implement["responses"].(map[string]any)
	conflict := implementResponses["409"].(map[string]any)["description"].(string)
	if !strings.Contains(conflict, "baseline-owned") || !strings.Contains(conflict, "fresh issue") {
		t.Fatalf("implement 409 does not document historical seal: %s", conflict)
	}
	if implementResponses["500"] == nil {
		t.Fatal("implement 500 lookup failure is undocumented")
	}
}

func builtReceiptJSONFields() (fields map[string]bool, required map[string]bool) {
	fields, required = map[string]bool{}, map[string]bool{}
	rt := reflect.TypeOf(baselinebatch.BuiltReceiptRequest{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = true
		if !strings.Contains(opts, "omitempty") {
			required[name] = true
		}
	}
	return fields, required
}

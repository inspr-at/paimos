// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFieldSchema(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"required":["points"],
		"additionalProperties":false,
		"properties":{"points":{"type":"integer","minimum":0},"name":{"type":"string","minLength":1,"pattern":"^[a-z]+$"}}
	}`)
	schema, err := compileSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFields(schema, json.RawMessage(`{"points":1,"name":"ab"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFields(schema, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing required: %v", err)
	}
	if _, err := validateFields(schema, json.RawMessage(`{"points":-1}`)); err == nil {
		t.Fatal("negative integer accepted")
	}
	if _, err := validateFields(schema, json.RawMessage(`{"points":1,"extra":true}`)); err == nil {
		t.Fatal("extra property accepted")
	}
	if _, err := validateFields(schema, json.RawMessage(`[]`)); err == nil {
		t.Fatal("array accepted")
	}
	if _, err := compileSchema(json.RawMessage(`{"$ref":"#/oops"}`)); err == nil {
		t.Fatal("$ref accepted")
	}
	if _, err := compileSchema(json.RawMessage(`{"exclusiveMinimum":true}`)); err == nil {
		t.Fatal("boolean exclusiveMinimum accepted")
	}
	open, err := compileSchema(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFields(open, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := compileSchema(json.RawMessage(`{"type":"object","issue_family":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := compileSchema(json.RawMessage(`{"issue_family":"yes"}`)); err == nil || !strings.Contains(err.Error(), "issue_family") {
		t.Fatalf("issue_family: %v", err)
	}
}

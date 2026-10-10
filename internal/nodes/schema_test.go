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

func TestFieldSchemaExactNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, schema, fields string
		valid                bool
	}{
		{"large integer", `{"properties":{"precise":{"type":"integer","exclusiveMinimum":9007199254740992}}}`, `{"precise":9007199254740993}`, true},
		{"precise decimal", `{"properties":{"precise":{"exclusiveMaximum":0.10000000000000000002}}}`, `{"precise":0.10000000000000000001}`, true},
		{"fraction", `{"properties":{"precise":{"type":"integer"}}}`, `{"precise":1.00000000000000000001}`, false},
		{"const", `{"properties":{"precise":{"const":9007199254740992}}}`, `{"precise":9007199254740993}`, false},
		{"enum", `{"properties":{"precise":{"enum":[0.10000000000000000001]}}}`, `{"precise":0.10000000000000000002}`, false},
		{"nested valid", `{"properties":{"nested":{"items":{"properties":{"precise":{"exclusiveMinimum":9007199254740992}}}}}}`, `{"nested":[{"precise":9007199254740993}]}`, true},
		{"nested invalid", `{"properties":{"nested":{"items":{"properties":{"precise":{"minimum":9007199254740993}}}}}}`, `{"nested":[{"precise":9007199254740992}]}`, false},
		{"additional valid", `{"additionalProperties":{"exclusiveMaximum":0.10000000000000000002}}`, `{"precise":0.10000000000000000001}`, true},
		{"additional invalid", `{"additionalProperties":{"maximum":0.10000000000000000001}}`, `{"precise":0.10000000000000000002}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := compileSchema(json.RawMessage(tc.schema))
			if err != nil {
				t.Fatal(err)
			}
			stored, err := validateFields(schema, json.RawMessage(tc.fields))
			if tc.valid {
				if err != nil || string(stored) != tc.fields {
					t.Fatalf("exact field preservation: stored=%s err=%v", stored, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "fields do not match the kind schema") {
				t.Fatalf("exact schema rejection: %v", err)
			}
		})
	}
}

func TestFitKindFieldsExactNumbers(t *testing.T) {
	schema, err := compileSchema(json.RawMessage(`{"required":["precise"],"additionalProperties":false,"properties":{"precise":{"type":"integer","exclusiveMinimum":9007199254740992}}}`))
	if err != nil {
		t.Fatal(err)
	}
	stored, dropped, violations, err := fitKindFields(schema, json.RawMessage(`{"precise":9007199254740993,"scratch":"drop"}`))
	if err != nil || string(stored) != `{"precise":9007199254740993}` || len(dropped) != 1 || dropped[0] != "scratch" || len(violations) != 0 {
		t.Fatalf("conversion preview: stored=%s dropped=%v violations=%v err=%v", stored, dropped, violations, err)
	}
	for _, fields := range []string{`{"scratch":"drop"}`, `{"precise":9007199254740992}`} {
		_, _, violations, err := fitKindFields(schema, json.RawMessage(fields))
		if err != nil || len(violations) != 1 || violations[0] != "precise" {
			t.Fatalf("conversion violation for %s: violations=%v err=%v", fields, violations, err)
		}
	}
}

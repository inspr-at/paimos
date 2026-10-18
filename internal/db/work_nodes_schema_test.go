// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeWorkSchemas(t *testing.T) {
	raw, err := mergeWorkSchemas(map[string]json.RawMessage{
		"epic":   json.RawMessage(`{"type":"object","properties":{"planned":{"type":"number"}},"states":[{"state":"custom","category":"open"}]}`),
		"ticket": json.RawMessage(`{"type":"object","properties":{"detail":{"type":"string"}},"states":[{"state":"review","category":"doing"}]}`),
		"task":   json.RawMessage(`{"type":"object","properties":{"detail":{"type":"string"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema["properties"].(map[string]any)) != 2 || len(schema["states"].([]any)) != 2 || schema["issue_family"] != true {
		t.Fatalf("merged schema %s", raw)
	}
	for _, tc := range []struct{ name, a, b, want string }{
		{"property", `{"properties":{"custom":{"type":"string"}}}`, `{"properties":{"custom":{"type":"number"}}}`, "incompatible property"},
		{"required", `{"required":["custom"]}`, `{}`, "incompatible required"},
		{"constraint", `{"additionalProperties":false}`, `{"additionalProperties":true}`, "incompatible schema constraint"},
		{"state-normalization", `{"states":[{"state":"needs--  review","category":"open"}]}`, `{"states":[{"state":"needs_review","category":"done"}]}`, "incompatible state"},
		{"state", `{"states":[{"state":"review","category":"open"}]}`, `{"states":[{"state":"review","category":"done"}]}`, "incompatible state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mergeWorkSchemas(map[string]json.RawMessage{"epic": json.RawMessage(tc.a), "task": json.RawMessage(tc.b)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

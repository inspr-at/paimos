// SPDX-License-Identifier: AGPL-3.0-only
package reportercontract

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

func TestAccountLimitRequestAndResponseSchemas(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schemas, err := child(doc, "components", "schemas")
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(name string) *jsonschema.Resolved {
		t.Helper()
		shape, err := expand(schemas[name], schemas, map[string]bool{})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(shape)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	write := resolve("AccountLimitWrite")
	read := resolve("AccountLimitRule")
	body := map[string]any{"amount": float64(20), "unit": "percent", "period": "day"}
	if err = write.Validate(body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "account_id", "created_at", "pace_model"} {
		body[field] = "extra"
		if write.Validate(body) == nil {
			t.Fatalf("write accepts %s", field)
		}
		delete(body, field)
	}
	body["id"] = "11111111-1111-4111-8111-111111111111"
	body["account_id"] = "22222222-2222-4222-8222-222222222222"
	body["created_at"] = "2026-09-29T12:00:00Z"
	if err = read.Validate(body); err != nil {
		t.Fatalf("response rejected: %v", err)
	}
	delete(body, "account_id")
	if read.Validate(body) == nil {
		t.Fatal("response accepts missing account")
	}
	body["amount"] = float64(0)
	if read.Validate(body) == nil {
		t.Fatal("response accepts invalid amount")
	}
}

// Retirement keeps the routes and read schemas while publishing stable codes
// for older clients to distinguish a retired action from invalid input.
func TestAccountRetirementResponseSchemas(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method, status, code string
	}{
		{"/agent-accounts/{accountId}/limit", "put", "410", "limit_retired"},
		{"/agent-accounts/{accountId}/windows/{windowId}/repeat", "post", "410", "limit_retired"},
		{"/agent-accounts/capacity/schedule", "put", "400", "override_retired"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			op, err := child(doc, "paths", tc.path, tc.method)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == "410" && op["deprecated"] != true {
				t.Fatal("retired limit operation is not marked deprecated")
			}
			shape, err := child(op, "responses", tc.status, "content", "application/json", "schema")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(shape)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"error": "This action is retired.", "code": tc.code}
			if err := resolved.Validate(body); err != nil {
				t.Fatal(err)
			}
			body["code"] = "wrong_code"
			if resolved.Validate(body) == nil {
				t.Fatal("retirement schema accepts the wrong code")
			}
			delete(body, "code")
			if tc.status == "410" && resolved.Validate(body) == nil {
				t.Fatal("410 schema accepts a missing retirement code")
			}
			if tc.status == "400" && resolved.Validate(body) != nil {
				t.Fatal("400 schema rejects ordinary schedule validation errors")
			}
			delete(body, "error")
			if resolved.Validate(body) == nil {
				t.Fatal("retirement schema accepts a missing error")
			}
		})
	}
	if _, err := child(doc, "paths", "/agent-accounts/{accountId}/limit", "delete", "responses", "204"); err != nil {
		t.Fatal("limit removal contract was lost:", err)
	}
}

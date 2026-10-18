// SPDX-License-Identifier: AGPL-3.0-only
package reportercontract

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// Risk: the series consumer is given an unbounded history, ambiguous timestamps
// or quota values whose required provenance/unknown semantics drift.
func TestAccountQuotaSeriesReadContract(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	route, err := child(doc, "paths", "/agent-accounts/{accountId}/quota-series", "get")
	if err != nil || route["operationId"] != "getAccountQuotaSeries" {
		t.Fatal("missing quota read route", err)
	}
	response, err := child(route, "responses", "200", "content", "application/json", "schema")
	if err != nil || response["$ref"] != "#/components/schemas/AccountQuotaSeries" {
		t.Fatal("wrong quota response", err)
	}
	for name, fields := range map[string][]any{
		"AccountQuotaSeries":  {"account_id", "latest_readings", "hourly_readings", "next_cursor"},
		"AccountQuotaHour":    {"hour_at", "readings"},
		"AccountQuotaReading": {"window_kind", "bucket", "used_percent", "remaining_percent", "read_at", "resets_at", "source", "reading_age_seconds", "freshness"},
	} {
		schema, err := child(doc, "components", "schemas", name)
		if err != nil || !reflect.DeepEqual(schema["required"], fields) || schema["additionalProperties"] != false {
			t.Fatalf("quota schema %s drifted: %v", name, err)
		}
	}
	params, ok := route["parameters"].([]any)
	if !ok {
		t.Fatal("missing bounded query")
	}
	seen := map[string]map[string]any{}
	for _, v := range params {
		p := v.(map[string]any)
		seen[p["name"].(string)] = p
	}
	for _, key := range []string{"from", "to"} {
		if seen[key]["required"] != true {
			t.Fatalf("%s must bound the series", key)
		}
	}
	limit := seen["limit"]["schema"].(map[string]any)
	if limit["maximum"] != 168 || limit["minimum"] != 1 || seen["cursor"] == nil {
		t.Fatal("missing keyset page bound")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package dsar

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestQuarantineInventoryParsesWithWorkflowColumn pins origin/main's array
// schema. An object-member quarantine entry makes the whole file invalid JSON.
func TestQuarantineInventoryParsesWithWorkflowColumn(t *testing.T) {
	raw, err := os.ReadFile("inventory.json")
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	type locator struct {
		TenantColumn string `json:"tenant_column"`
		PersonColumn string `json:"person_column"`
	}
	type table struct {
		Table          string            `json:"table"`
		Classification string            `json:"classification"`
		Locator        locator           `json:"locator"`
		Columns        map[string]string `json:"columns"`
	}
	var doc struct {
		License string  `json:"_license"`
		Version int     `json:"version"`
		Tables  []table `json:"tables"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("inventory schema: %v", err)
	}
	if dec.More() {
		t.Fatal("inventory has trailing data")
	}
	if doc.Version != 1 || doc.License == "" {
		t.Fatalf("inventory header: version %d", doc.Version)
	}
	allowed := map[string]bool{"personal": true, "metadata": true, "secret": true, "review": true}
	var quarantine *table
	seen := map[string]int{}
	for i := range doc.Tables {
		entry := &doc.Tables[i]
		seen[entry.Table]++
		if entry.Table == "" || !allowed[entry.Classification] || entry.Locator.TenantColumn == "" {
			t.Fatalf("table entry %q is not a classified array element", entry.Table)
		}
		for column, class := range entry.Columns {
			if column == "" || !allowed[class] {
				t.Fatalf("%s.%s classification %q", entry.Table, column, class)
			}
		}
		if entry.Table == "delivery_queue_failures" {
			quarantine = entry
		}
	}
	if seen["delivery_queue_failures"] != 1 || quarantine == nil {
		t.Fatalf("delivery_queue_failures count = %d", seen["delivery_queue_failures"])
	}
	want := map[string]string{
		"tenant_id":    "metadata",
		"repository":   "metadata",
		"pull_request": "metadata",
		"head_sha":     "metadata",
		"check_name":   "metadata",
		"workflow":     "metadata",
		"conclusion":   "metadata",
		"kind":         "metadata",
		"check_run_id": "metadata",
		"at":           "metadata",
	}
	if quarantine.Classification != "metadata" || quarantine.Locator.TenantColumn != "tenant_id" || quarantine.Locator.PersonColumn != "" || !reflect.DeepEqual(quarantine.Columns, want) {
		t.Fatalf("quarantine entry = %+v", quarantine)
	}
}

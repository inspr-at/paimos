// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"gopkg.in/yaml.v3"
)

func TestRulesReceiptOpenAPIAndRoutePermissionsAgree(t *testing.T) {
	read := func(path string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err = yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	api, module := read("../../api/openapi.yaml"), read("openapi.yaml")
	path := "/projects/{projectId}/harness-sessions/{sessionId}/rules-receipts"
	want := api["paths"].(map[string]any)[path]
	got := module["paths"].(map[string]any)["/api"+path]
	if want == nil || !reflect.DeepEqual(want, got) {
		t.Fatal("receipt OpenAPI route contracts diverge")
	}
	for _, name := range []string{"HarnessRulesReceiptContext", "HarnessRulesReceiptWrite", "HarnessRulesReceipt", "HarnessRulesReceiptRecorded", "HarnessRulesReceiptPage"} {
		want := api["components"].(map[string]any)["schemas"].(map[string]any)[name]
		got := module["components"].(map[string]any)["schemas"].(map[string]any)[name]
		if want == nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("schema %s diverges", name)
		}
		if name == "HarnessRulesReceiptWrite" {
			properties := want.(map[string]any)["properties"].(map[string]any)
			if maximum := properties["byte_size"].(map[string]any)["maximum"]; maximum != 512000 {
				t.Fatalf("receipt contract ceiling: %v", maximum)
			}
		}
	}
	for method, permission := range map[string]string{"GET": "harness.read", "POST": "harness.worker"} {
		if got, ok := authz.PermissionForPattern(method + " /api" + path); !ok || got != permission {
			t.Fatalf("%s permission %q, declared=%v", method, got, ok)
		}
	}
}

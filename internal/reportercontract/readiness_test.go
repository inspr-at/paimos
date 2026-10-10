// SPDX-License-Identifier: AGPL-3.0-only
package reportercontract

import (
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFix3ReadinessPackageComment(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../agentaccounts/doc.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if file.Doc == nil || !strings.HasPrefix(file.Doc.Text(), "Package agentaccounts ") {
		t.Fatal("agentaccounts package documentation is detached from its package declaration")
	}
}

func TestFix3AvailabilitySchemaEnums(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, property string
		items          bool
		want           []any
	}{
		{"CapacityWait", "code", false, []any{"schedule", "reserve", "reading", "vendor", "offline", "sign_in", "hold", "approval", "capacity", "allowance", "models", "state", "residency", "context", "daily_limit", "daily_limit_unknown"}},
		{"AccountCatalogChoice", "unavailable_reasons", true, []any{"state", "probe", "capacity", "allowance", "models"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := []string{"components", "schemas", test.name, "properties", test.property}
			if test.items {
				path = append(path, "items")
			}
			property, err := child(doc, path...)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(property["enum"], test.want) {
				t.Fatalf("availability-only schema must retain its emitted vocabulary: got %v, want %v", property["enum"], test.want)
			}
		})
	}
}

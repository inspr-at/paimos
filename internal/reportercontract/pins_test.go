// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResponseSchemaPins(t *testing.T) {
	openAPI, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	current, err := Current(openAPI)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]Pin
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	if len(pins) != len(current) {
		t.Fatalf("reporter contract pins: got %d, want %d surfaces", len(pins), len(current))
	}
	for name, now := range current {
		previous, ok := pins[name]
		if !ok {
			t.Errorf("%s: missing response schema pin", name)
			continue
		}
		bump, err := RequiredBump(previous, now)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if bump != "none" {
			t.Errorf("%s: response schema changed (%s change); bump %s in Aeon-Contract and update pins.json. Previous SHA256 %s, current %s", name, bump, bump, previous.SHA256, now.SHA256)
			continue
		}
		if previous.Version != now.Version {
			t.Errorf("%s: pinned version %s differs from declared %s; update pins.json", name, previous.Version, now.Version)
		}
	}
}

func TestRequiredBump(t *testing.T) {
	base := Pin{SHA256: "old", Shape: json.RawMessage(`{"a":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}`)}
	optional := Pin{SHA256: "new", Shape: json.RawMessage(`{"a":{"type":"object","properties":{"id":{"type":"string"},"note":{"type":"string"}},"required":["id"]}}`)}
	required := Pin{SHA256: "new", Shape: json.RawMessage(`{"a":{"type":"object","properties":{"id":{"type":"string"},"note":{"type":"string"}},"required":["id","note"]}}`)}
	changed := Pin{SHA256: "new", Shape: json.RawMessage(`{"a":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}}`)}
	for _, tc := range []struct {
		name string
		pin  Pin
		want string
	}{{"optional", optional, "minor"}, {"required", required, "major"}, {"changed", changed, "major"}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RequiredBump(base, tc.pin)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestRequiredBumpRequiredPropertyDirection(t *testing.T) {
	const base = `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`
	const finished = `{"type":"object","properties":{"id":{"type":"string"},"finished":{"type":"boolean"}},"required":["id","finished"]}`
	for _, tc := range []struct {
		name, before, after, responseBump string
	}{
		{"new required property", base, finished, "minor"},
		{"first required property", `{"type":"object","properties":{}}`, `{"type":"object","properties":{"finished":{"type":"boolean"}},"required":["finished"]}`, "minor"},
		{"nested required property", `{"type":"array","items":` + base + `}`, `{"type":"array","items":` + finished + `}`, "minor"},
		{"previously optional property", `{"type":"object","properties":{"id":{"type":"string"},"finished":{"type":"boolean"}},"required":["id"]}`, finished, "major"},
		{"required property removed", finished, base, "major"},
		{"existing type changed", base, `{"type":"object","properties":{"id":{"type":"integer"},"finished":{"type":"boolean"}},"required":["id","finished"]}`, "major"},
		{"closed response", strings.TrimSuffix(base, "}") + `,"additionalProperties":false}`, strings.TrimSuffix(finished, "}") + `,"additionalProperties":false}`, "major"},
	} {
		for _, direction := range []struct {
			operation, want string
		}{
			{"GET /sessions/{id} 200", tc.responseBump},
			{"POST /sessions/{id}/heartbeat 200", tc.responseBump},
			{"POST /sessions request", "major"},
			{"a", "major"}, // Unlabelled schemas must not inherit the response exception.
		} {
			t.Run(tc.name+"/"+direction.operation, func(t *testing.T) {
				before := Pin{SHA256: "old", Shape: json.RawMessage(fmt.Sprintf(`{%q:%s}`, direction.operation, tc.before))}
				after := Pin{SHA256: "new", Shape: json.RawMessage(fmt.Sprintf(`{%q:%s}`, direction.operation, tc.after))}
				got, err := RequiredBump(before, after)
				if err != nil || got != direction.want {
					t.Fatalf("got %q, %v; want %q", got, err, direction.want)
				}
			})
		}
	}
}

// TestPinnedShapeKeys rejects parse artifacts that landed in a pin as schema
// keys. Operation labels are the shape's outer keys. Inside a schema, a key is
// a JSON Schema keyword; names under properties, patternProperties,
// dependentSchemas, and component schema maps are declared names.
func TestPinnedShapeKeys(t *testing.T) {
	raw, err := os.ReadFile("pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]Pin
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	openAPI, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	current, err := Current(openAPI)
	if err != nil {
		t.Fatal(err)
	}
	check := func(label string, pin Pin) {
		var shape map[string]any
		if err := json.Unmarshal(pin.Shape, &shape); err != nil {
			t.Fatal(err)
		}
		for op, schema := range shape {
			if err := lintSchema(schema, label+" "+op); err != nil {
				t.Error(err)
			}
		}
	}
	for name, pin := range pins {
		check(name+" pin", pin)
	}
	for name, pin := range current {
		check(name+" parsed", pin)
	}
}

// TestOpenAPIPropertyNamesHaveNoSpace re-parses the canonical contract and the
// harness fragment. An unquoted flow-mapping description that contains ", " or
// ": " becomes extra keys, and those keys contain a space.
func TestOpenAPIPropertyNamesHaveNoSpace(t *testing.T) {
	for _, name := range []string{"../../api/openapi.yaml", "../../internal/harness/openapi.yaml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var doc any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			var bad []string
			var walk func(any, string)
			walk = func(v any, path string) {
				switch node := v.(type) {
				case map[string]any:
					keys := make([]string, 0, len(node))
					for key := range node {
						keys = append(keys, key)
					}
					sort.Strings(keys)
					for _, key := range keys {
						if strings.Contains(key, " ") {
							bad = append(bad, path+"/"+key)
						}
						walk(node[key], path+"/"+key)
					}
				case []any:
					for i, item := range node {
						walk(item, path+"["+strconv.Itoa(i)+"]")
					}
				}
			}
			walk(doc, "")
			if len(bad) > 0 {
				t.Fatalf("parsed OpenAPI property names contain a space:\n%s", strings.Join(bad, "\n"))
			}
		})
	}
}

func lintSchema(v any, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for key := range obj {
		if !jsonSchemaKeyword(key) {
			return fmt.Errorf("%s: %q is not a JSON Schema keyword or a declared property name", path, key)
		}
	}
	for key, val := range obj {
		switch key {
		case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
			named, ok := val.(map[string]any)
			if !ok {
				continue
			}
			for name, sub := range named {
				if err := lintSchema(sub, path+"."+key+"."+name); err != nil {
					return err
				}
			}
		case "allOf", "anyOf", "oneOf", "prefixItems":
			items, ok := val.([]any)
			if !ok {
				continue
			}
			for i, sub := range items {
				if err := lintSchema(sub, path+"."+key+"["+strconv.Itoa(i)+"]"); err != nil {
					return err
				}
			}
		case "items", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "not", "if", "then", "else", "contains", "propertyNames", "contentSchema":
			if err := lintSchema(val, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonSchemaKeyword(key string) bool {
	_, ok := jsonSchemaKeywords[key]
	return ok
}

// JSON Schema draft 2020-12 keywords plus the OpenAPI 3.1 schema-object fields.
var jsonSchemaKeywords = map[string]bool{
	"$anchor": true, "$comment": true, "$defs": true, "$dynamicAnchor": true, "$dynamicRef": true,
	"$id": true, "$ref": true, "$schema": true, "$vocabulary": true,
	"additionalProperties": true, "allOf": true, "anyOf": true, "const": true, "contains": true,
	"contentEncoding": true, "contentMediaType": true, "contentSchema": true, "default": true,
	"dependentRequired": true, "dependentSchemas": true, "deprecated": true, "description": true,
	"discriminator": true, "else": true, "enum": true, "example": true, "examples": true,
	"exclusiveMaximum": true, "exclusiveMinimum": true, "externalDocs": true, "format": true,
	"if": true, "items": true, "maxContains": true, "maxItems": true, "maxLength": true,
	"maxProperties": true, "maximum": true, "minContains": true, "minItems": true, "minLength": true,
	"minProperties": true, "minimum": true, "multipleOf": true, "not": true, "nullable": true,
	"oneOf": true, "pattern": true, "patternProperties": true, "prefixItems": true, "properties": true,
	"propertyNames": true, "readOnly": true, "required": true, "then": true, "title": true, "type": true,
	"unevaluatedItems": true, "unevaluatedProperties": true, "uniqueItems": true, "writeOnly": true, "xml": true,
}

func TestVersionShape(t *testing.T) {
	for name, s := range surfaces {
		parts := strings.Split(s.version, "/")
		if len(parts) != 2 || parts[0] != name {
			t.Errorf("%s: invalid contract name %s", name, s.version)
			continue
		}
		v := strings.Split(parts[1], ".")
		if len(v) != 2 {
			t.Errorf("%s: invalid major.minor version", name)
			continue
		}
		for _, number := range v {
			if _, err := strconv.Atoi(number); err != nil {
				t.Errorf("%s: invalid version %s", name, s.version)
			}
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package reportercontract

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The canonical bundle is ignored build output. Prepare it from authored areas
// so standalone package checks work in fresh remote checkouts too.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "node", "../../api/generate.mjs", "--write").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "prepare OpenAPI: %v\n%s", err, output)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// Risk: the generated contract drops a page field, requires a legacy `not`
// list on new writes, or exposes writable dispatch rules and missing routes.
func TestModelsV2PageOpenAPIContract(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	object := func(v any) map[string]any {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("expected contract object: %T", v)
		}
		return m
	}
	components := object(doc["components"])
	schemas := object(components["schemas"])
	properties := func(name string, inherited bool) map[string]any {
		t.Helper()
		schema := object(schemas[name])
		if inherited {
			schema = object(schema["allOf"].([]any)[1])
		}
		return object(schema["properties"])
	}
	for name, fields := range map[string][]string{
		"ModelSimpleRow":         {"column", "mine", "lock", "chain", "end", "never"},
		"ModelSimpleSituation":   {"chain", "end", "not"},
		"ModelSimplePreferences": {"all", "exceptions", "situations", "quick", "outdated", "unavailable"},
		"ModelSimpleStep":        {"line", "effort"},
		"ModelSimpleNever":       {"line", "by", "at", "why"},
		"ModelBoardOrderWrite":   {"rank", "not", "efforts", "end", "revision"},
		"ModelPause":             {"until", "reason", "by", "at"},
		"ReviewPolicySettings":   {"family_order", "floor", "security_excludes"},
	} {
		props := properties(name, name == "ModelSimpleRow" || name == "ModelSimpleSituation")
		for _, field := range fields {
			if props[field] == nil {
				t.Errorf("%s.%s missing", name, field)
			}
		}
	}
	row := properties("ModelSimpleRow", true)
	if !reflect.DeepEqual(object(row["end"])["enum"], []any{"wait", "default"}) {
		t.Fatal("row end contract changed")
	}
	page := properties("ModelSimplePreferences", false)
	for _, field := range []string{"line", "effort"} {
		if properties("ModelSimplePick", false)[field] == nil {
			t.Errorf("pick.%s missing", field)
		}
	}
	quick := object(object(page["quick"])["allOf"].([]any)[1])
	for _, field := range []string{"harness", "trace"} {
		if object(quick["properties"])[field] == nil {
			t.Errorf("quick.%s missing", field)
		}
	}
	outdated := object(object(page["outdated"])["properties"])
	for _, field := range []string{"count", "named", "uses"} {
		if outdated[field] == nil {
			t.Errorf("outdated.%s missing", field)
		}
	}
	named := object(object(object(outdated["named"])["items"])["properties"])
	if named["line"] == nil || named["successor"] == nil {
		t.Fatal("outdated names missing")
	}
	for _, name := range []string{"small", "fix", "stuck", "review"} {
		cells := object(object(page["situations"])["additionalProperties"])
		if object(cells["properties"])[name] == nil {
			t.Errorf("situation %s missing", name)
		}
	}
	unavailable := object(object(page["unavailable"])["items"])
	props := object(unavailable["properties"])
	for _, field := range []string{"cause", "until", "route", "waits"} {
		if props[field] == nil || !slices.Contains(unavailable["required"].([]any), any(field)) {
			t.Errorf("unavailable.%s missing/optional", field)
		}
	}
	if !reflect.DeepEqual(object(props["cause"])["enum"], []any{"daily_cap", "paused", "no_tools", "no_account", "retired", "residency"}) || object(props["reason"])["deprecated"] != true {
		t.Fatal("cause/deprecation contract changed")
	}
	for _, field := range []string{"family_order", "floor", "security_excludes"} {
		if object(properties("ReviewPolicySettings", false)[field])["readOnly"] != true && field != "floor" {
			t.Errorf("review %s is writable", field)
		}
	}
	if object(schemas["ReviewDispatchFloor"])["readOnly"] != true {
		t.Fatal("review floor is writable")
	}
	if slices.Contains(object(schemas["ModelBoardOrderWrite"])["required"].([]any), any("not")) {
		t.Fatal("new writes require legacy exclusion list")
	}
	paths := object(doc["paths"])
	for _, path := range []string{"/model-preferences/orders/{column}/first/never/{line}", "/models/lines/{harness}/{model}/pause"} {
		for _, method := range []string{"put", "delete"} {
			operation := object(object(paths[path])[method])
			if operation["security"] == nil || object(operation["responses"])["200"] == nil || object(operation["responses"])["403"] == nil {
				t.Errorf("incomplete %s %s", method, path)
			}
		}
	}
	orderPath := object(paths["/model-preferences/orders/{column}/{situation}"])
	for _, method := range []string{"put", "delete"} {
		parameters := object(orderPath[method])["parameters"].([]any)
		for _, parameter := range parameters {
			p := object(parameter)
			if p["name"] == "situation" {
				values := object(p["schema"])["enum"].([]any)
				if !slices.Contains(values, any("small")) || !slices.Contains(values, any("review")) {
					t.Errorf("%s order lacks new situations", method)
				}
			}
		}
		if object(object(orderPath[method])["responses"])["423"] == nil {
			t.Errorf("%s order missing locked status", method)
		}
	}
}

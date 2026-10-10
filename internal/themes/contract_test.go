// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestThemeContractParses(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	for path, methods := range map[string][]string{
		"/themes": {"get", "post"}, "/themes/{themeId}": {"get", "patch", "delete"},
		"/themes/{themeId}/duplicate": {"post"}, "/me/theme": {"get", "put"},
	} {
		for _, method := range methods {
			if contract.Paths[path][method] == nil {
				t.Errorf("missing %s %s", method, path)
			}
		}
	}
	for _, schema := range []string{"Theme", "ThemeValues", "ActiveTheme", "ThemeSelectionInput", "ThemePage"} {
		if contract.Components.Schemas[schema] == nil {
			t.Errorf("missing %s", schema)
		}
	}
}

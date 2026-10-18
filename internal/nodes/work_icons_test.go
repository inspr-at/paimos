// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// Risk (AEON-1142): the server, the API contract and the web picker accept
// different icon lists, so a saved icon is rejected or silently drawn as the
// default. All three must come from internal/nodes/work_icons.json; the web
// side (import and drawings) is checked in web/tests/work-icons.test.ts.
func TestWorkIconsMatchTheContractEnum(t *testing.T) {
	var doc struct {
		Icons []struct {
			Name string `json:"name"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(workIconsDocument, &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{""}
	for _, icon := range doc.Icons {
		want = append(want, icon.Name)
	}
	if len(want) < 101 {
		t.Fatalf("the picker must offer at least 100 icons, has %d", len(want)-1)
	}
	if len(workIcons) != len(want) {
		t.Fatalf("server allow-list has %d entries, document %d (duplicates?)", len(workIcons), len(want))
	}
	for _, name := range want {
		if !workIcons[name] {
			t.Fatalf("server rejects %q", name)
		}
	}
	raw, err := os.ReadFile("../../api/areas/settings.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if got := contract.Components.Schemas["WorkLevel"].Properties["icon"].Enum; !reflect.DeepEqual(got, want) {
		t.Fatalf("WorkLevel.icon enum drifted; run node scripts/work-icons.mjs --write\ngot  %q\nwant %q", got, want)
	}
	// A saved choice round-trips through validation; anything else is refused.
	for _, icon := range []string{"rocket", "folder-kanban", "", "epic"} {
		if err := (workVocabulary{Leaf: workLevel{Icon: icon}, Levels: []workLevel{{Icon: icon}}}).validate(); err != nil {
			t.Fatalf("%q: %v", icon, err)
		}
	}
	for _, icon := range []string{"Rocket", "rocket ", "lucide-rocket", "<svg>"} {
		if (workVocabulary{Leaf: workLevel{Icon: icon}}).validate() == nil {
			t.Fatalf("accepted %q", icon)
		}
	}
}

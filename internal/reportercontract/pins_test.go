// SPDX-License-Identifier: AGPL-3.0-only

package reportercontract

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
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

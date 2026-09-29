// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPairingNixGuideAdminConfiguration(t *testing.T) {
	t.Setenv("AEON_DATABASE_URL", "postgres://example")
	t.Setenv("AEON_ENV", "dev")
	t.Setenv("AEON_PAIRING_NIX_GUIDE_JSON", "")
	cfg, err := FromEnv()
	if err != nil || cfg.PairingNixGuide != nil {
		t.Fatalf("unconfigured instance published Nix guidance: %v", err)
	}
	raw := `{"module_url":"https://example.test/module.nix","service_option":"services.aeon.enable","platforms":["darwin"],"service_note":"Paired service support is required."}`
	t.Setenv("AEON_PAIRING_NIX_GUIDE_JSON", raw)
	cfg, err = FromEnv()
	if err != nil || cfg.PairingNixGuide == nil || cfg.PairingNixGuide.ModuleURL != "https://example.test/module.nix" || cfg.PairingNixGuide.Platforms[0] != "darwin" {
		t.Fatalf("configured guide unavailable: %v", err)
	}
	for _, platform := range [][]string{{"linux"}, {"darwin", "linux"}} {
		guide := *cfg.PairingNixGuide
		guide.Platforms = platform
		if err := guide.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"null", "{}", raw + "{}", strings.Repeat(" ", 8193), strings.Replace(raw, `"darwin"`, `"windows"`, 1), strings.Replace(raw, `["darwin"]`, `["darwin","darwin"]`, 1)} {
		if _, err := parsePairingNixGuide(bad); err == nil {
			t.Fatal("invalid guide accepted")
		}
	}
	for _, override := range []map[string]any{
		{"module_url": "javascript:alert(1)"}, {"module_url": "http://example.test/module.nix"},
		{"module_url": "https://user:synthetic@example.test/module.nix"}, {"module_url": "https://example.test/module.nix?token=synthetic"},
		{"service_option": "services.aeon.enable; run-command"}, {"service_note": ""}, {"service_note": "line\nbreak"},
		{"command": "run-command"}, {"platforms": []string{}},
	} {
		var data map[string]any
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		for k, v := range override {
			data[k] = v
		}
		bad, _ := json.Marshal(data)
		t.Setenv("AEON_PAIRING_NIX_GUIDE_JSON", string(bad))
		if _, err := FromEnv(); err == nil || strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "run-command") {
			t.Fatal("invalid configuration accepted or echoed")
		}
	}
}

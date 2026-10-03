// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceStepUpConfigurationPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := fileConfig{DefaultInstance: "fixture", Instances: map[string]fileInstance{"fixture": {URL: "https://paired.test", AgentdStateRoot: "/tmp/paired-fixture"}}}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	rt := &runtime{configPath: path}
	got, _, err := rt.loadConfig()
	if err != nil || got.Instances["fixture"].AgentdStateRoot != "/tmp/paired-fixture" {
		t.Fatal("instance pairing root lost")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "keys")); !os.IsNotExist(err) {
		t.Fatal("public config unexpectedly accessed keys")
	}
}

func TestStepUpRejectsRelativePairingPathWithoutReadingIt(t *testing.T) {
	if _, err := localStepUp("https://paired.test", "relative")(t.Context(), "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("relative pairing accepted")
	}
}

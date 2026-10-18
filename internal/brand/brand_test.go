// SPDX-License-Identifier: AGPL-3.0-only

package brand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOverride(t *testing.T) {
	t.Setenv(EnvFile, "")
	if b, err := Load(); err != nil || b != Default() {
		t.Fatalf("no override: %+v %v", b, err)
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "brand.json")
	if err := os.WriteFile(good, []byte(`{"schema":"inspr.brand.v1","product":"NOVA","generation":"1","release_name":"DAWN","wordmark":"NOVA DAWN","short_name":"DAWN"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvFile, good)
	if b, err := Load(); err != nil || b.Wordmark != "NOVA DAWN" || b.ShortName != "DAWN" {
		t.Fatalf("override: %+v %v", b, err)
	}
	for name, content := range map[string]string{
		"schema":  `{"schema":"inspr.brand.v2","product":"A","generation":"1","release_name":"B","wordmark":"A B","short_name":"B"}`,
		"missing": `{"schema":"inspr.brand.v1","product":"A","generation":"1","release_name":"B","wordmark":"","short_name":"B"}`,
		"unknown": `{"schema":"inspr.brand.v1","product":"A","generation":"1","release_name":"B","wordmark":"A B","short_name":"B","logo":"x"}`,
		"markup":  `{"schema":"inspr.brand.v1","product":"A","generation":"1","release_name":"<b>","wordmark":"A B","short_name":"B"}`,
		"long":    `{"schema":"inspr.brand.v1","product":"` + strings.Repeat("A", 49) + `","generation":"1","release_name":"B","wordmark":"A B","short_name":"B"}`,
		"json":    `{"schema":`,
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(EnvFile, path)
		if _, err := Load(); err == nil {
			t.Fatalf("%s: a bad brand file must fail", name)
		}
	}
	t.Setenv(EnvFile, filepath.Join(dir, "absent.json"))
	if _, err := Load(); err == nil {
		t.Fatal("a missing brand file must fail")
	}
}

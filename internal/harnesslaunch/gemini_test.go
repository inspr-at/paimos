// SPDX-License-Identifier: AGPL-3.0-only
package harnesslaunch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGeminiBudgetScaleAndSettings(t *testing.T) {
	for _, tc := range []struct {
		effort string
		level  int
	}{{"off", 0}, {"0", 0}, {"1", 1}, {"1024", 1}, {"1025", 2}, {"4096", 2}, {"4097", 3}, {"16384", 3}, {"16385", 4}, {"32768", 4}, {"32769", 5}} {
		got := GeminiEffortLevel(tc.effort)
		if got == nil || *got != tc.level {
			t.Fatalf("%s level %v", tc.effort, got)
		}
	}
	for _, effort := range []string{"default", "high", "-1", "001024", "1e4", "1\n", "1000001"} {
		if GeminiEffortLevel(effort) != nil {
			t.Fatal("guessed budget", effort)
		}
	}
	for _, tc := range []struct{ model, effort string }{{"gemini-2.5-pro", "0"}, {"gemini-2.5-pro", "32769"}, {"gemini-2.5-flash", "24577"}, {"auto", "4096"}, {"gemini-3-pro", "4096"}} {
		if _, err := GeminiSettings(tc.model, tc.effort); err == nil {
			t.Fatal("unsupported pin", tc)
		}
	}
	path, err := GeminiSettings("gemini-2.5-pro", "16384")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal("settings not private")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		ModelConfigs struct {
			Overrides []struct {
				Match struct {
					Model string `json:"model"`
				} `json:"match"`
				Config struct {
					Generate struct {
						Thinking struct {
							Budget int `json:"thinkingBudget"`
						} `json:"thinkingConfig"`
					} `json:"generateContentConfig"`
				} `json:"modelConfig"`
			} `json:"overrides"`
		} `json:"modelConfigs"`
	}
	if json.Unmarshal(raw, &settings) != nil || len(settings.ModelConfigs.Overrides) != 1 || settings.ModelConfigs.Overrides[0].Match.Model != "gemini-2.5-pro" || settings.ModelConfigs.Overrides[0].Config.Generate.Thinking.Budget != 16384 {
		t.Fatal("wrong vendor settings", string(raw))
	}
}

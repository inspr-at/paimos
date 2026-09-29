// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/inspr-at/paimos/internal/rulescompare"
)

// rulesChannelCheck is the aeon doctor form of rules compare (AEON-320).
// With the session hook installed it reports harness drift from the pinned
// doctrine and whether any rule is served twice. Without the hook it does
// not call the rules API.
func (rt *runtime) rulesChannelCheck() doctorCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return doctorCheck{Name: "rules", Status: "warn", Detail: "home directory unavailable"}
	}
	harnesses, err := sessionHookHarnesses(home)
	if err != nil {
		return doctorCheck{Name: "rules", Status: "warn", Detail: "session hook settings could not be read"}
	}
	if len(harnesses) == 0 {
		return doctorCheck{Name: "rules", Status: "ok", Detail: "session hook not installed"}
	}
	var layer struct {
		Sources []struct {
			Repository string `json:"repository"`
			Ref        string `json:"ref"`
			Commit     string `json:"commit"`
			Files      []struct {
				Rules []struct {
					Identity string `json:"identity"`
					Key      string `json:"key"`
					Text     string `json:"text"`
				} `json:"rules"`
			} `json:"files"`
		} `json:"sources"`
	}
	if err := rt.do(http.MethodGet, "/api/rules/doctrine", nil, &layer); err != nil {
		return doctorCheck{Name: "rules", Status: "fail", Detail: "doctrine index unavailable"}
	}
	var channels struct {
		Duplicates []struct {
			Identity string `json:"identity"`
			Doctrine string `json:"doctrine"`
		} `json:"duplicates"`
	}
	if err := rt.do(http.MethodGet, "/api/rules/channels", nil, &channels); err != nil {
		return doctorCheck{Name: "rules", Status: "fail", Detail: "rule channels unavailable"}
	}
	releases := make([]rulescompare.PinnedRelease, 0, len(layer.Sources))
	for _, source := range layer.Sources {
		rel := rulescompare.PinnedRelease{Repository: source.Repository, Ref: source.Ref, Commit: source.Commit}
		for _, file := range source.Files {
			for _, rule := range file.Rules {
				rel.Rules = append(rel.Rules, rulescompare.PinnedRule{Identity: rule.Identity, Key: rule.Key, Text: rule.Text})
			}
		}
		releases = append(releases, rel)
	}
	files := make([]rulescompare.HarnessFile, 0, len(harnesses))
	for _, harness := range harnesses {
		text, missing, err := readHarnessFile(harnessFile(home, harness))
		if err != nil {
			return doctorCheck{Name: "rules", Status: "warn", Detail: harness + ": rendered harness file could not be read"}
		}
		files = append(files, rulescompare.HarnessFile{Harness: harness, Missing: missing, Text: text})
	}
	dups := make([]rulescompare.ServedDuplicate, 0, len(channels.Duplicates))
	for _, dup := range channels.Duplicates {
		dups = append(dups, rulescompare.ServedDuplicate{Identity: dup.Identity, Doctrine: dup.Doctrine})
	}
	status, detail := rulescompare.DeliveryReport(files, releases, dups)
	return doctorCheck{Name: "rules", Status: status, Detail: detail}
}

func sessionHookHarnesses(home string) ([]string, error) {
	cwd, _ := os.Getwd()
	checks := []struct{ harness, path string }{
		{"claude", hookSettingsPath(home, "claude")},
		{"codex", hookSettingsPath(home, "codex")},
	}
	if cwd != "" {
		checks = append(checks,
			struct{ harness, path string }{"claude", filepath.Join(cwd, ".claude", "settings.local.json")},
			struct{ harness, path string }{"codex", filepath.Join(cwd, ".codex", "hooks.json")},
		)
	}
	var out []string
	seen := map[string]bool{}
	for _, check := range checks {
		if seen[check.harness] {
			continue
		}
		ok, err := fileHasMarker(check.path, inboxHookMarker)
		if err != nil {
			return nil, err
		}
		if ok {
			seen[check.harness] = true
			out = append(out, check.harness)
		}
	}
	return out, nil
}

func hookSettingsPath(home, harness string) string {
	if harness == "codex" {
		if dir := os.Getenv("CODEX_HOME"); dir != "" {
			return filepath.Join(dir, "hooks.json")
		}
		return filepath.Join(home, ".codex", "hooks.json")
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

func harnessFile(home, harness string) string {
	if harness == "codex" {
		if dir := os.Getenv("CODEX_HOME"); dir != "" {
			return filepath.Join(dir, "AGENTS.md")
		}
		return filepath.Join(home, ".codex", "AGENTS.md")
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "CLAUDE.md")
	}
	return filepath.Join(home, ".claude", "CLAUDE.md")
}

func fileHasMarker(path, marker string) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(raw) > 1<<20 {
		return false, errors.New("hook settings are larger than 1 MiB")
	}
	return strings.Contains(string(raw), marker), nil
}

func readHarnessFile(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(raw) > 1<<20 {
		return "", false, errors.New("harness file is larger than 1 MiB")
	}
	return string(raw), false, nil
}

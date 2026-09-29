// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/inspr-at/paimos/internal/rulescompare"
)

// rulesChannelCheck is the aeon doctor form of rules compare (AEON-320).
// With the session hook installed it reports harness drift from the pinned
// doctrine and whether any rule is served twice. Without the hook it does
// not call the rules API.
func (rt *runtime) rulesChannelCheck(options doctorRulesOptions) doctorCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return doctorCheck{Name: "rules", Status: "warn", Detail: "home directory unavailable"}
	}
	outputs, err := rulesSessionOutputs(home, options)
	if err != nil {
		return doctorCheck{Name: "rules", Status: "warn", Detail: "rules hook outputs unverified; use doctor --rules-harness and --rules-out with the delivered file"}
	}
	if len(outputs) == 0 {
		return doctorCheck{Name: "rules", Status: "ok", Detail: "rules session hook not installed"}
	}
	var layer struct {
		Sources []struct {
			Repository string `json:"repository"`
			Ref        string `json:"ref"`
			Commit     string `json:"commit"`
			State      string `json:"state"`
			Error      string `json:"error"`
			Files      []struct {
				Problem string `json:"problem"`
				Rules   []struct {
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
		Releases []struct {
			Repository string `json:"repository"`
			Commit     string `json:"commit"`
		} `json:"releases"`
		Duplicates []struct {
			Identity string `json:"identity"`
			Doctrine string `json:"doctrine"`
		} `json:"duplicates"`
	}
	if err := rt.do(http.MethodGet, "/api/rules/channels", nil, &channels); err != nil {
		return doctorCheck{Name: "rules", Status: "fail", Detail: "rule channels unavailable"}
	}
	releases := make([]rulescompare.PinnedRelease, 0, len(layer.Sources))
	pins := map[string]string{}
	for _, source := range layer.Sources {
		if source.Files == nil {
			return doctorCheck{Name: "rules", Status: "fail", Detail: "doctrine index unverified"}
		}
		pins[source.Repository] = source.Commit
		rel := rulescompare.PinnedRelease{Repository: source.Repository, Ref: source.Ref, Commit: source.Commit, State: source.State, Error: source.Error}
		for _, file := range source.Files {
			if file.Rules == nil || file.Problem != "" {
				return doctorCheck{Name: "rules", Status: "fail", Detail: "doctrine index unverified"}
			}
			for _, rule := range file.Rules {
				rel.Rules = append(rel.Rules, rulescompare.PinnedRule{Identity: rule.Identity, Key: rule.Key, Text: rule.Text})
			}
		}
		releases = append(releases, rel)
	}
	if layer.Sources == nil || channels.Duplicates == nil || channels.Releases == nil || len(channels.Releases) != len(pins) {
		return doctorCheck{Name: "rules", Status: "fail", Detail: "rule channels or doctrine index unverified"}
	}
	for _, release := range channels.Releases {
		if pin, ok := pins[release.Repository]; !ok || pin != release.Commit {
			return doctorCheck{Name: "rules", Status: "fail", Detail: "doctrine pin changed during comparison; run doctor again"}
		}
		delete(pins, release.Repository)
	}
	files := make([]rulescompare.HarnessFile, 0, 2*len(outputs))
	seenHarness := map[string]bool{}
	for _, output := range outputs {
		paths := []struct {
			path    string
			session bool
		}{{output.Path, true}}
		if !seenHarness[output.Harness] {
			paths = append(paths, struct {
				path    string
				session bool
			}{harnessFile(home, output.Harness), false})
			seenHarness[output.Harness] = true
		}
		for _, target := range paths {
			text, missing, err := readHarnessFile(target.path)
			if err != nil {
				return doctorCheck{Name: "rules", Status: "warn", Detail: output.Harness + ": delivered file could not be read"}
			}
			files = append(files, rulescompare.HarnessFile{Harness: output.Harness, Path: target.path, Session: target.session, Missing: missing, Text: text})
		}
	}
	dups := make([]rulescompare.ServedDuplicate, 0, len(channels.Duplicates))
	for _, dup := range channels.Duplicates {
		dups = append(dups, rulescompare.ServedDuplicate{Identity: dup.Identity, Doctrine: dup.Doctrine})
	}
	status, detail := rulescompare.DeliveryReport(files, releases, dups)
	return doctorCheck{Name: "rules", Status: status, Detail: detail}
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

func readHarnessFile(path string) (string, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("expected a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return "", false, err
	}
	if len(raw) > 1<<20 {
		return "", false, errors.New("harness file is larger than 1 MiB")
	}
	return string(raw), false, nil
}

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const rulesHookMarker = " # aeon-rules-hook-v1"

type doctorRulesOptions struct{ Harness, Out string }
type rulesSessionOutput struct{ Harness, Path string }

// rulesSessionOutputs reads configuration only. It never runs hook commands or
// expands shell expressions. A dynamic target needs an explicit doctor input.
func rulesSessionOutputs(home string, options doctorRulesOptions) ([]rulesSessionOutput, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	output := func(harness, path string) (rulesSessionOutput, error) {
		if harness == "claude-code" {
			harness = "claude"
		}
		if path == "" || filepath.Ext(path) != ".txt" {
			return rulesSessionOutput{}, errors.New("session output must be a .txt file")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		return rulesSessionOutput{harness, filepath.Clean(path)}, nil
	}
	if options.Out != "" {
		item, err := output(options.Harness, options.Out)
		return []rulesSessionOutput{item}, err
	}
	checks := []struct{ harness, path string }{
		{"claude", hookSettingsPath(home, "claude")},
		{"codex", hookSettingsPath(home, "codex")},
		{"claude", filepath.Join(cwd, ".claude", "settings.local.json")},
		{"codex", filepath.Join(cwd, ".codex", "hooks.json")},
	}
	var out []rulesSessionOutput
	seen := map[rulesSessionOutput]bool{}
	for _, check := range checks {
		raw, missing, err := readHarnessFile(check.path)
		if err != nil {
			return nil, err
		}
		if missing {
			continue
		}
		var settings struct {
			Hooks map[string][]struct {
				Hooks []struct{ Type, Command string } `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return nil, err
		}
		for _, groups := range settings.Hooks {
			for _, group := range groups {
				for _, hook := range group.Hooks {
					if hook.Type != "command" || !strings.HasSuffix(strings.TrimSpace(hook.Command), rulesHookMarker) {
						continue
					}
					args, err := staticHookArgs(strings.TrimSuffix(strings.TrimSpace(hook.Command), rulesHookMarker))
					if err != nil {
						return nil, err
					}
					var path string
					for i := 0; i < len(args); i++ {
						if args[i] == "--rules-out" && i+1 < len(args) {
							i++
							path = args[i]
						} else if strings.HasPrefix(args[i], "--rules-out=") {
							path = strings.TrimPrefix(args[i], "--rules-out=")
						}
					}
					item, err := output(check.harness, path)
					if err != nil {
						return nil, err
					}
					if !seen[item] {
						seen[item] = true
						out = append(out, item)
					}
				}
			}
		}
	}
	return out, nil
}

// Only literal words and shell quoting are supported; substitutions, redirects,
// pipelines and compound commands cannot establish a verified output path.
func staticHookArgs(command string) ([]string, error) {
	var out []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, ch := range command {
		if strings.ContainsRune("$`\n\r;|&<>()", ch) {
			return nil, errors.New("dynamic rules hook")
		}
		if quote == 0 && !escaped && strings.ContainsRune("~*?[]{}#", ch) {
			return nil, errors.New("nonliteral rules hook")
		}
		if quote == '"' && ch == '\\' {
			return nil, errors.New("unsupported double-quote escape")
		}
		if escaped {
			word.WriteRune(ch)
			escaped = false
		} else if ch == '\\' && quote != '\'' {
			escaped, started = true, true
		} else if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteRune(ch)
			}
		} else if ch == '\'' || ch == '"' {
			quote, started = ch, true
		} else if ch == ' ' || ch == '\t' {
			if started {
				out = append(out, word.String())
				word.Reset()
				started = false
			}
		} else {
			word.WriteRune(ch)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("incomplete hook quoting")
	}
	if started {
		out = append(out, word.String())
	}
	return out, nil
}

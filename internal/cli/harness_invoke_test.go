// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The fixture reports only its arguments and credential-free generated budget.
func TestHarnessInvokeFixture(t *testing.T) {
	if os.Getenv("AEON_HARNESS_INVOKE_FIXTURE") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	budget := -1
	if path := os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH"); path != "" {
		var settings struct {
			Configs struct {
				Overrides []struct {
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
		raw, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || info.Mode().Perm() != 0600 || json.Unmarshal(raw, &settings) != nil || len(settings.Configs.Overrides) != 1 {
			os.Exit(8)
		}
		budget = settings.Configs.Overrides[0].Config.Generate.Thinking.Budget
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Args   []string
		Budget int
	}{args, budget})
	os.Exit(0)
}

func TestHarnessInvokePinsArgumentsAndBudget(t *testing.T) {
	const prompt = "fixture ; $(no-command) ' quoted"
	for _, tc := range []struct {
		harness, model, effort string
		review                 bool
		budget                 int
		args                   []string
	}{
		{"gemini", "gemini-2.5-pro", "16384", false, 16384, []string{"--model", "gemini-2.5-pro", "--output-format", "json", "-p", prompt}},
		{"gemini", "gemini-2.5-flash", "0", true, 0, []string{"--model", "gemini-2.5-flash", "--output-format", "json", "--approval-mode", "plan", "-p", prompt}},
		{"opencode", "ollama/qwen3-coder", "default", false, -1, []string{"run", "--model", "ollama/qwen3-coder", "--format", "json", "--", prompt}},
		{"opencode", "custom/model", "high", true, -1, []string{"run", "--model", "custom/model", "--format", "json", "--variant", "high", "--agent", "plan", "--", prompt}},
	} {
		t.Run(tc.harness+"/"+tc.effort, func(t *testing.T) {
			isolate(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, tc.harness)
			script := fmt.Sprintf("#!/bin/sh\nAEON_HARNESS_INVOKE_FIXTURE=1 exec %q -test.run=^TestHarnessInvokeFixture$ -- \"$@\"\n", exe)
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root)
			t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", "")
			args := []string{"aeon", "harness", "invoke", "--harness", tc.harness, "--model", tc.model, "--effort", tc.effort}
			if tc.review {
				args = append(args, "--review")
			}
			code, out, stderr := runCLI(append(args, "--", prompt), "")
			var result struct {
				Args   []string
				Budget int
			}
			if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &result) != nil || !reflect.DeepEqual(result.Args, tc.args) || result.Budget != tc.budget {
				t.Fatalf("invocation code %d result %+v stderr %q", code, result, stderr)
			}
		})
	}
}

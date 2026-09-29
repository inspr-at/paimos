// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

func npmFixture(t *testing.T, harness string) (Discovery, string, harnesslaunch.Node) {
	t.Helper()
	home, path, node := piNodeFixture(t)
	script := `#!/usr/bin/env node
[ -z "$NODE_OPTIONS" ] && [ -z "$NODE_PATH" ] || exit 126
case "$1" in
 --version) echo 1.2.3; exit;;
 login) echo 'Logged in using ChatGPT'; exit;;
 status) echo '{"status":"authenticated","isAuthenticated":true,"userInfo":{"userId":"42","email":"agent@example.test"}}'; exit;;
 auth) echo '{"loggedIn":true,"email":"agent@example.test"}'; exit;;
esac
IFS= read -r line
printf '%s\n' '{"id":1,"result":{}}'
IFS= read -r line
IFS= read -r line
printf '%s\n' '{"id":2,"result":{"account":{"type":"chatgpt","email":"agent@example.test"}}}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	d := Discovery{Home: home, NodePath: node.Path, Workspace: physicalTemp(t), LookPath: func(string) (string, error) { return path, nil }}
	return d, path, node
}

func TestNpmDiscoveryPinsEveryLauncher(t *testing.T) {
	for _, harness := range []string{"codex", "cursor", "claude"} {
		t.Run(harness, func(t *testing.T) {
			d, path, node := npmFixture(t, harness)
			t.Setenv("PATH", physicalTemp(t))
			t.Setenv("NODE_OPTIONS", "synthetic")
			t.Setenv("NODE_PATH", "synthetic")
			c, err := d.Detect(t.Context(), harness, "")
			if err != nil || c.Node != node || c.Path != path || c.Login != "signed_in" {
				t.Fatal("discovery failed", err)
			}
			public, _ := json.Marshal(c)
			if strings.Contains(string(public), node.Path) || strings.Contains(string(public), node.Version) {
				t.Fatal("private pin leaked")
			}
			local := LocalCandidate{Candidate: c, Path: path, Home: c.Home, Identity: c.Identity}
			raw, err := json.Marshal(local)
			if err != nil {
				t.Fatal(err)
			}
			var restored LocalCandidate
			if err := json.Unmarshal(raw, &restored); err != nil || restored.Candidate.Node != node {
				t.Fatal("private pin lost")
			}
		})
	}
}

func TestNpmEnrollmentPersistsAndValidatesPins(t *testing.T) {
	for _, harness := range []string{"codex", "cursor"} {
		for _, add := range []bool{false, true} {
			t.Run(harness+map[bool]string{false: "/setup", true: "/add"}[add], func(t *testing.T) {
				e, api, _, options, _ := engineFixture(t)
				defer e.Store.Close()
				d, path, node := npmFixture(t, harness)
				c, err := d.Detect(t.Context(), harness, "")
				if err != nil {
					t.Fatal(err)
				}
				if add {
					options.Candidates[0].Harness = "pi"
					options.Candidates[0].Identity = "anthropic"
					approveFixture(t, e, api, options)
					api.approved = false
					if _, err := e.AddHarness(t.Context(), []Candidate{c}); err != nil {
						t.Fatal(err)
					}
					api.approved = true
					s, err := e.load()
					if err != nil {
						t.Fatal(err)
					}
					s.NextPoll = e.now()
					if err := e.save(s, false); err != nil {
						t.Fatal(err)
					}
					if _, err := e.Step(t.Context()); err != nil {
						t.Fatal(err)
					}
				} else {
					options.Candidates = []Candidate{c}
					approveFixture(t, e, api, options)
				}
				saved, err := e.SavedOptions()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, candidate := range saved.Candidates {
					if candidate.Harness == harness {
						found = candidate.Node == node
					}
				}
				if !found {
					t.Fatal("resume lost interpreter pin")
				}
				config, err := ReadRuntimeConfig(e.Store.Path())
				if err != nil {
					t.Fatal(err)
				}
				found = false
				for _, account := range config.Accounts {
					if account.Harness == harness {
						found = account.Node == node
					}
				}
				if !found {
					t.Fatal("runtime lost interpreter pin")
				}
				if err := ValidateRuntimeDependencies(config); err != nil {
					t.Fatal(err)
				}
				wire, _ := json.Marshal(api.request.Accounts)
				if strings.Contains(string(wire), node.Path) {
					t.Fatal("pin uploaded")
				}
				for _, scenario := range []string{"missing", "version drift", "writable", "workspace"} {
					cfg := RuntimeConfig{Workspace: d.Workspace, Accounts: []RuntimeAccount{{Harness: harness, Path: path, Node: node}}}
					switch scenario {
					case "missing":
						cfg.Accounts[0].Node = harnesslaunch.Node{}
					case "version drift":
						cfg.Accounts[0].Node.Version = "22.20.0"
					case "writable":
						if err := os.Chmod(node.Path, 0777); err != nil {
							t.Fatal(err)
						}
					case "workspace":
						if err := os.Chmod(node.Path, 0700); err != nil {
							t.Fatal(err)
						}
						cfg.Workspace = filepath.Dir(node.Path)
					}
					if !errors.Is(ValidateRuntimeDependencies(cfg), harnesslaunch.ErrStart) {
						t.Fatal("unsafe pin accepted", scenario)
					}
				}
			})
		}
	}
}

func TestSetupUsesServicePathForShellWrappers(t *testing.T) {
	for _, harness := range []string{"pi", "codex", "cursor", "claude"} {
		t.Run(harness, func(t *testing.T) {
			d, path, node := npmFixture(t, harness)
			script := "#!/bin/sh\nexec node " + path + ".entry \"$@\"\n"
			if err := os.Rename(path, path+".entry"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(node.Path))
			d.NodePath = ""
			if c, err := d.Detect(t.Context(), harness, ""); !errors.Is(err, harnesslaunch.ErrStart) || c.Login == "signed_in" {
				t.Fatal("shell PATH masked missing service interpreter", err)
			}
			if harness != "pi" {
				d.NodePath = node.Path
				if _, err := d.Detect(t.Context(), harness, ""); err != nil {
					t.Fatal("explicit wrapper interpreter rejected", err)
				}
			}
		})
	}
}

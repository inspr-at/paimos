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

func TestUnpinnedEnrollmentDoesNotVetoSibling(t *testing.T) {
	root := physicalTemp(t)
	nodePath := filepath.Join(root, "node")
	if err := os.WriteFile(nodePath, []byte("#!/bin/sh\necho v22.19.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	envNode := filepath.Join(root, "env-node")
	if err := os.WriteFile(envNode, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(root, "shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	node := harnesslaunch.Node{Path: nodePath, Version: "22.19.0"}
	pinned := RuntimeAccount{Harness: "cursor", AccountID: "new", Path: envNode, Node: node}
	old := RuntimeAccount{Harness: "codex", AccountID: "old", Path: envNode}
	native := RuntimeAccount{Harness: "codex", AccountID: "nix", Path: shell}
	pi := RuntimeAccount{Harness: "pi", AccountID: "pi-old", Path: envNode}
	if !UnpinnedEnrollment(old) || !UnpinnedEnrollment(pi) || UnpinnedEnrollment(pinned) || UnpinnedEnrollment(native) {
		t.Fatal("unpinned classification changed")
	}
	full := RuntimeConfig{Accounts: []RuntimeAccount{pinned, old}}
	if !errors.Is(ValidateRuntimeDependencies(full), harnesslaunch.ErrStart) {
		t.Fatal("mixed config stopped failing closed")
	}
	if err := ValidateRuntimeDependencies(LaunchableRuntime(full)); err != nil {
		t.Fatal("pinned sibling vetoed", err)
	}
	if err := ValidateRuntimeDependencies(RuntimeConfig{Accounts: []RuntimeAccount{native}}); err != nil {
		t.Fatal("native wrapper rejected", err)
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

func TestAccountPinBlockReasons(t *testing.T) {
	root := physicalTemp(t)
	nodePath := filepath.Join(root, "node")
	if err := os.WriteFile(nodePath, []byte("#!/bin/sh\necho v22.19.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "launcher")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(root, "shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	node := harnesslaunch.Node{Path: nodePath, Version: "22.19.0"}
	healthy := RuntimeAccount{Harness: "cursor", AccountID: "healthy", Path: launcher, Node: node}
	cases := []struct {
		name, reason, fix string
		bad               RuntimeAccount
		workspace         string
	}{
		{"drifted", PinDrifted, FixRepin, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: harnesslaunch.Node{Path: nodePath, Version: "22.20.0"}}, ""},
		{"invalid", PinInvalid, FixRepin, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: harnesslaunch.Node{Path: nodePath, Version: "not-a-version"}}, ""},
		{"partial", PinPartial, FixRepin, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: harnesslaunch.Node{Path: nodePath}}, ""},
		{"missing", PinMissing, FixAddHarness, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher}, ""},
		{"unsafe", PinUnsafe, FixRepin, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: node}, root},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := RuntimeConfig{Workspace: tc.workspace, Accounts: []RuntimeAccount{healthy, tc.bad}}
			if tc.name == "unsafe" {
				cfg.Accounts[0].Node = harnesslaunch.Node{}
				cfg.Accounts[0].Path = shell
			}
			blocks := AccountPinBlocks(cfg)
			if len(blocks) != 1 || blocks[0].AccountID != "bad" || blocks[0].Harness != "codex" || blocks[0].Reason != tc.reason || blocks[0].Fix != tc.fix {
				t.Fatalf("pin classification: %+v", blocks)
			}
			if err := ValidateRuntimeDependencies(cfg); !errors.Is(err, harnesslaunch.ErrStart) {
				t.Fatal("whole-config check stopped failing closed", err)
			}
			kept := LaunchableRuntime(cfg)
			if tc.reason == PinMissing {
				if err := ValidateRuntimeDependencies(kept); err != nil {
					t.Fatal("missing pin still vetoed the sibling", err)
				}
			}
		})
	}
	native := RuntimeConfig{Accounts: []RuntimeAccount{{Harness: "codex", AccountID: "nix", Path: shell}, {Harness: "grok", AccountID: "grok", Path: shell}}}
	if blocks := AccountPinBlocks(native); len(blocks) != 0 {
		t.Fatalf("native and grok accounts were pin-blocked: %+v", blocks)
	}
	claude := RuntimeConfig{Accounts: []RuntimeAccount{{Harness: "claude", AccountID: "claude", Path: shell}, {Harness: "codex", AccountID: "codex", Path: shell}}}
	blocks := AccountPinBlocks(claude)
	if len(blocks) != 1 || blocks[0].AccountID != "claude" || blocks[0].Reason != PinMissing || blocks[0].Fix != FixAddHarness {
		t.Fatalf("claude dependency blocked the wrong account: %+v", blocks)
	}
	if ValidateRuntimeDependencies(claude) == nil {
		t.Fatal("missing claude pins became launchable")
	}
}

func TestStatusKeepsBlockedAccountReasonAndFix(t *testing.T) {
	e, a, l, o, _ := engineFixture(t)
	approveFixture(t, e, a, o)
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", Ready: true, BlockedAccounts: []BlockedAccount{{AccountID: "old", Harness: "codex", Reason: PinDrifted, Fix: FixRepin}}}
	p, err := e.Status(t.Context())
	if err != nil || p.Stage != "connected" || len(p.BlockedAccounts) != 1 || p.BlockedAccounts[0].AccountID != "old" || p.BlockedAccounts[0].Harness != "codex" || p.BlockedAccounts[0].Reason != PinDrifted || p.BlockedAccounts[0].Fix != FixRepin {
		t.Fatalf("connected status hid the blocked account: stage=%s blocks=%+v err=%v", p.Stage, p.BlockedAccounts, err)
	}
}

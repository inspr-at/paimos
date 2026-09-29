// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/piprobe"
)

func piNodeFixture(t *testing.T) (string, string, piprobe.Node) {
	t.Helper()
	home := physicalTemp(t)
	profile := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	node := piprobe.Node{Path: filepath.Join(home, "node"), Version: "22.19.0"}
	// This interpreter double still exercises the kernel's env-node lookup.
	if err := os.WriteFile(node.Path, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo v22.19.0; exit; fi\nexec /bin/sh \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "pi")
	script := `#!/usr/bin/env node
[ -z "$NODE_OPTIONS" ] && [ -z "$ANTHROPIC_API_KEY" ] || exit 3
if [ "$1" = --version ]; then echo 0.87.1; exit; fi
IFS= read -r line
printf '%s\n' '{"id":"get_state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"anthropic","id":"test"}}}'
IFS= read -r line
printf '%s\n' '{"id":"get_available_models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"anthropic","id":"test"}]}}'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return home, path, node
}

func TestPiDiscoveryPinsEnvNode(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit without node on PATH"}[explicit], func(t *testing.T) {
			home, path, node := piNodeFixture(t)
			d := Discovery{Home: home, Workspace: physicalTemp(t)}
			t.Setenv("PATH", home)
			if explicit {
				d.NodePath = node.Path
				bin := physicalTemp(t)
				if err := os.Symlink(path, filepath.Join(bin, "pi")); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin)
			}
			t.Setenv("NODE_OPTIONS", "synthetic-not-an-option")
			t.Setenv("ANTHROPIC_API_KEY", "synthetic-not-a-credential")
			c, err := d.Detect(t.Context(), "pi", "")
			if err != nil || c.PiNode != node || c.Version != "0.87.1" || c.Login != "signed_in" {
				t.Fatal("env-node discovery failed", err)
			}
			raw, _ := json.Marshal(c)
			if strings.Contains(string(raw), home) || strings.Contains(string(raw), node.Version) || strings.Contains(string(raw), "pi_node") {
				t.Fatal("interpreter binding escaped public candidate")
			}
			t.Setenv("PATH", physicalTemp(t))
			if _, err := exec.LookPath("node"); err == nil {
				t.Fatal("service PATH unexpectedly has node")
			}
			if provider, err := piprobe.Provider(t.Context(), path, c.Home, "anthropic", c.PiNode.Path); err != nil || provider != "anthropic" {
				t.Fatal("pinned probe failed with service PATH", err)
			}
			if err := ValidateRuntimeDependencies(RuntimeConfig{Workspace: d.Workspace, Accounts: []RuntimeAccount{{Harness: "pi", Path: path, Home: c.Home, PiNode: c.PiNode}}}); err != nil {
				t.Fatal("runtime interpreter pin rejected", err)
			}
		})
	}
}

func TestPiDiscoveryRefusesMissingOrUnsafeNode(t *testing.T) {
	for _, scenario := range []string{"missing", "workspace", "writable", "invalid version", "unsupported interpreter"} {
		t.Run(scenario, func(t *testing.T) {
			home, path, node := piNodeFixture(t)
			workspace := physicalTemp(t)
			if scenario == "workspace" {
				workspace = home
			}
			if scenario == "writable" {
				if err := os.Chmod(node.Path, 0777); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "invalid version" {
				if err := os.WriteFile(node.Path, []byte("#!/bin/sh\necho private-diagnostic\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unsupported interpreter" {
				if err := os.WriteFile(path, []byte("#!/usr/bin/env other\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			d := Discovery{Home: home, Workspace: workspace, NodePath: node.Path, LookPath: func(name string) (string, error) {
				if name == "pi" {
					return path, nil
				}
				return "", os.ErrNotExist
			}}
			if scenario == "missing" {
				d.NodePath = ""
			}
			c, err := d.Detect(t.Context(), "pi", "")
			if !errors.Is(err, piprobe.ErrStart) || c.Login == "signed_in" || strings.Contains(err.Error(), "private-diagnostic") {
				t.Fatal("unsafe dependency not rejected as startup failure", err)
			}
			if scenario == "missing" && !strings.Contains(err.Error(), "--node-path") {
				t.Fatal("missing recovery action")
			}
		})
	}
}

func TestPiDiscovery(t *testing.T) {
	for _, scenario := range []string{"found", "missing", "not signed in", "unpinnable", "not executable", "invalid version", "wrong context"} {
		t.Run(scenario, func(t *testing.T) {
			home := physicalTemp(t)
			path := filepath.Join(home, "pi")
			if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			if scenario == "not executable" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			probes := 0
			d := Discovery{Home: home, LookPath: func(name string) (string, error) {
				if name != "pi" {
					t.Fatal("wrong executable")
				}
				if scenario == "missing" {
					return "", os.ErrNotExist
				}
				if scenario == "unpinnable" {
					return "pi-does-not-exist", nil
				}
				return path, nil
			}, Executor: executorFunc(func(_ context.Context, c Command) ([]byte, error) {
				if c.Path != path || strings.Join(c.Args, " ") != "--version" {
					t.Fatal("unexpected discovery command")
				}
				if scenario == "invalid version" {
					return []byte("private diagnostic"), nil
				}
				return []byte("pi 0.60.0"), nil
			}), PiProvider: func(_ context.Context, executable, profile, expected, node string) (string, error) {
				probes++
				if executable != path || profile != filepath.Join(home, ".pi", "agent") || expected != "anthropic" || node != "" {
					t.Fatal("probe lost account binding")
				}
				if scenario == "not signed in" {
					return "", errors.New("private diagnostic")
				}
				if scenario == "wrong context" {
					return "openai", nil
				}
				return "anthropic", nil
			}}
			c, err := d.Detect(t.Context(), "pi", "anthropic")
			if scenario != "found" {
				if err == nil || c.Login == "signed_in" {
					t.Fatal("unsafe discovery succeeded")
				}
				if strings.Contains(err.Error(), "private diagnostic") {
					t.Fatal("vendor diagnostic escaped")
				}
				return
			}
			if err != nil || c.Identity != "anthropic" || c.Provider != "anthropic" || c.Login != "signed_in" || c.Version != "0.60.0" || probes != 1 {
				t.Fatal("provider discovery failed", err)
			}
			raw, _ := json.Marshal(c)
			if strings.Contains(string(raw), home) || strings.Contains(string(raw), "identity") || !strings.Contains(string(raw), "local profile") {
				t.Fatal("public candidate contains private binding or misleading identity")
			}
		})
	}
}

func TestPiSetupAndAddHarnessPersistPrivateBinding(t *testing.T) {
	for _, add := range []bool{false, true} {
		t.Run(map[bool]string{false: "setup", true: "add-harness"}[add], func(t *testing.T) {
			e, api, _, o, _ := engineFixture(t)
			defer e.Store.Close()
			home, path, node := piNodeFixture(t)
			pi := Candidate{Harness: "pi", Provider: "anthropic", Label: "pi / anthropic (local profile)", Identity: "anthropic", Path: path, Home: filepath.Join(home, ".pi", "agent"), Login: "signed_in", Version: "0.87.1", PiNode: node}
			if add {
				approveFixture(t, e, api, o)
				api.approved = false
				if _, err := e.AddHarness(t.Context(), []Candidate{pi}); err != nil {
					t.Fatal(err)
				}
			} else {
				o.Candidates = []Candidate{pi}
				if _, err := e.Begin(t.Context(), o); err != nil {
					t.Fatal(err)
				}
			}
			api.approved = true
			raw, _ := json.Marshal(api.request)
			if strings.Contains(string(raw), node.Path) || strings.Contains(string(raw), node.Version) {
				t.Fatal("pairing upload exposed interpreter binding")
			}
			s, err := e.load()
			if err != nil {
				t.Fatal(err)
			}
			s.NextPoll = e.now()
			if err := e.save(s, false); err != nil {
				t.Fatal(err)
			}
			if p, err := e.Step(t.Context()); err != nil || p.Stage != "connected" {
				t.Fatal("pi pairing failed", err)
			}
			config, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, account := range config.Accounts {
				if account.Harness == "pi" {
					found = account.Identity == "anthropic" && account.Home == pi.Home && account.Path == pi.Path && account.PiNode == node
				}
			}
			if !found {
				t.Fatal("runtime lost pi provider/profile binding")
			}
		})
	}
}

func TestServerSelectedProfileKeepsEveryOtherChoiceBound(t *testing.T) {
	local := []Candidate{{Key: "local", Harness: "pi", Provider: "anthropic", Label: "pi / anthropic (local profile)"}}
	remote := append([]Candidate(nil), local...)
	remote[0].ProfileID = testAccount
	if !sameChoices(remote, local) || local[0].ProfileID != "" {
		t.Fatal("valid server default not accepted without mutating choices")
	}
	for _, change := range []func(*Candidate){
		func(c *Candidate) { c.Provider = "openai" },
		func(c *Candidate) { c.Harness = "codex" },
		func(c *Candidate) { c.Label = "other" },
		func(c *Candidate) { c.Key = "other" },
		func(c *Candidate) { c.ProfileID = "invalid" },
	} {
		changed := append([]Candidate(nil), remote...)
		change(&changed[0])
		if sameChoices(changed, local) {
			t.Fatal("default profile widened another choice")
		}
	}
	local[0].ProfileID = otherAccount
	if sameChoices(remote, local) {
		t.Fatal("pinned profile was replaced")
	}
}

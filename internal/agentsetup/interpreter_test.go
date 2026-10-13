// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

// A synthetic Homebrew Cellar plus a Nix-style bash guard that execs an
// env-node shim. Only temp files and fake vendor executors are involved.
func codexUpgradeFixture(t *testing.T) (Discovery, string, harnesslaunch.Node, harnesslaunch.Node) {
	t.Helper()
	root, home := physicalTemp(t), physicalTemp(t)
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	nodes := []harnesslaunch.Node{}
	for _, version := range []string{"26.10.0", "26.11.1"} {
		node := harnesslaunch.Node{Path: filepath.Join(root, "Cellar", "node", version, "bin", "node"), Version: version}
		if err := os.MkdirAll(filepath.Dir(node.Path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(node.Path, []byte("#!/bin/sh\nprintf '%s\\n' v"+version+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
	}
	launcher := filepath.Join(root, "store", "synthetic-codex", "bin", "codex")
	shim := filepath.Join(root, "npm", "codex")
	for _, path := range []string{launcher, shim} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(shim, []byte("#!/usr/bin/env node\n// synthetic shim\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/bash\nexec "+shim+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	d := Discovery{Home: home, Workspace: physicalTemp(t), LookPath: func(name string) (string, error) {
		switch name {
		case "codex":
			return launcher, nil
		case "node":
			return nodes[1].Path, nil
		}
		return "", os.ErrNotExist
	}, nodeClass: func(path string) string {
		if !within(root, path) {
			return ""
		}
		return nodeLocationClass("/opt/homebrew/" + strings.TrimPrefix(path, root+"/"))
	}, Executor: executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		for _, node := range nodes {
			if c.Path == node.Path && len(c.Args) == 1 && c.Args[0] == "--version" {
				return []byte("v" + node.Version), nil
			}
		}
		if c.Path == launcher && len(c.Args) > 0 {
			switch c.Args[0] {
			case "--version":
				return []byte("codex-cli 0.157.1"), nil
			case "login":
				return []byte("Logged in using ChatGPT"), nil
			}
		}
		t.Fatal("unexpected vendor command in renewal fixture")
		return nil, os.ErrInvalid
	}), CodexProbe: func(_ context.Context, _, _, node, _ string) (string, error) {
		if node == "" {
			return "", harnesslaunch.ErrStart
		}
		return "agent@example.test", nil
	}}
	return d, launcher, nodes[0], nodes[1]
}

// Risk: a failed runtime commit must leave renewal retryable and must not
// publish a successful renewal or discard the existing repair command.
func TestCodexRenewalRuntimeWriteFailure(t *testing.T) {
	e, api, local, options, _ := engineFixture(t)
	defer e.Store.Close()
	d, _, old, next := codexUpgradeFixture(t)
	d.NodePath = old.Path
	c, err := d.Detect(t.Context(), "codex", "agent@example.test")
	if err != nil {
		t.Fatal(err)
	}
	options.Candidates, options.Workspace = []Candidate{c}, d.Workspace
	approveFixture(t, e, api, options)
	if err := os.Remove(old.Path); err != nil {
		t.Fatal(err)
	}
	// Fake discovery never executes this script. The final OS version read
	// makes just the runtime artifact unwritable at the exact commit boundary.
	runtimePath := filepath.Join(e.Store.Path(), RuntimeName)
	quoted := "'" + strings.ReplaceAll(runtimePath, "'", "'\\''") + "'"
	if err := os.WriteFile(next.Path, []byte("#!/bin/sh\nchmod 0644 "+quoted+"\nprintf '%s\\n' v26.11.1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	d.NodePath = ""
	e.PinDiscovery = &d
	err = e.SyncFences(t.Context())
	if err == nil || !strings.Contains(err.Error(), "runtime update incomplete") {
		t.Fatal("did not reach the failed runtime commit", err)
	}
	if err := os.Chmod(runtimePath, 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.CodexPins.Renewals) != 0 || saved.Candidates[0].Candidate.Node != old {
		t.Fatal("failed runtime write published renewal metadata or advanced the saved pin")
	}
	config, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || config.Accounts[0].Node != old {
		t.Fatal("failed runtime write changed the installed pin", err)
	}
	blocked, _ := HarnessReport("codex", "blocked", PinInvalid)
	blocked.Attention = []AccountAttention{{AccountID: testAccount, Reason: PinInvalid}}
	local.states[""] = LocalStatus{DaemonID: "paired-daemon", AccountStatuses: map[string]HarnessDetail{testAccount: blocked}, HarnessDetails: map[string]HarnessDetail{"codex": blocked}}
	p, err := e.Status(t.Context())
	if err != nil || p.AccountStatuses[testAccount].Reason != PinInvalid || p.HarnessDetails["codex"].Fix != RecoveryFix("codex", PinInvalid) || !strings.Contains(p.Action, "aeon-agentd add-harness --harness codex") {
		t.Fatal("failed renewal hid its failure or repair command", err)
	}
	if err := os.WriteFile(next.Path, []byte("#!/bin/sh\nprintf '%s\\n' v26.11.1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncFences(t.Context()); err != nil {
		t.Fatal(err)
	}
	config, err = ReadRuntimeConfig(e.Store.Path())
	saved, saveErr := e.load()
	if err != nil || saveErr != nil || config.Accounts[0].Node != next || saved.Candidates[0].Candidate.Node != next || saved.CodexPins.Renewals[c.Key] != next {
		t.Fatal("failed runtime write prevented a subsequent safe renewal", err, saveErr)
	}
}

// Risk: renewal advice must belong to the committed pin's pending probe,
// never replace a new failure or reappear after that probe has finished.
func TestCodexRenewalProbeLifecycle(t *testing.T) {
	for _, scenario := range []string{PinInvalid, PinMissing, PinPartial, PinDrifted, PinUnsafe, "probe_failed", "probe_timeout", "login_required", "runtime pin changed"} {
		t.Run(scenario, func(t *testing.T) {
			e, api, local, options, _ := engineFixture(t)
			defer e.Store.Close()
			d, _, old, _ := codexUpgradeFixture(t)
			d.NodePath = old.Path
			c, err := d.Detect(t.Context(), "codex", "agent@example.test")
			if err != nil {
				t.Fatal(err)
			}
			options.Candidates, options.Workspace = []Candidate{c}, d.Workspace
			approveFixture(t, e, api, options)
			if err := os.Remove(old.Path); err != nil {
				t.Fatal(err)
			}
			d.NodePath = ""
			e.PinDiscovery = &d
			if err := e.SyncFences(t.Context()); err != nil {
				t.Fatal(err)
			}
			pending, _ := HarnessReport("codex", "blocked", "probe_pending")
			pending.Attention = []AccountAttention{{AccountID: testAccount, Reason: "probe_pending"}}
			local.states[""] = LocalStatus{DaemonID: "paired-daemon", AccountStatuses: map[string]HarnessDetail{testAccount: pending}, HarnessDetails: map[string]HarnessDetail{"codex": pending}}
			if err := e.SyncFences(t.Context()); err != nil {
				t.Fatal(err)
			}
			if detail := api.progress.HarnessDetails["codex"]; detail.Reason != PinRenewed || detail.ReasonDetail != ProbePinRenewed || detail.Fix != (HarnessFix{}) {
				t.Fatal("committed renewal absent from the pending probe")
			}
			state, reason, cause := "blocked", scenario, ""
			switch scenario {
			case "runtime pin changed":
				config, err := ReadRuntimeConfig(e.Store.Path())
				if err != nil {
					t.Fatal(err)
				}
				config.Accounts[0].Node.Version = "26.11.2"
				raw, _ := json.Marshal(config)
				if err := e.Store.Write(RuntimeName, raw, false); err != nil {
					t.Fatal(err)
				}
				reason = "probe_pending"
				// Keep automatic renewal out of this lifecycle assertion.
				e.PinDiscovery.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
			case "probe_failed":
				cause = ProbeCommandFailed
			case "login_required":
				state, cause = "login_required", ProbeDifferentIdentity
			}
			failure, _ := HarnessReport("codex", state, reason)
			failure = failure.WithProbeDetail("codex", cause)
			failure.Attention = []AccountAttention{{AccountID: testAccount, Reason: reason, ReasonDetail: cause}}
			local.states[""] = LocalStatus{DaemonID: "paired-daemon", AccountStatuses: map[string]HarnessDetail{testAccount: failure}, HarnessDetails: map[string]HarnessDetail{"codex": failure}}
			p, err := e.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, detail := range []HarnessDetail{p.AccountStatuses[testAccount], p.HarnessDetails["codex"]} {
				if detail.State != state || detail.Reason != reason || detail.ReasonDetail != cause || detail.Fix != RecoveryFix("codex", reason) || len(detail.Attention) != 1 || detail.Attention[0].Reason != reason || detail.Attention[0].ReasonDetail != cause {
					t.Fatal("renewal masked a current failure or a different runtime pin")
				}
			}
			if err := e.SyncFences(t.Context()); err != nil {
				t.Fatal(err)
			}
			saved, err := e.load()
			if err != nil || len(saved.CodexPins.Renewals) != 0 {
				t.Fatal("completed or replaced pin probe retained its renewal marker", err)
			}
			local.states[""] = LocalStatus{DaemonID: "paired-daemon", AccountStatuses: map[string]HarnessDetail{testAccount: pending}, HarnessDetails: map[string]HarnessDetail{"codex": pending}}
			p, err = e.Status(t.Context())
			if err != nil || p.AccountStatuses[testAccount].Reason != "probe_pending" || p.HarnessDetails["codex"].Reason != "probe_pending" {
				t.Fatal("completed renewal resurfaced during a later probe", err)
			}
		})
	}
}

func TestCodexNodeUpgradeRenewalFences(t *testing.T) {
	for _, pair := range []struct {
		old, next string
		same      bool
	}{
		{"/opt/homebrew/Cellar/node/26.10.0/bin/node", "/opt/homebrew/Cellar/node/26.11.1/bin/node", true},
		{"/opt/homebrew/Cellar/node/26.10.0/bin/node", "/opt/homebrew/Cellar/node@24/24.1.0/bin/node", false},
		{"/opt/homebrew/Cellar/node/26.10.0/bin/node", "/usr/local/Cellar/node/26.11.1/bin/node", false},
		{"/opt/homebrew/Cellar/node/26.10.0/bin/node", "/nix/store/synthetic-nodejs-26.11.1/bin/node", false},
		{"/nix/store/old-nodejs-26.10.0/bin/node", "/nix/store/new-nodejs-26.11.1/bin/node", true},
		{"/unrecognized/bin/node", "/unrecognized/bin/node", false},
	} {
		oldClass, nextClass := nodeLocationClass(pair.old), nodeLocationClass(pair.next)
		if (oldClass != "" && oldClass == nextClass) != pair.same {
			t.Fatal("interpreter location class crossed its package or prefix fence")
		}
	}
	for _, scenario := range []string{"automatic", "add harness", "healthy", "launcher content", "launcher during probe", "launcher during version read", "launcher path", "home", "symlink home", "home during probe", "identity", "location class", "unsafe candidate", "unsafe old pin", "missing launcher hash", "runtime owner", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			e, api, local, options, _ := engineFixture(t)
			defer e.Store.Close()
			d, launcher, old, next := codexUpgradeFixture(t)
			d.NodePath = old.Path
			c, err := d.Detect(t.Context(), "codex", "agent@example.test")
			if err != nil {
				t.Fatal(err)
			}
			options.Candidates, options.Workspace = []Candidate{c}, d.Workspace
			approveFixture(t, e, api, options)
			before, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "healthy" && scenario != "unsafe old pin" {
				if err := os.Remove(old.Path); err != nil {
					t.Fatal(err)
				}
			}
			d.NodePath = ""
			probes := 0
			d.CodexProbe = func(_ context.Context, path, home, node, _ string) (string, error) {
				probes++
				if path != launcher || home != c.Home || node != next.Path {
					t.Fatal("automatic probe did not use the candidate binding")
				}
				if scenario == "identity" {
					return "other@example.test", nil
				}
				if scenario == "launcher during probe" {
					if err := os.WriteFile(launcher, []byte("#!/bin/bash\n# changed during identity read\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "home during probe" {
					if err := os.Remove(c.Home); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(physicalTemp(t), c.Home); err != nil {
						t.Fatal(err)
					}
				}
				return c.Identity, nil
			}
			saved, err := e.load()
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "launcher content":
				if err := os.WriteFile(launcher, []byte("#!/bin/bash\n# changed guard\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "launcher path":
				before.Accounts[0].Path = next.Path
			case "launcher during version read":
				// The fake discovery executor does not execute this script; the
				// final OS version read does, establishing the exact interleaving.
				quoted := "'" + strings.ReplaceAll(launcher, "'", "'\\''") + "'"
				script := "#!/bin/sh\nprintf '%s\\n' '#!/bin/bash' '# changed during version read' > " + quoted + "\nprintf '%s\\n' v26.11.1\n"
				if err := os.WriteFile(next.Path, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "home":
				before.Accounts[0].Home = physicalTemp(t)
			case "symlink home":
				if err := os.Remove(c.Home); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(physicalTemp(t), c.Home); err != nil {
					t.Fatal(err)
				}
			case "location class":
				class := d.nodeClass
				d.nodeClass = func(path string) string {
					if path == next.Path {
						return "/nix/store/nodejs"
					}
					return class(path)
				}
			case "unsafe candidate":
				if err := os.Chmod(next.Path, 0777); err != nil {
					t.Fatal(err)
				}
			case "unsafe old pin":
				if err := os.Chmod(old.Path, 0777); err != nil {
					t.Fatal(err)
				}
			case "missing launcher hash":
				saved.CodexPins.LauncherHashes = nil
			case "runtime owner":
				before.PrincipalID = testComputer
			case "revoked":
				api.view.Enrollments[0].State = "revoked"
			}
			if err := e.save(saved, false); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(before)
			if err := e.Store.Write(RuntimeName, raw, false); err != nil {
				t.Fatal(err)
			}
			e.PinDiscovery = &d
			creates := api.createCount
			if scenario == "add harness" {
				d.NodePath = next.Path
				c, err = d.Detect(t.Context(), "codex", c.Identity)
				if err == nil {
					_, err = e.AddHarness(t.Context(), []Candidate{c})
				}
			} else {
				err = e.SyncFences(t.Context())
			}
			if scenario == "runtime owner" || scenario == "launcher during version read" {
				if err == nil {
					t.Fatal("changed binding accepted")
				}
				want := "automatic pin renewal runtime unavailable"
				if scenario == "launcher during version read" {
					want = "content changed during renewal"
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatal("renewal refused for the wrong reason", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := e.Store.Read(RuntimeName, 128<<10)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(e.Store.Path())
			if err != nil {
				t.Fatal(err)
			}
			journals := 0
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "pin-renewal-") {
					journals++
				}
			}
			if scenario != "automatic" && scenario != "add harness" {
				if !bytes.Equal(raw, after) || journals != 0 {
					t.Fatal("refused renewal changed the runtime or journal")
				}
				wantProbes := 0
				if scenario == "identity" || scenario == "launcher during probe" || scenario == "home during probe" || scenario == "launcher during version read" {
					wantProbes = 1
				}
				if probes != wantProbes {
					t.Fatal("renewal did not reach exactly its intended refusal fence")
				}
				return
			}
			if journals != 1 || probes != 1 || api.createCount != creates {
				t.Fatal("renewal was not exactly one local, identity-checked event")
			}
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), "pin-renewal-") {
					continue
				}
				raw, err := e.Store.Read(entry.Name(), 4096)
				var event struct {
					Kind      string `json:"kind"`
					AccountID string `json:"account_id"`
					Harness   string `json:"harness"`
					Old, New  harnesslaunch.Node
				}
				if err != nil || json.Unmarshal(raw, &event) != nil || event.Kind != "interpreter_pin_renewed" || event.Old != old || event.New != next || event.AccountID != testAccount || event.Harness != "codex" {
					t.Fatal("renewal journal lost the approved account or interpreter history", err)
				}
			}
			config, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil || config.Accounts[0].Node != next || len(AccountPinBlocks(config)) != 0 {
				t.Fatal("renewed pin remains blocked", err)
			}
			savedOptions, err := e.SavedOptions()
			if err != nil || savedOptions.Candidates[0].Node != next {
				t.Fatal("saved discovery would restore the removed Node", err)
			}
			blocked, _ := HarnessReport("codex", "blocked", PinInvalid)
			blocked.Attention = []AccountAttention{{AccountID: testAccount, Reason: PinInvalid}}
			pending, _ := HarnessReport("codex", "blocked", "probe_pending")
			pending.Attention = []AccountAttention{{AccountID: testAccount, Reason: "probe_pending"}}
			local.states[""] = LocalStatus{DaemonID: "paired-daemon", HarnessStatuses: map[string]string{"codex": "blocked"}, HarnessDetails: map[string]HarnessDetail{"codex": pending}, AccountStatuses: map[string]HarnessDetail{testAccount: pending}}
			if err := e.SyncFences(t.Context()); err != nil {
				t.Fatal(err)
			}
			detail := api.progress.HarnessDetails["codex"]
			if detail.Reason != PinRenewed || detail.ReasonDetail != ProbePinRenewed || len(detail.Attention) != 1 || detail.Attention[0].Reason != PinRenewed || detail.Attention[0].ReasonDetail != ProbePinRenewed || detail.Fix != (HarnessFix{}) {
				t.Fatal("renewal absent from safe attention details")
			}
			public, _ := json.Marshal(detail)
			if strings.Contains(string(public), next.Path) || strings.Contains(string(public), c.Identity) {
				t.Fatal("renewal attention leaked a local binding")
			}
			if probes != 1 {
				t.Fatal("a healthy pin was automatically replaced")
			}
			// The daemon normally reports checking while its fresh probe runs.
			checking, _ := HarnessReport("codex", "checking", "probe_pending")
			local.states[""] = LocalStatus{DaemonID: "paired-daemon", HarnessStatuses: map[string]string{"codex": "checking"}, HarnessDetails: map[string]HarnessDetail{"codex": checking}, AccountStatuses: map[string]HarnessDetail{testAccount: checking}}
			progress, err := e.Status(t.Context())
			if err != nil || progress.AccountStatuses[testAccount].State != "checking" || progress.AccountStatuses[testAccount].Reason != PinRenewed || progress.HarnessDetails["codex"].State != "checking" || progress.HarnessDetails["codex"].Reason != PinRenewed || !strings.Contains(progress.Action, "waiting for its sign-in and availability check") {
				t.Fatal("renewal changed pending readiness or lost the current probe", err)
			}
			ready, _ := HarnessReport("codex", "ready", "")
			local.states[""] = LocalStatus{Ready: true, DaemonID: "paired-daemon", HarnessStatuses: map[string]string{"codex": "ready"}, HarnessDetails: map[string]HarnessDetail{"codex": ready}, AccountStatuses: map[string]HarnessDetail{testAccount: ready}}
			if err := e.SyncFences(t.Context()); err != nil {
				t.Fatal(err)
			}
			if api.progress.HarnessDetails["codex"].ReasonDetail != "" {
				t.Fatal("ready report retained recovery diagnostics")
			}
			local.states[""] = LocalStatus{DaemonID: "paired-daemon", HarnessStatuses: map[string]string{"codex": "blocked"}, HarnessDetails: map[string]HarnessDetail{"codex": blocked}, AccountStatuses: map[string]HarnessDetail{testAccount: blocked}}
			progress, err = e.Status(t.Context())
			if err != nil || progress.HarnessDetails["codex"].Reason != PinInvalid || progress.HarnessDetails["codex"].ReasonDetail != "" {
				t.Fatal("past renewal hid a later failure", err)
			}
		})
	}
}

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
		{"drifted", PinDrifted, FixAddHarness, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: harnesslaunch.Node{Path: nodePath, Version: "22.20.0"}}, ""},
		{"invalid", PinInvalid, FixAddHarness, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: harnesslaunch.Node{Path: nodePath, Version: "not-a-version"}}, ""},
		{"partial", PinPartial, FixAddHarness, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: harnesslaunch.Node{Path: nodePath}}, ""},
		{"missing", PinMissing, FixAddHarness, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher}, ""},
		{"unsafe", PinUnsafe, FixAddHarness, RuntimeAccount{Harness: "codex", AccountID: "bad", Path: launcher, Node: node}, root},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := RuntimeConfig{Workspace: tc.workspace, Accounts: []RuntimeAccount{healthy, tc.bad}}
			if tc.name == "unsafe" {
				cfg.Accounts[0].Node = harnesslaunch.Node{}
				cfg.Accounts[0].Path = shell
			}
			blocks := AccountPinBlocks(cfg)
			if len(blocks) != 1 || blocks[0].AccountID != "bad" || blocks[0].Harness != "codex" || blocks[0].Reason != tc.reason || blocks[0].Fix.Kind != tc.fix {
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
	// Claude's missing shared pins are repaired by repin, which sets them.
	if len(blocks) != 1 || blocks[0].AccountID != "claude" || blocks[0].Reason != PinMissing || blocks[0].Fix != (HarnessFix{FixRepin, "aeon-agentd repin --harness claude"}) {
		t.Fatalf("claude dependency blocked the wrong account: %+v", blocks)
	}
	if ValidateRuntimeDependencies(claude) == nil {
		t.Fatal("missing claude pins became launchable")
	}
}

func TestStatusKeepsBlockedAccountReasonAndFix(t *testing.T) {
	e, a, l, o, _ := engineFixture(t)
	approveFixture(t, e, a, o)
	l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained", Ready: true, BlockedAccounts: []BlockedAccount{{AccountID: "old", Harness: "codex", Reason: PinDrifted, Fix: RecoveryFix("codex", PinDrifted)}}}
	p, err := e.Status(t.Context())
	if err != nil || p.Stage != "connected" || len(p.BlockedAccounts) != 1 || p.BlockedAccounts[0].AccountID != "old" || p.BlockedAccounts[0].Harness != "codex" || p.BlockedAccounts[0].Reason != PinDrifted || p.BlockedAccounts[0].Fix.Kind != FixAddHarness {
		t.Fatalf("connected status hid the blocked account: stage=%s blocks=%+v err=%v", p.Stage, p.BlockedAccounts, err)
	}
}

// add_harness is the fix for another harness's blocked pin. For the enrolled
// account it renews only that Node pin locally: no request, approval, identity
// or launcher change, and a healthy pin is never replaced.
func TestAddHarnessRenewsOnlyABlockedPin(t *testing.T) {
	e, api, _, options, _ := engineFixture(t)
	defer e.Store.Close()
	d, _, node := npmFixture(t, "codex")
	c, err := d.Detect(t.Context(), "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	options.Candidates = []Candidate{c}
	approveFixture(t, e, api, options)
	if _, err := e.AddHarness(t.Context(), []Candidate{c}); err == nil || !strings.Contains(err.Error(), "already connected") {
		t.Fatal("healthy pin renewed or re-requested", err)
	}
	drift := func() {
		t.Helper()
		config, err := ReadRuntimeConfig(e.Store.Path())
		if err != nil {
			t.Fatal(err)
		}
		config.Accounts[0].Node.Version = "22.20.0"
		raw, _ := json.Marshal(config)
		if err := e.Store.Write(RuntimeName, raw, false); err != nil {
			t.Fatal(err)
		}
		blocks := AccountPinBlocks(config)
		if len(blocks) != 1 || blocks[0].Reason != PinDrifted || blocks[0].Fix != (HarnessFix{FixAddHarness, "aeon-agentd add-harness --harness codex"}) {
			t.Fatalf("drift not classified with its fix: %+v", blocks)
		}
	}
	drift()
	other := c
	other.Identity = "someone-else@example.test"
	if _, err := e.AddHarness(t.Context(), []Candidate{other}); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatal("another identity renewed the enrolled pin", err)
	}
	creates := api.createCount
	p, err := e.AddHarness(t.Context(), []Candidate{c})
	if err != nil || !strings.Contains(p.Action, "Interpreter pin renewed") || api.createCount != creates {
		t.Fatal("blocked pin not renewed locally", err, p.Action)
	}
	config, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || len(config.Accounts) != 1 || config.Accounts[0].Node != node || len(AccountPinBlocks(config)) != 0 {
		t.Fatalf("runtime still blocked: %+v %v", config.Accounts, err)
	}
	saved, err := e.SavedOptions()
	if err != nil || len(saved.Candidates) != 1 || saved.Candidates[0].Node != node {
		t.Fatal("resume would restore the drifted pin", err)
	}
	if _, err := e.AddHarness(t.Context(), []Candidate{c}); err == nil || !strings.Contains(err.Error(), "already connected") {
		t.Fatal("renewed pin renewed again", err)
	}
}

func TestAddHarnessRenewsCodexWhileClaudeDependenciesAreUnavailable(t *testing.T) {
	e, api, _, o, _ := engineFixture(t)
	defer e.Store.Close()
	d, _, node := npmFixture(t, "codex")
	codex, err := d.Detect(t.Context(), "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	_, claudeNode, sdk := claudeFixture(t)
	shellDir := physicalTemp(t)
	shell := filepath.Join(shellDir, "claude")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	claude := Candidate{Harness: "claude", Label: "claude@example.test", Identity: "claude@example.test", Path: shell, Home: shellDir, Login: "signed_in", Version: "1.0.0"}
	o.Candidates = []Candidate{claude, codex}
	o.NodePath, o.ClaudeSDKPath = claudeNode, sdk
	approveFixture(t, e, api, o)
	before, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(before.ClaudeSDKPath); err != nil {
		t.Fatal(err)
	}
	config, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	codexIndex := -1
	for i, account := range config.Accounts {
		if account.Harness == "codex" {
			codexIndex = i
		}
	}
	if codexIndex < 0 {
		t.Fatal("codex enrollment missing")
	}
	config.Accounts[codexIndex].Node.Version = "22.20.0"
	raw, _ := json.Marshal(config)
	if err := e.Store.Write(RuntimeName, raw, false); err != nil {
		t.Fatal(err)
	}
	creates := api.createCount
	p, err := e.AddHarness(t.Context(), []Candidate{codex})
	if err != nil || !strings.Contains(p.Action, "Interpreter pin renewed") || api.createCount != creates {
		t.Fatal("codex renewal waited on claude", err, p.Action)
	}
	config, err = ReadRuntimeConfig(e.Store.Path())
	if err != nil {
		t.Fatal(err)
	}
	blocks := AccountPinBlocks(config)
	if len(blocks) != 1 || blocks[0].Harness != "claude" || blocks[0].Fix.Command != "aeon-agentd repin --harness claude" {
		t.Fatalf("claude block changed: %+v", blocks)
	}
	for _, account := range config.Accounts {
		if account.Harness == "codex" && account.Node != node {
			t.Fatalf("codex pin not renewed: %+v want %+v", account.Node, node)
		}
	}
	after, err := e.load()
	if err != nil || after.NodePath != before.NodePath || after.ClaudeSDKPath != before.ClaudeSDKPath {
		t.Fatal("renewal repinned claude", err)
	}
}

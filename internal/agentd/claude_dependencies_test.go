// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func claudeAdapterDependencies(t *testing.T, vendor string) (string, string) {
	t.Helper()
	node := filepath.Join(filepath.Dir(vendor), "node")
	raw, err := os.ReadFile(vendor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, raw, 0700); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(filepath.Dir(vendor), "lib", "node_modules", "@anthropic-ai", "claude-agent-sdk")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@anthropic-ai/claude-agent-sdk","main":"sdk.mjs","version":"1.2.3"}`), 0600); err != nil {
		t.Fatal(err)
	}
	sdk := filepath.Join(pkg, "sdk.mjs")
	if err := os.WriteFile(sdk, []byte("export {};"), 0600); err != nil {
		t.Fatal(err)
	}
	return node, sdk
}

func adapterTestLink(t *testing.T, path, target string) {
	t.Helper()
	if err := os.Symlink(target, path+".next"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeStartReresolvesStableLinksAndExecutesPhysicalPaths(t *testing.T) {
	r := adapterRequest(t)
	r.Profile.Harness = Claude
	path := fakeVendorPath(t, "claude")
	node, sdk := claudeAdapterDependencies(t, path)
	root := filepath.Dir(path)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	nodeLink, sdkLink := filepath.Join(root, "node-link"), filepath.Join(root, "sdk-link")
	adapterTestLink(t, nodeLink, node)
	adapterTestLink(t, sdkLink, sdk)
	a := NewClaudeAdapter(nodeLink, sdkLink, path, map[string]string{"account": root})
	for i := 0; i < 2; i++ {
		if i == 1 {
			next := fakeVendorPath(t, "claude")
			node, sdk = claudeAdapterDependencies(t, next)
			adapterTestLink(t, nodeLink, node)
			adapterTestLink(t, sdkLink, sdk)
		}
		p, err := a.Start(t.Context(), r, func(AdapterEvent) {})
		if err != nil {
			t.Fatal(err)
		}
		cp := p.(*claudeProcess)
		if cp.cmd.Path != node || cp.cmd.Args[2] != sdk {
			t.Fatal("launch did not use current physical dependency paths")
		}
		if err := p.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		_ = p.Wait()
	}
	adapterTestLink(t, nodeLink, filepath.Join(r.Workspace, "node"))
	if err := os.WriteFile(filepath.Join(r.Workspace, "node"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Start(t.Context(), r, func(AdapterEvent) {}); err == nil {
		t.Fatal("workspace retarget launched")
	}
	a.Workspace = r.Workspace
	if a.Probe(t.Context(), "account") {
		t.Fatal("unsafe configured workspace probed")
	}
}

func TestRestartClaudePreservesOtherAdaptersAndBusyProcesses(t *testing.T) {
	path := fakeVendorPath(t, "claude")
	node, sdk := claudeAdapterDependencies(t, path)
	old := NewClaudeAdapter(node, sdk, path, nil)
	next := NewClaudeAdapter(node, sdk, path, nil)
	other := NewCodexAdapter(path, nil)
	r := adapterRequest(t)
	s := &Supervisor{workspace: r.Workspace, daemonID: "daemon", generation: "current", accounts: []EnrolledAccount{{ID: "claude-account", Key: "key", Harness: Claude}}, adapters: map[string]Adapter{Claude: old, Codex: other}, runs: map[string]*owned{}, probedAccounts: map[string]bool{}, blockedAccounts: map[string]bool{}, loginRequired: map[string]bool{}}
	// Empty run set exercises reload without any process or service operation.
	// Lifecycle fence reads require the same private state as a real supervisor.
	state, err := agentsetup.OpenStore(r.StateRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	s.state = state
	if err := s.RestartClaude(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if s.adapters[Claude] != next || s.adapters[Codex] != other {
		t.Fatal("restart changed another adapter")
	}
	ownedRun := &owned{record: Record{RunID: "running", AccountID: "claude-account", Generation: "prior", State: "ownership_lost"}}
	s.runs["running"] = ownedRun
	if err := s.RestartClaude(context.Background(), old); err != nil {
		t.Fatal("historical ownership blocked adapter replacement", err)
	}
	if s.adapters[Claude] != old || s.runs["running"] != ownedRun || len(s.Lifecycle("").UnconfirmedRunIDs) != 1 {
		t.Fatal("restart erased historical ownership evidence")
	}
	proc := &fakeProcess{stopped: make(chan struct{})}
	ownedRun.record.Generation = s.generation
	ownedRun.process, ownedRun.record.State = proc, "running"
	if err := s.RestartClaude(context.Background(), next); !errors.Is(err, ErrDraining) {
		t.Fatal("active process allowed restart", err)
	}
	select {
	case <-proc.stopped:
		t.Fatal("repin stopped an active process")
	default:
	}
	ownedRun.record.State, ownedRun.record.ExitObserved = "completed", true
	if err := s.RestartClaude(context.Background(), old); err != nil {
		t.Fatal("settled process blocked restart", err)
	}
}

func TestClaudeDependencyBreakAndRepinHoldDoNotStarveCodex(t *testing.T) {
	for _, failure := range []string{"node", "sdk", "cli", "pending", "failed"} {
		t.Run(failure, func(t *testing.T) {
			s, api, _ := testSupervisor(t)
			path := fakeVendorPath(t, "claude")
			node, sdk := claudeAdapterDependencies(t, path)
			if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			claude := NewClaudeAdapter(node, sdk, path, map[string]string{"claude-local": filepath.Dir(path)})
			if err := s.RefreshAccounts([]EnrolledAccount{{ID: "claude-account", Key: "claude-local", Harness: Claude}}, []Adapter{claude}); err != nil {
				t.Fatal(err)
			}
			// Start with a successful probe, then break the installation in place.
			if available, err := claude.ProbeAccount(t.Context(), "claude-local"); !available || err != nil {
				t.Fatal("fixture is not initially healthy", err)
			}
			s.probedAccounts["claude-account"] = true
			switch failure {
			case "node", "sdk", "cli":
				broken := map[string]string{"node": node, "sdk": sdk, "cli": path}[failure]
				if err := os.Rename(broken, broken+".retired"); err != nil {
					t.Fatal(err)
				}
			case "pending", "failed":
				s.SetHarnessHold(Claude, "Claude repin "+failure)
			}
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if api.claims != 1 || len(api.routeAccounts) != 1 || api.routeAccounts[0] != "account" {
				t.Fatal("Claude failure starved Codex dispatch")
			}
			status := s.Lifecycle("")
			issue := status.HarnessErrors[Claude]
			if !status.Ready || status.LoginRequired || issue == "" || len(status.HarnessErrors) != 1 || status.HarnessStatuses[Claude] != "blocked" || status.HarnessStatuses[Codex] != "ready" {
				t.Fatalf("failure hidden or treated as login: %+v", status)
			}
			if (failure == "node" || failure == "sdk") && !strings.Contains(issue, "repin") {
				t.Fatal("dependency diagnostic has no repair action")
			}
			if s.accountAvailable("claude-account") {
				t.Fatal("Claude dispatch not held")
			}
			api.profile.Harness = Claude
			blocked := api.run
			blocked.ID = "new-claude-run"
			if err := s.StartRun(t.Context(), blocked); !errors.Is(err, ErrDraining) || api.claims != 1 {
				t.Fatal("direct Claude dispatch bypassed hold", err)
			}
			api.profile.Harness = Codex
			if failure == "node" || failure == "sdk" || failure == "cli" {
				broken := map[string]string{"node": node, "sdk": sdk, "cli": path}[failure]
				if err := os.Rename(broken+".retired", broken); err != nil {
					t.Fatal(err)
				}
			} else {
				s.SetHarnessHold(Claude, "")
			}
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if status := s.Lifecycle(""); !status.Ready || status.LoginRequired || len(status.HarnessErrors) != 0 || status.HarnessStatuses[Claude] != "ready" || status.HarnessStatuses[Codex] != "ready" {
				t.Fatalf("repaired Claude did not recover: %+v", status)
			}
		})
	}
}

func TestClaudeProbeAcceptsApprovedHomebrewCLI(t *testing.T) {
	path := fakeVendorPath(t, "claude")
	node, sdk := claudeAdapterDependencies(t, fakeVendorPath(t, "claude"))
	root := filepath.Dir(path)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0775); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "private-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := NewClaudeAdapter(node, sdk, path, map[string]string{"local": home})
	if available, err := a.ProbeAccount(t.Context(), "local"); err != nil || !available {
		t.Fatal("approved Homebrew-style CLI shown as signed out", err)
	}
}

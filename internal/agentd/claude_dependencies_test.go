// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	s := &Supervisor{workspace: r.Workspace, daemonID: "daemon", accounts: []EnrolledAccount{{ID: "claude-account", Key: "key", Harness: Claude}}, adapters: map[string]Adapter{Claude: old, Codex: other}, runs: map[string]*owned{}, probedAccounts: map[string]bool{}, blockedAccounts: map[string]bool{}, loginRequired: map[string]bool{}}
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
	ownedRun := &owned{record: Record{RunID: "running", AccountID: "claude-account", State: "ownership_lost"}}
	s.runs["running"] = ownedRun
	if err := s.RestartClaude(context.Background(), old); !errors.Is(err, ErrDraining) {
		t.Fatal("unconfirmed process allowed restart", err)
	}
	if s.adapters[Claude] != next || s.runs["running"] != ownedRun {
		t.Fatal("restart disturbed owned process")
	}
	proc := &fakeProcess{stopped: make(chan struct{})}
	ownedRun.process, ownedRun.record.State = proc, "running"
	if err := s.RestartClaude(context.Background(), old); !errors.Is(err, ErrDraining) {
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

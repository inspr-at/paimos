// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/piprobe"
)

func TestPairedPiAdapterBinding(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := piprobe.Node{Path: filepath.Join(root, "node"), Version: "22.19.0"}
	path := filepath.Join(root, "pi")
	if err := os.WriteFile(node.Path, []byte("#!/bin/sh\necho v22.19.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	c := agentsetup.RuntimeConfig{Accounts: []agentsetup.RuntimeAccount{{Harness: "pi", Key: "local", AccountID: "account", Home: root, Identity: "anthropic", Path: path, PiNode: node}}}
	accounts, adapters, err := pairedAdapters(c)
	if err != nil || len(accounts) != 1 || len(adapters) != 1 {
		t.Fatal("pi adapter missing", err)
	}
	pi, ok := adapters[0].(*agentd.PiAdapter)
	if !ok || pi.Nodes["local"] != node || pi.Providers["local"] != "anthropic" || pi.Homes["local"] != c.Accounts[0].Home || pi.Path != c.Accounts[0].Path || accounts[0].Harness != "pi" {
		t.Fatal("pi binding changed")
	}
	if pi.VerificationSupported() {
		t.Fatal("unqualified verification advertised")
	}
	c.Accounts[0].Identity = ""
	if _, _, err := pairedAdapters(c); err == nil {
		t.Fatal("missing pi provider accepted")
	}
	c.Accounts[0].Identity = "anthropic"
	c.Accounts[0].PiNode.Version = "22.20.0"
	accounts, adapters, err = pairedAdapters(c)
	if err != nil || len(accounts) != 1 || !accounts[0].DependencyBlocked || accounts[0].PinReason != agentsetup.PinDrifted || accounts[0].PinFix != agentsetup.FixRepin || len(adapters) != 0 {
		t.Fatal("drifted pi pin stopped the daemon or stayed launchable", err)
	}
	c.Accounts[0].PiNode = piprobe.Node{}
	accounts, adapters, err = pairedAdapters(c)
	if err != nil || len(accounts) != 1 || !accounts[0].DependencyBlocked || accounts[0].PinReason != agentsetup.PinMissing || accounts[0].PinFix != agentsetup.FixAddHarness || len(adapters) != 0 {
		t.Fatal("env-node entrypoint without a pin stopped the daemon or stayed launchable", err)
	}
	status := localStatus(agentd.LifecycleStatus{HarnessFailed: true})
	if !status.HarnessFailed || status.LoginRequired {
		t.Fatal("startup failure became login required")
	}
}

func TestPiNodeOptionDoesNotRebindClaude(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(root, "node")
	if err := os.WriteFile(node, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	pi := agentsetup.Candidate{Harness: "pi", PiNode: piprobe.Node{Path: node, Version: "22.19.0"}}
	claude := agentsetup.Candidate{Harness: "claude"}
	for _, tc := range []struct {
		name              string
		discovered, saved []agentsetup.Candidate
		want              string
	}{
		{"pi setup", []agentsetup.Candidate{pi}, nil, ""},
		{"add pi to Claude", []agentsetup.Candidate{pi}, []agentsetup.Candidate{claude}, ""},
		{"resume pi", nil, []agentsetup.Candidate{pi}, ""},
		{"setup both", []agentsetup.Candidate{pi, claude}, nil, node},
		{"resume Claude", nil, []agentsetup.Candidate{claude}, node},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claudeNodeOption(node, tc.discovered, tc.saved)
			if err != nil || got != tc.want {
				t.Fatal("Node option crossed harness bindings", err)
			}
		})
	}
	if _, err := claudeNodeOption(filepath.Join(root, "different-node"), nil, []agentsetup.Candidate{pi}); err == nil {
		t.Fatal("resume silently changed the pi interpreter pin")
	}
}

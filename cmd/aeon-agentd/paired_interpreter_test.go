// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

func TestPairedNpmBindings(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := harnesslaunch.Node{Path: filepath.Join(root, "node"), Version: "22.19.0"}
	if err := os.WriteFile(node.Path, []byte("#!/bin/sh\necho v22.19.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "launcher")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"codex", "cursor"} {
		c := agentsetup.RuntimeConfig{Accounts: []agentsetup.RuntimeAccount{{Harness: harness, Key: "local", AccountID: "account", Home: root, Identity: "42", Path: path, Node: node}}}
		_, adapters, err := pairedAdapters(c)
		if err != nil || len(adapters) != 1 {
			t.Fatal("adapter missing", err)
		}
		var got harnesslaunch.Node
		switch a := adapters[0].(type) {
		case *agentd.CodexAdapter:
			got = a.Nodes["local"]
		case *agentd.CursorAdapter:
			got = a.Nodes["local"]
		}
		if got != node {
			t.Fatal("adapter lost pin")
		}
		saved := []agentsetup.Candidate{{Harness: harness, Node: node}}
		if got, err := claudeNodeOption(node.Path, nil, saved); err != nil || got != "" {
			t.Fatal("npm pin became Claude dependency", err)
		}
		if _, err := claudeNodeOption(filepath.Join(root, "other-node"), nil, saved); err == nil {
			t.Fatal("resume rebound interpreter")
		}
		c.Accounts[0].Node = harnesslaunch.Node{}
		accounts, adapters, err := pairedAdapters(c)
		if err != nil || len(accounts) != 1 || !accounts[0].DependencyBlocked || len(adapters) != 0 {
			t.Fatal("legacy npm account without pin stopped the daemon or stayed launchable", err)
		}
	}
	pinned := agentsetup.RuntimeConfig{Accounts: []agentsetup.RuntimeAccount{{
		Harness: "cursor", Key: "cursor-local", AccountID: "cursor-account", Home: root, Identity: "42", Path: path, Node: node,
	}, {
		Harness: "codex", Key: "codex-local", AccountID: "codex-account", Home: root, Identity: "agent@example.test", Path: path,
	}}}
	accounts, adapters, err := pairedAdapters(pinned)
	if err != nil || len(accounts) != 2 || len(adapters) != 1 {
		t.Fatal("unpinned sibling stopped pairing", err)
	}
	if !accounts[1].DependencyBlocked || accounts[0].DependencyBlocked {
		t.Fatal("pin block applied to the wrong account")
	}
	cursor, ok := adapters[0].(*agentd.CursorAdapter)
	if !ok || cursor.Nodes["cursor-local"] != node {
		t.Fatal("pinned cursor binding lost")
	}
	drifted := pinned
	drifted.Accounts = append([]agentsetup.RuntimeAccount(nil), pinned.Accounts...)
	drifted.Accounts[0].Node.Version = "22.20.0"
	accounts, adapters, err = pairedAdapters(drifted)
	if err != nil || len(accounts) != 2 || !accounts[0].DependencyBlocked || accounts[0].PinReason != agentsetup.PinDrifted || accounts[0].PinFix != agentsetup.FixAddHarness || !accounts[1].DependencyBlocked || accounts[1].PinReason != agentsetup.PinMissing || len(adapters) != 0 {
		t.Fatal("drifted pin stopped the daemon or stayed launchable", err)
	}
	status := localStatus(agentd.LifecycleStatus{HarnessFailed: true, ProfilePermissions: true, BlockedAccounts: []agentsetup.BlockedAccount{{AccountID: "old", Harness: "cursor", Reason: agentsetup.PinDrifted, Fix: agentsetup.RecoveryFix("codex", agentsetup.PinDrifted)}}})
	if !status.HarnessFailed || !status.ProfilePermissions || status.LoginRequired || len(status.BlockedAccounts) != 1 || status.BlockedAccounts[0].Reason != agentsetup.PinDrifted || status.BlockedAccounts[0].Fix.Kind != agentsetup.FixAddHarness || status.BlockedAccounts[0].AccountID != "old" {
		t.Fatal("permission diagnostic or pin block lost")
	}
}

func TestPinProblemIsolatesAccount(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(root, "node")
	if err := os.WriteFile(nodePath, []byte("#!/bin/sh\necho v22.19.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	unsafePath := filepath.Join(root, "node-unsafe")
	if err := os.WriteFile(unsafePath, []byte("#!/bin/sh\necho v22.19.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "launcher")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	node := harnesslaunch.Node{Path: nodePath, Version: "22.19.0"}
	healthy := agentsetup.RuntimeAccount{Harness: "cursor", Key: "cursor-local", AccountID: "cursor-account", Home: root, Identity: "42", Path: launcher, Node: node}
	cases := []struct {
		name, reason, fix string
		bad               agentsetup.RuntimeAccount
	}{
		{"drifted", agentsetup.PinDrifted, agentsetup.FixAddHarness, agentsetup.RuntimeAccount{Harness: "codex", Key: "codex-local", AccountID: "codex-account", Home: root, Identity: "agent@example.test", Path: launcher, Node: harnesslaunch.Node{Path: nodePath, Version: "22.20.0"}}},
		{"invalid", agentsetup.PinInvalid, agentsetup.FixAddHarness, agentsetup.RuntimeAccount{Harness: "codex", Key: "codex-local", AccountID: "codex-account", Home: root, Identity: "agent@example.test", Path: launcher, Node: harnesslaunch.Node{Path: nodePath, Version: "not-a-version"}}},
		{"partial", agentsetup.PinPartial, agentsetup.FixAddHarness, agentsetup.RuntimeAccount{Harness: "codex", Key: "codex-local", AccountID: "codex-account", Home: root, Identity: "agent@example.test", Path: launcher, Node: harnesslaunch.Node{Path: nodePath}}},
		{"missing", agentsetup.PinMissing, agentsetup.FixAddHarness, agentsetup.RuntimeAccount{Harness: "codex", Key: "codex-local", AccountID: "codex-account", Home: root, Identity: "agent@example.test", Path: launcher}},
		{"unsafe", agentsetup.PinUnsafe, agentsetup.FixAddHarness, agentsetup.RuntimeAccount{Harness: "codex", Key: "codex-local", AccountID: "codex-account", Home: root, Identity: "agent@example.test", Path: launcher, Node: harnesslaunch.Node{Path: unsafePath, Version: "22.19.0"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unsafe" {
				if err := os.Chmod(unsafePath, 0777); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(unsafePath, 0700) })
			}
			accounts, adapters, err := pairedAdapters(agentsetup.RuntimeConfig{Accounts: []agentsetup.RuntimeAccount{healthy, tc.bad}})
			if err != nil || len(accounts) != 2 || len(adapters) != 1 {
				t.Fatal("pin problem stopped the daemon", err)
			}
			if accounts[0].DependencyBlocked || accounts[0].PinReason != "" || accounts[1].ID != "codex-account" || !accounts[1].DependencyBlocked || accounts[1].PinReason != tc.reason || accounts[1].PinFix != tc.fix {
				t.Fatalf("block applied to the wrong account: %+v", accounts)
			}
			cursor, ok := adapters[0].(*agentd.CursorAdapter)
			if !ok || cursor.Nodes["cursor-local"] != node {
				t.Fatal("healthy account did not stay launchable")
			}
		})
	}
}

func TestRuntimeDriftKeepsSiblingPolling(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	goodDir, driftDir := filepath.Join(root, "good"), filepath.Join(root, "drift")
	if err := os.Mkdir(goodDir, 0700); err != nil || os.Mkdir(driftDir, 0700) != nil {
		t.Fatal(err)
	}
	goodPath, driftPath := filepath.Join(goodDir, "node"), filepath.Join(driftDir, "node")
	for _, path := range []string{goodPath, driftPath} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho v22.19.0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(root, "launcher")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	current := agentsetup.RuntimeConfig{Accounts: []agentsetup.RuntimeAccount{{
		Harness: "cursor", Key: "cursor-local", AccountID: "cursor-account", Home: root, Identity: "42", Path: launcher,
		Node: harnesslaunch.Node{Path: goodPath, Version: "22.19.0"},
	}, {
		Harness: "codex", Key: "codex-local", AccountID: "codex-account", Home: root, Identity: "agent@example.test", Path: launcher,
		Node: harnesslaunch.Node{Path: driftPath, Version: "22.19.0"},
	}}}
	_, accounts, adapters, stop, poll, err := runtimeRefresh(current, current)
	if err != nil || stop || !poll || len(adapters) != 2 || accounts[0].DependencyBlocked || accounts[1].DependencyBlocked {
		t.Fatal("healthy pins suppressed polling", err, stop, poll)
	}
	if err := os.WriteFile(driftPath, []byte("#!/bin/sh\necho v22.20.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, accounts, adapters, stop, poll, err = runtimeRefresh(current, current)
	if err != nil || stop || !poll || len(adapters) != 1 {
		t.Fatal("runtime drift suppressed sibling polling", err, stop, poll)
	}
	if accounts[0].DependencyBlocked || !accounts[1].DependencyBlocked || accounts[1].PinReason != agentsetup.PinDrifted || accounts[1].PinFix != agentsetup.FixAddHarness {
		t.Fatalf("runtime drift blocked the wrong account: %+v", accounts)
	}
	if _, ok := adapters[0].(*agentd.CursorAdapter); !ok {
		t.Fatal("healthy account dropped after runtime drift")
	}
	changed := current
	changed.Accounts = append([]agentsetup.RuntimeAccount(nil), current.Accounts...)
	changed.Accounts[1].Home = filepath.Join(root, "other-home")
	if _, _, _, stop, poll, err = runtimeRefresh(current, changed); err == nil || !stop || poll {
		t.Fatal("non-pin binding change kept polling", err, stop, poll)
	}
	changed = current
	changed.Origin = "https://other.example.test"
	if _, _, _, stop, poll, err = runtimeRefresh(current, changed); err == nil || stop || poll {
		t.Fatal("identity change stopped the daemon or kept polling", err, stop, poll)
	}
}

func TestStatusTextKeepsPinFix(t *testing.T) {
	progress := agentsetup.Progress{Stage: "connected", Action: "Computer connected; ongoing limits remain separately controlled.",
		HarnessDetails:  map[string]agentsetup.HarnessDetail{"claude": {State: "blocked", Reason: agentsetup.PinDrifted, Fix: agentsetup.RecoveryFix("claude", agentsetup.PinDrifted)}, "codex": {State: "ready"}},
		BlockedAccounts: []agentsetup.BlockedAccount{{AccountID: "old", Harness: "codex", Reason: agentsetup.PinInvalid, Fix: agentsetup.RecoveryFix("codex", agentsetup.PinInvalid)}}}
	var text bytes.Buffer
	if err := printSetupProgress(&text, false, progress); err != nil || !strings.Contains(text.String(), "connected:") || !strings.Contains(text.String(), "harness claude blocked reason pin_drifted fix aeon-agentd repin --harness claude\n") || strings.Contains(text.String(), "\nharness codex") || !strings.Contains(text.String(), "blocked account old harness codex reason pin_invalid fix aeon-agentd add-harness --harness codex\n") {
		t.Fatal(text.String(), err)
	}
	var encoded bytes.Buffer
	if err := printSetupProgress(&encoded, true, progress); err != nil || !strings.Contains(encoded.String(), `"blocked_accounts"`) || !strings.Contains(encoded.String(), `"reason":"pin_invalid"`) || !strings.Contains(encoded.String(), `"fix":{"kind":"add_harness","command":"aeon-agentd add-harness --harness codex"}`) || !strings.Contains(encoded.String(), `"fix":{"kind":"repin","command":"aeon-agentd repin --harness claude"}`) {
		t.Fatal(encoded.String(), err)
	}
}

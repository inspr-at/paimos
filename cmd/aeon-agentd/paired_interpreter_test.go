// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"os"
	"path/filepath"
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
	drifted.Accounts[0].Node.Version = "22.20.0"
	if _, _, err := pairedAdapters(drifted); err == nil {
		t.Fatal("changed pin on another account was ignored")
	}
	status := localStatus(agentd.LifecycleStatus{HarnessFailed: true, ProfilePermissions: true})
	if !status.HarnessFailed || !status.ProfilePermissions || status.LoginRequired {
		t.Fatal("permission diagnostic lost")
	}
}

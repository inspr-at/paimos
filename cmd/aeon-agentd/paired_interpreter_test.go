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
		if _, _, err := pairedAdapters(c); err == nil {
			t.Fatal("legacy npm account without pin accepted")
		}
	}
	status := localStatus(agentd.LifecycleStatus{HarnessFailed: true, ProfilePermissions: true})
	if !status.HarnessFailed || !status.ProfilePermissions || status.LoginRequired {
		t.Fatal("permission diagnostic lost")
	}
}

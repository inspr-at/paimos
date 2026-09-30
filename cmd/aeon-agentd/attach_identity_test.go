// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestAttachFallbackRefreshCannotWidenApprovedRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "codex")
	if err = os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	old := agentsetup.RuntimeConfig{Workspace: workspace, Accounts: []agentsetup.RuntimeAccount{{Harness: "codex", Path: path}}}
	next := old
	next.RecordAttachIdentities()
	if !validAttachIdentityRefresh(old, next) {
		t.Fatal("local repair rejected")
	}
	next.AttachIdentities["codex"] = agentsetup.AttachIdentity{InstallRoot: root, Owner: os.Getuid()}
	if validAttachIdentityRefresh(old, next) {
		t.Fatal("broader install root accepted")
	}
	if _, _, _, _, _, err = runtimeRefresh(old, next); err == nil {
		t.Fatal("runtime adopted unapproved fallback")
	}
	next.AttachIdentities = map[string]agentsetup.AttachIdentity{"cursor": {InstallRoot: path, Owner: os.Getuid()}}
	if validAttachIdentityRefresh(old, next) {
		t.Fatal("unenrolled harness identity accepted")
	}
}

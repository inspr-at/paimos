// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
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

func TestAttachStartupRejectsUnapprovedFallbackBeforeRegistration(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(root, "codex")
	if err = os.WriteFile(image, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	c := agentsetup.RuntimeConfig{Origin: server.URL, Workspace: workspace, Accounts: []agentsetup.RuntimeAccount{{Harness: "codex", Path: image}}, AttachIdentities: map[string]agentsetup.AttachIdentity{"codex": {InstallRoot: root, Owner: os.Getuid()}}}
	remote := &agentd.Remote{Client: &client.Client{BaseURL: server.URL, HTTP: server.Client()}}
	if manager, err := pairedAttach(root, c, remote); err == nil || err.Error() != "attach fallback differs from the approved installation" || manager != nil || requests != 0 {
		if manager != nil {
			manager.Close(context.Background())
		}
		t.Fatal("startup registered an unapproved install root", err)
	}
}

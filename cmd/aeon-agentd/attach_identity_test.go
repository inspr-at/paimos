// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
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

func TestAttachStartupDropsOnlyUnverifiableFallbacks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(root, "codex")
	stale := filepath.Join(root, ".local", "share", "claude", "versions", "v1")
	if err = os.MkdirAll(filepath.Dir(stale), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{image, stale} {
		if err = os.WriteFile(path, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := agentsetup.RuntimeConfig{Workspace: workspace, Accounts: []agentsetup.RuntimeAccount{{Harness: "codex", Path: image}, {Harness: "claude", Path: stale}}}
	c.RecordAttachIdentities()
	healthy := c.AttachIdentities["codex"]
	if err = os.Rename(stale, stale+".unavailable"); err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{"missing-old-version", "unapproved-root", "wrong-owner", "unenrolled"} {
		t.Run(attack, func(t *testing.T) {
			next := c
			next.AttachIdentities = map[string]agentsetup.AttachIdentity{"codex": healthy, "claude": c.AttachIdentities["claude"]}
			switch attack {
			case "unapproved-root":
				next.Accounts = []agentsetup.RuntimeAccount{{Harness: "codex", Path: image}, {Harness: "claude", Path: image}}
				next.AttachIdentities["claude"] = agentsetup.AttachIdentity{InstallRoot: root, Owner: os.Getuid()}
			case "wrong-owner":
				next.Accounts = []agentsetup.RuntimeAccount{{Harness: "codex", Path: image}, {Harness: "claude", Path: image}}
				next.AttachIdentities["claude"] = agentsetup.AttachIdentity{InstallRoot: image, Owner: os.Getuid() + 10000, Exact: true}
			case "unenrolled":
				next.AttachIdentities["cursor"] = healthy
			}
			before := make(map[string]agentsetup.AttachIdentity)
			for harness, identity := range next.AttachIdentities {
				before[harness] = identity
			}
			got := startupAttachIdentities(next)
			if len(got) != 1 || got["codex"] != healthy {
				t.Fatal("startup retained an unverified fallback or dropped a healthy one")
			}
			if !reflect.DeepEqual(next.AttachIdentities, before) {
				t.Fatal("startup rewrote persisted pairing identities")
			}
		})
	}
}

func TestAttachStartupRegistersWithUnverifiableFallback(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	const tenant = "11111111-1111-4111-8111-111111111111"
	const computer = "22222222-2222-4222-8222-222222222222"
	const principal = "33333333-3333-4333-8333-333333333333"
	const request = "44444444-4444-4444-8444-444444444444"
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/agent-pairing/attach" {
			t.Error("unexpected registration path")
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"state":"registered","local_consent_proof_version":2}`))
	}))
	defer server.Close()
	view := agentsetup.View{RequestID: request, TenantID: tenant, ComputerID: computer, PrincipalID: principal}
	fixtureProof := strings.Repeat("a", 64)
	snapshot := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": server.URL, "request": map[string]string{"request_id": request, "computer_name": "fixture", "workspace_path": root}, "view": view, "device_secret": fixtureProof, "runtime_secret": fixtureProof, "lifecycle_secret": fixtureProof}
	raw, _ := json.Marshal(snapshot)
	store, err := agentsetup.OpenStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write("pairing.json", raw, true); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	c := agentsetup.RuntimeConfig{Origin: server.URL, TenantID: tenant, ComputerID: computer, PrincipalID: principal, Workspace: root, Accounts: []agentsetup.RuntimeAccount{{Harness: "claude", Path: filepath.Join(root, "missing-version")}, {Harness: "codex", Path: filepath.Join(root, "codex")}}, AttachIdentities: map[string]agentsetup.AttachIdentity{"claude": {InstallRoot: root, Owner: os.Getuid()}}}
	remote := &agentd.Remote{Client: &client.Client{BaseURL: server.URL, HTTP: server.Client()}}
	manager, err := pairedAttach(root, c, remote)
	if manager != nil {
		defer manager.Close(context.Background())
	}
	if err != nil || manager == nil || requests.Load() != 1 {
		t.Fatal("one unavailable fallback disabled attach for every harness", err)
	}
}

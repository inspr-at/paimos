// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

type claudeOwnerFixture struct {
	os.FileInfo
	uid uint32
}

func (f claudeOwnerFixture) Sys() any { return &syscall.Stat_t{Uid: f.uid} }

func TestClaudeOwnershipRejectsForeignUIDWithoutPrivileges(t *testing.T) {
	_, node, _ := claudeFixture(t)
	info, err := os.Stat(node)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []uint32{0, uint32(os.Getuid())} {
		if !trustedClaudeOwner(claudeOwnerFixture{info, uid}) {
			t.Fatal("rejected trusted owner")
		}
	}
	if trustedClaudeOwner(claudeOwnerFixture{info, uint32(os.Getuid() + 10000)}) {
		t.Fatal("accepted foreign owner")
	}
}

func replaceTestLink(t *testing.T, link, target string) {
	t.Helper()
	tmp := link + ".next"
	if err := os.Symlink(target, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, link); err != nil {
		t.Fatal(err)
	}
}

func stableClaudeFixture(t *testing.T, node, sdk string) ClaudeDependencies {
	t.Helper()
	root := physicalTemp(t)
	pins := ClaudeDependencies{NodePath: filepath.Join(root, "bin", "node"), SDKPath: filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-agent-sdk", "sdk.mjs")}
	for _, dir := range []string{filepath.Dir(pins.NodePath), filepath.Dir(filepath.Dir(pins.SDKPath))} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	replaceTestLink(t, pins.NodePath, node)
	replaceTestLink(t, filepath.Dir(pins.SDKPath), filepath.Dir(sdk))
	return pins
}

func versionedClaudeFixture(t *testing.T, version string) (string, string, string) {
	t.Helper()
	root, node, sdk := claudeFixture(t)
	if err := os.WriteFile(node, []byte("#!/bin/sh\n[ \"$1\" = --version ] || exit 91\n[ -z \"$NODE_OPTIONS\" ] || exit 92\nprintf '%s\\n' v"+version+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"name": "@anthropic-ai/claude-agent-sdk", "main": "sdk.mjs", "version": version})
	if err := os.WriteFile(filepath.Join(filepath.Dir(sdk), "package.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return root, node, sdk
}

func TestClaudeStableLinksSurviveRetargetAndOldInstallationRemoval(t *testing.T) {
	oldRoot, node, sdk := claudeFixture(t)
	pins := stableClaudeFixture(t, node, sdk)
	workspace := physicalTemp(t)
	d := Discovery{LookPath: func(string) (string, error) { return pins.NodePath, nil }}
	got, err := d.ResolveClaudeDependencies(ClaudeDependencies{}, workspace)
	if err != nil || got != pins {
		t.Fatalf("stable discovery: %+v %v", got, err)
	}
	e, api, _, opts, _ := engineFixture(t)
	opts.Workspace, opts.Candidates[0].Harness = workspace, "claude"
	opts.NodePath, opts.ClaudeSDKPath = pins.NodePath, pins.SDKPath
	approveFixture(t, e, api, opts)
	_, newNode, newSDK := claudeFixture(t)
	replaceTestLink(t, pins.NodePath, newNode)
	replaceTestLink(t, filepath.Dir(pins.SDKPath), filepath.Dir(newSDK))
	// Renaming our fixture models GC without touching any real installation.
	if err := os.Rename(oldRoot, oldRoot+"-retired"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(oldRoot+"-retired", oldRoot) })
	resolved, err := ResolveClaudeRuntime(got, workspace)
	if err != nil || resolved != (ClaudeDependencies{NodePath: newNode, SDKPath: newSDK}) {
		t.Fatalf("retarget: %+v %v", resolved, err)
	}
	if err := ValidateRuntimeDependencies(RuntimeConfig{Workspace: workspace, NodePath: got.NodePath, ClaudeSDKPath: got.SDKPath, Accounts: []RuntimeAccount{{Harness: "claude", Path: newNode}}}); err != nil {
		t.Fatal(err)
	}
	opts.NodePath, opts.ClaudeSDKPath = "", ""
	if _, err := e.Begin(t.Context(), opts); err != nil {
		t.Fatal("resume after link update", err)
	}
	c, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || c.NodePath != pins.NodePath || c.ClaudeSDKPath != pins.SDKPath || api.createCount != 1 {
		t.Fatal("stable pins lost or update required a new enrollment")
	}
}

func TestResolveOwnedPathReturnsFinalInfoForParentLink(t *testing.T) {
	root := physicalTemp(t)
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink("child/..", link); err != nil {
		t.Fatal(err)
	}
	physical, got, err := resolveOwnedPath(link, "")
	want, statErr := os.Stat(root)
	if err != nil || statErr != nil || physical != root || !os.SameFile(got, want) {
		t.Fatal("returned metadata for an intermediate component", err, statErr)
	}
}

func TestClaudeCLIUsesExistingPolicyAndReportsBrokenExecutable(t *testing.T) {
	_, node, sdk := claudeFixture(t)
	brew := physicalTemp(t)
	if err := os.Chmod(brew, 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(brew, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(brew, "claude")
	if err := os.WriteFile(cli, []byte("synthetic executable"), 0755); err != nil {
		t.Fatal(err)
	}
	c := RuntimeConfig{Workspace: physicalTemp(t), NodePath: node, ClaudeSDKPath: sdk, Accounts: []RuntimeAccount{{Harness: "claude", Path: cli}}}
	if err := ValidateRuntimeDependencies(c); err != nil {
		t.Fatal("Homebrew-style approved CLI rejected", err)
	}
	if err := os.Chmod(cli, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimeDependencies(c); err == nil || !strings.Contains(err.Error(), "Claude CLI executable") || strings.Contains(err.Error(), "login") {
		t.Fatal("invalid CLI lacks a specific diagnostic", err)
	}
	if _, err := ResolveClaudeExecutable(filepath.Join(brew, "missing"), ""); err == nil {
		t.Fatal("missing CLI accepted")
	}
}

func TestClaudeRetargetFailsClosed(t *testing.T) {
	for _, kind := range []string{"workspace-target", "sdk-workspace", "workspace-link", "intermediate-workspace", "writable-target", "writable-link-parent", "writable-intermediate", "dangling", "cycle", "wrong-entry", "foreign-target", "foreign-link"} {
		t.Run(kind, func(t *testing.T) {
			_, node, sdk := claudeFixture(t)
			pins := stableClaudeFixture(t, node, sdk)
			workspace, newNode, newSDK := claudeFixture(t)
			if _, err := ResolveClaudeRuntime(pins, workspace); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "workspace-target":
				replaceTestLink(t, pins.NodePath, newNode)
			case "sdk-workspace":
				replaceTestLink(t, filepath.Dir(pins.SDKPath), filepath.Dir(newSDK))
			case "workspace-link":
				pins.NodePath = filepath.Join(workspace, "node-link")
				replaceTestLink(t, pins.NodePath, node)
			case "intermediate-workspace":
				middle := filepath.Join(workspace, "node-link")
				replaceTestLink(t, middle, node)
				replaceTestLink(t, pins.NodePath, middle)
			case "writable-target":
				if err := os.Chmod(filepath.Dir(node), 0777); err != nil {
					t.Fatal(err)
				}
			case "writable-link-parent":
				if err := os.Chmod(filepath.Dir(pins.NodePath), 0777); err != nil {
					t.Fatal(err)
				}
			case "writable-intermediate":
				middle := filepath.Join(physicalTemp(t), "node")
				replaceTestLink(t, middle, node)
				if err := os.Chmod(filepath.Dir(middle), 0777); err != nil {
					t.Fatal(err)
				}
				replaceTestLink(t, pins.NodePath, middle)
			case "dangling":
				replaceTestLink(t, pins.NodePath, filepath.Join(physicalTemp(t), "missing"))
			case "cycle":
				replaceTestLink(t, pins.NodePath, pins.NodePath)
			case "wrong-entry":
				other := filepath.Join(filepath.Dir(sdk), "other.mjs")
				if err := os.WriteFile(other, []byte("module"), 0600); err != nil {
					t.Fatal(err)
				}
				pins.SDKPath = filepath.Join(filepath.Dir(pins.NodePath), "sdk-entry")
				replaceTestLink(t, pins.SDKPath, other)
			case "foreign-target", "foreign-link":
				path := node
				if kind == "foreign-link" {
					path = pins.NodePath
				}
				if err := os.Lchown(path, os.Getuid()+10000, -1); err != nil {
					t.Skip("foreign ownership requires chown privilege")
				}
				t.Cleanup(func() { _ = os.Lchown(path, os.Getuid(), -1) })
			}
			if _, err := ResolveClaudeRuntime(pins, workspace); err == nil {
				t.Fatal("unsafe retarget accepted")
			}
			// The SDK link must apply the workspace check independently of Node.
			pins.SDKPath = newSDK
			if _, err := ResolveClaudeRuntime(pins, workspace); err == nil {
				t.Fatal("workspace SDK accepted")
			}
		})
	}
}

func TestClaudeRepinRecoversMissingPinsPreservingPairing(t *testing.T) {
	e, api, _, opts, _ := engineFixture(t)
	oldRoot, node, sdk := versionedClaudeFixture(t, "1.2.3")
	opts.Candidates[0].Harness = "claude"
	opts.NodePath, opts.ClaudeSDKPath = node, sdk
	approveFixture(t, e, api, opts)
	before, _ := e.load()
	runtimeBefore, _ := ReadRuntimeConfig(e.Store.Path())
	if err := os.Rename(oldRoot, oldRoot+"-retired"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(oldRoot+"-retired", oldRoot) })
	if _, err := e.AddHarness(t.Context(), opts.Candidates); err == nil || !strings.Contains(err.Error(), "repin --harness claude") {
		t.Fatal("missing actionable repin error")
	}
	_, node, sdk = versionedClaudeFixture(t, "2.3.4")
	pins := stableClaudeFixture(t, node, sdk)
	t.Setenv("NODE_OPTIONS", "must not reach version probe")
	plan, err := e.PrepareClaudeRepin(t.Context(), Discovery{}, pins)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Old.NodeVersion != "unavailable" || plan.New.NodeVersion != "v2.3.4" || plan.New.SDKVersion != "2.3.4" {
		t.Fatal("version preview wrong")
	}
	id, err := e.ApplyClaudeRepin(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := e.load()
	if after.Device != before.Device || after.Runtime != before.Runtime || after.Lifecycle != before.Lifecycle || !reflect.DeepEqual(after.Request, before.Request) || !reflect.DeepEqual(after.Candidates, before.Candidates) || !reflect.DeepEqual(after.Service, before.Service) || api.createCount != 1 {
		t.Fatal("repin changed pairing authority or enrollment")
	}
	runtimeAfter, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || runtimeAfter.NodePath != pins.NodePath || runtimeAfter.ClaudeSDKPath != pins.SDKPath || runtimeAfter.ClaudeRepinID != id {
		t.Fatal("runtime pins not published")
	}
	runtimeAfter.NodePath, runtimeAfter.ClaudeSDKPath, runtimeAfter.ClaudeRepinID = runtimeBefore.NodePath, runtimeBefore.ClaudeSDKPath, runtimeBefore.ClaudeRepinID
	if !reflect.DeepEqual(runtimeAfter, runtimeBefore) {
		t.Fatal("repin changed other runtime fields")
	}
	raw, err := e.Store.Read("claude-repin-"+id+".json", 64<<10)
	if err != nil || !bytes.Contains(raw, []byte("claude_dependencies_repin_requested")) {
		t.Fatal("missing local audit event")
	}
	if bytes.Contains(raw, []byte(before.Runtime)) || bytes.Contains(raw, []byte(before.Lifecycle)) {
		t.Fatal("audit event contains authority")
	}
	if ok, err := ClaudeRepinApplied(e.Store, id); err != nil || ok {
		t.Fatal("claimed restart before acknowledgement")
	}
	c, _ := ReadRuntimeConfig(e.Store.Path())
	if err := AcknowledgeClaudeRepin(e.Store.Path(), c); err != nil {
		t.Fatal(err)
	}
	if ok, err := ClaudeRepinApplied(e.Store, id); err != nil || !ok {
		t.Fatal("restart acknowledgement missing")
	}
	if err := AcknowledgeClaudeRepin(e.Store.Path(), c); err != nil {
		t.Fatal("acknowledgement not idempotent", err)
	}
}

func TestClaudeRepinPreviewCannotAuthorizeLaterChanges(t *testing.T) {
	for _, change := range []string{"retarget", "pairing", "runtime", "revoked"} {
		t.Run(change, func(t *testing.T) {
			e, api, _, opts, _ := engineFixture(t)
			_, node, sdk := versionedClaudeFixture(t, "1.2.3")
			opts.Candidates[0].Harness = "claude"
			opts.NodePath, opts.ClaudeSDKPath = node, sdk
			approveFixture(t, e, api, opts)
			pins := stableClaudeFixture(t, node, sdk)
			plan, err := e.PrepareClaudeRepin(t.Context(), Discovery{}, pins)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "retarget":
				_, node, _ = versionedClaudeFixture(t, "3.4.5")
				replaceTestLink(t, pins.NodePath, node)
			case "pairing", "revoked":
				s, _ := e.load()
				s.NextPoll = e.now()
				if change == "revoked" {
					s.DisconnectAll = true
				}
				if err := e.save(s, false); err != nil {
					t.Fatal(err)
				}
			case "runtime":
				c, _ := ReadRuntimeConfig(e.Store.Path())
				c.ComputerID = otherAccount
				raw, _ := json.Marshal(c)
				if err := e.Store.Write(RuntimeName, raw, false); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := e.Store.Read(snapshotName, 1<<20)
			if _, err := e.ApplyClaudeRepin(t.Context(), plan); err == nil {
				t.Fatal("stale confirmation accepted")
			}
			after, _ := e.Store.Read(snapshotName, 1<<20)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected plan changed pairing")
			}
		})
	}
}

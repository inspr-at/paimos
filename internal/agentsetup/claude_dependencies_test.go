// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claudeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := physicalTemp(t)
	node := filepath.Join(root, "bin", "node")
	pkg := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-agent-sdk")
	if err := os.MkdirAll(filepath.Dir(node), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, []byte("synthetic executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@anthropic-ai/claude-agent-sdk","exports":{".":{"default":"./sdk.mjs"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	sdk := filepath.Join(pkg, "sdk.mjs")
	if err := os.WriteFile(sdk, []byte("synthetic module"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, node, sdk
}

func TestClaudeDependencyDiscoveryAndValidation(t *testing.T) {
	root, node, sdk := claudeFixture(t)
	workspace := physicalTemp(t)
	d := Discovery{Home: root, LookPath: func(name string) (string, error) {
		if name != "node" {
			t.Fatal("unexpected executable lookup")
		}
		return node, nil
	}}
	got, err := d.ResolveClaudeDependencies(ClaudeDependencies{}, workspace)
	if err != nil || got != (ClaudeDependencies{NodePath: node, SDKPath: sdk}) {
		t.Fatalf("safe global metadata discovery failed: %+v %v", got, err)
	}
	got, err = (Discovery{}).ResolveClaudeDependencies(got, workspace)
	if err != nil || got.NodePath != node || got.SDKPath != sdk {
		t.Fatal("explicit installed paths were not preserved")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(sdk), "package.json"), []byte(`{"name":"@anthropic-ai/sdk","main":"sdk.mjs"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolveClaudeDependencies(ClaudeDependencies{NodePath: node, SDKPath: sdk}, workspace); err == nil {
		t.Fatal("accepted unrelated SDK metadata")
	}
}

func TestClaudeDependencyFailuresAreSetupBlockers(t *testing.T) {
	workspace := physicalTemp(t)
	d := Discovery{LookPath: func(string) (string, error) { return "", errors.New("missing") }}
	if _, err := d.ResolveClaudeDependencies(ClaudeDependencies{}, workspace); err == nil || !strings.Contains(err.Error(), "dependencies required") || strings.Contains(err.Error(), "login") {
		t.Fatal("missing Node was treated as vendor login")
	}
	_, node, sdk := claudeFixture(t)
	if _, err := d.ResolveClaudeDependencies(ClaudeDependencies{NodePath: node, SDKPath: sdk}, filepath.Dir(node)); err == nil {
		t.Fatal("accepted workspace-controlled executable")
	}
	if err := os.Chmod(node, 0722); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolveClaudeDependencies(ClaudeDependencies{NodePath: node, SDKPath: sdk}, workspace); err == nil {
		t.Fatal("accepted group-writable Node")
	}
}

func TestClaudeSetupPinsSurviveResumeAndOtherHarness(t *testing.T) {
	e, a, _, o, _ := engineFixture(t)
	_, node, sdk := claudeFixture(t)
	o.Candidates[0].Harness = "claude"
	o.NodePath, o.ClaudeSDKPath = node, sdk
	approveFixture(t, e, a, o)
	before, err := e.load()
	if err != nil {
		t.Fatal(err)
	}
	if before.NodePath != node || before.ClaudeSDKPath != sdk {
		t.Fatal("setup did not persist dependency pins")
	}
	runtime, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || runtime.NodePath != node || runtime.ClaudeSDKPath != sdk {
		t.Fatal("runtime lost dependency pins")
	}
	if err := os.Chmod(sdk, 0622); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRuntimeConfig(e.Store.Path()); err == nil {
		t.Fatal("restart accepted unsafe SDK module")
	}
	if err := os.Chmod(sdk, 0600); err != nil {
		t.Fatal(err)
	}
	o.NodePath, o.ClaudeSDKPath = "", ""
	if _, err := e.Begin(t.Context(), o); err != nil {
		t.Fatal("resume rejected saved dependencies", err)
	}
	_, otherNode, _ := claudeFixture(t)
	o.NodePath = otherNode
	if _, err := e.Begin(t.Context(), o); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatal("conflicting resume override changed pins")
	}
	c := Candidate{Harness: "cursor", Label: "other@example.test", Identity: "other", Path: o.Candidates[0].Path, Login: "signed_in"}
	e.ClaudeDependencies = ClaudeDependencies{NodePath: otherNode}
	if _, err := e.AddHarness(t.Context(), []Candidate{c}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatal("conflicting Add harness override accepted")
	}
	after, _ := e.load()
	if before.Request.RequestID != after.Request.RequestID || before.NodePath != after.NodePath || a.createCount != 1 {
		t.Fatal("conflict mutated enrollment")
	}
	e.ClaudeDependencies = ClaudeDependencies{}
	if _, err := e.AddHarness(t.Context(), []Candidate{c}); err != nil {
		t.Fatal(err)
	}
	after, _ = e.load()
	if after.NodePath != node || after.ClaudeSDKPath != sdk {
		t.Fatal("adding another harness repinned Claude")
	}
}

func TestAddClaudeValidatesBeforeApprovalAndPersistsPins(t *testing.T) {
	e, a, _, o, _ := engineFixture(t)
	approveFixture(t, e, a, o)
	before, _ := e.load()
	c := Candidate{Harness: "claude", Label: "claude@example.test", Identity: "claude@example.test", Path: o.Candidates[0].Path, Login: "signed_in"}
	e.ClaudeDependencies = ClaudeDependencies{NodePath: "/missing/node", SDKPath: "/missing/sdk.mjs"}
	if p, err := e.AddHarness(t.Context(), []Candidate{c}); err == nil || p.Stage != "blocked" {
		t.Fatal("missing dependencies requested approval")
	}
	unchanged, _ := e.load()
	if unchanged.Request.RequestID != before.Request.RequestID || a.createCount != 1 {
		t.Fatal("dependency failure changed enrollment")
	}
	_, node, sdk := claudeFixture(t)
	e.ClaudeDependencies = ClaudeDependencies{NodePath: node, SDKPath: sdk}
	if _, err := e.AddHarness(t.Context(), []Candidate{c}); err != nil {
		t.Fatal(err)
	}
	pending, _ := e.load()
	if pending.NodePath != node || pending.ClaudeSDKPath != sdk {
		t.Fatal("Add harness did not save validated pins")
	}
	a.approved = true
	e.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := e.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtime, err := ReadRuntimeConfig(e.Store.Path())
	if err != nil || runtime.NodePath != node || runtime.ClaudeSDKPath != sdk {
		t.Fatal("Add harness did not carry pins into runtime")
	}
}

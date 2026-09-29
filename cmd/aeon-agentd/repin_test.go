// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func repinCommandFixture(t *testing.T) (*agentsetup.Engine, agentsetup.ClaudeDependencies) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentsetup.OpenStore(filepath.Join(root, "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	const tenant = "11111111-1111-4111-8111-111111111111"
	const principal = "22222222-2222-4222-8222-222222222222"
	const computer = "33333333-3333-4333-8333-333333333333"
	const account = "44444444-4444-4444-8444-444444444444"
	request := agentsetup.DeviceRequest{RequestID: tenant, Details: agentsetup.Details{Workspace: workspace}}
	c := agentsetup.RuntimeConfig{Schema: "aeon.agent-runtime.v1", Origin: "https://repin.example.test", TenantID: tenant, PrincipalID: principal, ComputerID: computer, DaemonID: "fixture", Workspace: workspace, NodePath: "/missing/node", ClaudeSDKPath: "/missing/sdk.mjs", Accounts: []agentsetup.RuntimeAccount{{Harness: "claude", AccountID: account, Key: "fixture-account", Path: filepath.Join(root, "node")}}}
	// Synthetic pairing state only. No real daemon or vendor credentials.
	s := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": c.Origin, "phase": "connected", "request": request, "device_secret": strings.Repeat("a", 64), "runtime_secret": strings.Repeat("b", 64), "lifecycle_secret": strings.Repeat("c", 64), "bound_computer_id": computer, "bound_principal_id": principal, "bound_daemon_id": "fixture", "node_path": c.NodePath, "claude_sdk_path": c.ClaudeSDKPath, "view": agentsetup.View{ComputerID: computer, PrincipalID: principal, TenantID: tenant, DaemonID: "fixture", ComputerState: "connected", Enrollments: []agentsetup.Enrollment{{AccountID: account, AccountKey: "fixture-account", Harness: "claude", State: "connected"}}}}
	for name, value := range map[string]any{"pairing.json": s, agentsetup.RuntimeName: c} {
		raw, _ := json.Marshal(value)
		if err := store.Write(name, raw, true); err != nil {
			t.Fatal(err)
		}
	}
	node := filepath.Join(root, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\necho v24.1.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-agent-sdk")
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
	return &agentsetup.Engine{Store: store}, agentsetup.ClaudeDependencies{NodePath: node, SDKPath: sdk}
}

func TestRepinCommandDeclineAndJSONRequireConfirmation(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "declined", true: "json-needs-yes"}[jsonOutput], func(t *testing.T) {
			e, pins := repinCommandFixture(t)
			before, _ := e.Store.Read("pairing.json", 1<<20)
			var out bytes.Buffer
			err := setupCommandInput("repin", append([]string{"--state-root", e.Store.Path(), "--harness", "claude", "--node-path", pins.NodePath, "--claude-sdk-path", pins.SDKPath}, map[bool][]string{true: {"--json"}, false: {}}[jsonOutput]...), strings.NewReader("no\n"), &out)
			if err == nil {
				t.Fatal("repin applied without confirmation")
			}
			if jsonOutput && !strings.Contains(err.Error(), "--yes") {
				t.Fatal("missing automation guidance", err)
			}
			if !strings.Contains(out.String(), "v24.1.0") || !strings.Contains(out.String(), "unavailable") {
				t.Fatal("missing old/new version preview")
			}
			after, _ := e.Store.Read("pairing.json", 1<<20)
			if !bytes.Equal(before, after) {
				t.Fatal("unconfirmed repin changed pairing")
			}
		})
	}
}

func TestRepinCommandYesWaitsForDaemonReceipt(t *testing.T) {
	e, pins := repinCommandFixture(t)
	ack := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			c, err := agentsetup.ReadRuntimeConfig(e.Store.Path())
			if err == nil && c.ClaudeRepinID != "" {
				// Model the daemon's idle-restart acknowledgement; production
				// RestartClaude ownership gates have separate supervisor tests.
				err = agentsetup.AcknowledgeClaudeRepin(e.Store.Path(), c)
				if err == nil {
					ack <- nil
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		ack <- context.DeadlineExceeded
	}()
	var out bytes.Buffer
	err := setupCommandInput("repin", []string{"--state-root", e.Store.Path(), "--harness", "claude", "--node-path", pins.NodePath, "--claude-sdk-path", pins.SDKPath, "--yes", "--json"}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-ack; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"stage":"repinned"`) || !strings.Contains(out.String(), `"stage":"repin_preview"`) {
		t.Fatal("command lacks preview/restart receipt")
	}
}

func TestRepinCommandNeverClaimsOfflineRestart(t *testing.T) {
	e, pins := repinCommandFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	// Cancel after the runtime write, so the version probe and commit complete.
	go func() {
		for ctx.Err() == nil {
			c, err := agentsetup.ReadRuntimeConfig(e.Store.Path())
			if err == nil && c.ClaudeRepinID != "" {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	defer cancel()
	var out bytes.Buffer
	err := repinClaude(ctx, e, agentsetup.Discovery{}, pins, setupPrompt{in: bufio.NewReader(strings.NewReader("")), out: &out}, true)
	if err == nil || !strings.Contains(err.Error(), "restart remains pending") || strings.Contains(out.String(), "repinned:") {
		t.Fatal("offline restart incorrectly claimed", err)
	}
}

func TestRepinCommandRejectsOtherScopes(t *testing.T) {
	for _, args := range [][]string{{}, {"--harness", "codex"}, {"--harness", "claude", "--workspace", "/tmp"}, {"--harness", "claude", "--start-service"}, {"--harness", "claude", "--harness", "claude"}} {
		if err := setupCommandInput("repin", args, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatal("repin accepted unsupported scope")
		}
	}
}

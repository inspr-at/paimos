// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture deliberately asks for a tool even when its peer requested a
// harmless reply. It does not execute that tool. A separate startup sentinel
// proves that refusing after initialize would already be too late for hooks.
func TestVerificationVendorFixture(t *testing.T) {
	harness := os.Getenv("AEON_VERIFICATION_FIXTURE")
	if harness == "" {
		return
	}
	if err := os.WriteFile(os.Getenv("AEON_VERIFICATION_SENTINEL"), []byte("started"), 0600); err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			result["protocolVersion"] = 1
		case "thread/start":
			result["thread"] = map[string]string{"id": "fixture-thread"}
		case "session/new":
			result["sessionId"] = "fixture-session"
		case "turn/start", "session/prompt":
			method := "item/tool/call"
			params := map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "callId": "fixture-call", "tool": "write_file", "arguments": map[string]string{"path": "repository-sentinel"}}
			if harness == Cursor {
				method = "session/request_permission"
				params = map[string]any{"sessionId": "fixture-session", "toolCall": map[string]string{"toolCallId": "fixture-call", "title": "Write repository sentinel", "kind": "edit"}, "options": []map[string]string{{"optionId": "allow-once", "kind": "allow_once", "name": "Allow"}}}
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 900, "method": method, "params": params})
			os.Exit(0)
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	os.Exit(0)
}

func TestUnqualifiedVerificationRefusesToolCallingProtocolFixtures(t *testing.T) {
	for _, harness := range []string{Codex, Cursor} {
		t.Run(harness, func(t *testing.T) {
			r := verificationRequest(t)
			r.Profile.Harness = harness
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(root, "startup-sentinel")
			t.Setenv("AEON_VERIFICATION_FIXTURE", harness)
			t.Setenv("AEON_VERIFICATION_SENTINEL", sentinel)
			path := filepath.Join(root, "fake-vendor")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			if err := os.WriteFile(path, []byte(fmt.Sprintf("#!/bin/sh\nexec %s -test.run=^TestVerificationVendorFixture$\n", quote(executable))), 0700); err != nil {
				t.Fatal(err)
			}
			var adapter Adapter = NewCursorAdapter(path, map[string]string{"account": "42"})
			if harness == Codex {
				adapter = NewCodexAdapter(path, map[string]string{"account": root})
			}
			process, err := adapter.Start(t.Context(), r, func(AdapterEvent) { t.Error("unexpected vendor activity") })
			if process != nil {
				_ = process.Stop(t.Context())
				t.Fatal("unqualified fixture started")
			}
			if !errors.Is(err, ErrVerificationUnavailable) {
				t.Fatalf("expected capability refusal, got %v", err)
			}
			if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("vendor launched before the refusal")
			}
			// Positive control: prove this is a functioning protocol fixture,
			// not an invalid executable giving a misleading no-launch pass.
			start, prompt, tool := "thread/start", "turn/start", "item/tool/call"
			if harness == Cursor {
				start, prompt, tool = "session/new", "session/prompt", "session/request_permission"
			}
			cmd := exec.CommandContext(t.Context(), path)
			cmd.Dir = root
			cmd.Stdin = strings.NewReader(fmt.Sprintf("{\"id\":1,\"method\":\"initialize\"}\n{\"id\":2,\"method\":%q}\n{\"id\":3,\"method\":%q}\n", start, prompt))
			output, err := cmd.Output()
			if err != nil || !strings.Contains(string(output), `"method":"`+tool+`"`) {
				t.Fatal("tool-calling fixture positive control failed")
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "started" {
				t.Fatal("startup fixture positive control failed")
			}
		})
	}
}

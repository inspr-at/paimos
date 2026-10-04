// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCommandRequiresExplicitExistingAccount(t *testing.T) {
	for _, args := range [][]string{{}, {"--account", "bad"}, {"--account", "x", "unexpected"}, {"--unknown"}} {
		var out bytes.Buffer
		opened := false
		err := requestVerificationApproval(args, &out, func(context.Context, string) error { opened = true; return nil })
		if err == nil || opened || out.Len() != 0 {
			t.Fatal("invalid request reached approval browser")
		}
	}
}

func TestVerifyCommandOpensOnlyOwnerApprovalAndPreservesFixtureState(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	account := "ab000000-0000-4000-8000-000000000685"
	proof := strings.Repeat("a", 64)
	state := map[string]any{"schema": "aeon.agent-setup.private.v1", "origin": "https://fixture.example.test",
		"request":       map[string]any{"request_id": "ac000000-0000-4000-8000-000000000685"},
		"device_secret": proof, "runtime_secret": proof, "lifecycle_secret": proof,
		"view": map[string]any{"computer_state": "connected", "enrollments": []map[string]any{{"account_id": account, "state": "connected"}}}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pairing.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, noBrowser := range []bool{false, true} {
		var out bytes.Buffer
		opened := ""
		args := []string{"--account", account, "--state-root", root}
		if noBrowser {
			args = append(args, "--no-browser")
		}
		err = requestVerificationApproval(args, &out, func(_ context.Context, link string) error { opened = link; return nil })
		if err != nil {
			t.Fatal(err)
		}
		link := "https://fixture.example.test/agents?verify_account=" + account
		if !strings.Contains(out.String(), link) || strings.Contains(out.String(), proof) || (!noBrowser && opened != link) || (noBrowser && opened != "") {
			t.Fatal("incorrect approval path or proof exposure")
		}
		if !strings.Contains(out.String(), "no check or allowance has been created") {
			t.Fatal("command claimed unapproved verification")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, raw) {
			t.Fatal("verify migrated or changed fixture state")
		}
	}
}

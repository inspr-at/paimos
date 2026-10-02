// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
)

// Use the real CLI, agent auth, API and migrated database while the launcher's
// environment still names the registered Claude parent (AEON-498).
func TestClaudeCoordinatorRegistersChildrenEndToEnd(t *testing.T) {
	isolate(t)
	d := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), d.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	base := renameServer(t, d, "aeon", true)
	key := mintAgent(t, base, "aeon-coordinator")
	seedProject(t, base, key.Token)
	t.Setenv("AEON_URL", base)
	t.Setenv("AEON_API_KEY", key.Token)
	const vendor = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	t.Setenv("CLAUDE_CODE_SESSION_ID", vendor)
	config := filepath.Join(t.TempDir(), "missing")
	prepare := func(name, parent, role string) (string, []string) {
		t.Helper()
		dir := t.TempDir()
		ref := "claude-child-registration-" + name
		lease := "synthetic-child-worker-lease-00000000-" + name
		refFile, leaseFile := filepath.Join(dir, "ref"), filepath.Join(dir, "lease")
		if err := os.WriteFile(refFile, []byte(ref), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(leaseFile, []byte(lease), 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"aeon", "--config", config, "--json", "harness", "register", "--project", "AEON", "--agent", "aeon-coordinator", "--harness", "claude", "--harness-version", "2.1.0", "--host", "children-test", "--management", "unmanaged", "--role", role, "--harness-session-file", refFile, "--worker-lease-file", leaseFile}
		if parent != "" {
			args = append(args, "--parent-session", parent)
		}
		return lease, args
	}
	registered := func(name string, code int, out, stderr string) harness.Session {
		t.Helper()
		if code != 0 || stderr != "" {
			t.Fatalf("register %s: code %d stderr %s", name, code, stderr)
		}
		if strings.Contains(out, "claude-child-registration-") || strings.Contains(out, "synthetic-child-worker-lease-") || strings.Contains(out, vendor) || strings.Contains(out, key.Token) {
			t.Fatal("registration exposed a private value")
		}
		var s harness.Session
		if err := json.Unmarshal([]byte(out), &s); err != nil || !validUUID(s.ID) {
			t.Fatal("registration did not return a valid session")
		}
		return s
	}
	parentLease, parentArgs := prepare("parent", "", "coordinator")
	code, out, stderr := runCLI(parentArgs, "")
	parent := registered("parent", code, out, stderr)
	firstLease, firstArgs := prepare("first", parent.ID, "worker")
	secondLease, secondArgs := prepare("second", parent.ID, "worker")
	type result struct {
		index       int
		code        int
		out, stderr string
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i, args := range [][]string{firstArgs, secondArgs} {
		go func() {
			<-start
			code, out, stderr := runCLI(args, "")
			results <- result{i, code, out, stderr}
		}()
	}
	close(start)
	children := make([]harness.Session, 2)
	for range children {
		r := <-results
		children[r.index] = registered([]string{"first", "second"}[r.index], r.code, r.out, r.stderr)
	}
	first, second := children[0], children[1]
	if !parent.HasVendorSessionRef || first.HasVendorSessionRef || second.HasVendorSessionRef {
		t.Fatal("children inherited the parent's vendor binding")
	}
	if parent.ID == first.ID || parent.ID == second.ID || first.ID == second.ID {
		t.Fatal("parent and children are not distinct generations")
	}
	for _, child := range []harness.Session{first, second} {
		if child.AgentPrincipalID != parent.AgentPrincipalID || child.ParentID == nil || *child.ParentID != parent.ID || child.Harness != "claude" {
			t.Fatal("child lost its authenticated agent or parent")
		}
	}
	code, out, stderr = runCLI(firstArgs, "")
	var replay harness.Session
	if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &replay) != nil || replay.ID != first.ID {
		t.Fatal("child exact replay did not preserve its generation")
	}
	c := client.New(base, key.Token)
	for _, entry := range []struct{ ref, id string }{
		{vendor, parent.ID},
		{"claude-child-registration-first", first.ID},
		{"claude-child-registration-second", second.ID},
	} {
		var binding struct {
			SessionID string `json:"session_id"`
		}
		if err := c.Do(t.Context(), http.MethodPost, "/api/inbox/session-binding", map[string]string{"harness_session_ref": entry.ref}, &binding); err != nil || binding.SessionID != entry.id {
			t.Fatal("session reference did not resolve to its own generation")
		}
	}
	path := harnessPath(parent.ProjectID, "")
	var sessions []harness.Session
	if err := c.Do(t.Context(), http.MethodGet, path, nil, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("active sessions = %d, want parent and two children", len(sessions))
	}
	for i, s := range []harness.Session{parent, first, second} {
		if s.StoppedAt != nil || s.ArchivedAt != nil {
			t.Fatal("a generation ended during child registration")
		}
		lease := []string{parentLease, firstLease, secondLease}[i]
		if err := c.DoWithHeaders(t.Context(), http.MethodPost, harnessPath(s.ProjectID, s.ID)+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1}, nil, map[string]string{"X-Aeon-Worker-Lease": lease}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.DoWithHeaders(t.Context(), http.MethodPost, harnessPath(first.ProjectID, first.ID)+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 2}, nil, map[string]string{"X-Aeon-Worker-Lease": secondLease}); err == nil || !strings.HasPrefix(err.Error(), "api 403:") {
		t.Fatal("one child could use its lease on another child")
	}

	// The server still rejects an explicit duplicate native reference; omitting
	// an inherited ambient reference is not an exemption from uniqueness.
	duplicate := map[string]any{"agent_principal_id": key.PrincipalID, "harness": "claude", "host": "children-test", "management_mode": "unmanaged", "role": "worker", "parent_harness_session_id": parent.ID, "harness_session_ref": "duplicate-child-registration-ref", "worker_lease": "synthetic-duplicate-child-lease-00000000", "vendor_session_ref": vendor}
	err := c.Do(t.Context(), http.MethodPost, path, duplicate, nil)
	sum := sha256.Sum256([]byte("aeon.harness.ref\x00" + vendor))
	wantConflict := fmt.Sprintf("api 409: vendor_session_ref is already bound to an active generation for this agent (sha256:%x)", sum[:8])
	if err == nil || err.Error() != wantConflict || strings.Contains(err.Error(), vendor) || strings.Contains(err.Error(), key.Token) || strings.Contains(err.Error(), duplicate["worker_lease"].(string)) || strings.Contains(err.Error(), duplicate["harness_session_ref"].(string)) {
		t.Fatalf("duplicate native reference diagnostic: %v", err)
	}
	assertParentBinding := func() {
		t.Helper()
		var binding struct {
			SessionID string `json:"session_id"`
		}
		if err := c.Do(t.Context(), http.MethodPost, "/api/inbox/session-binding", map[string]string{"harness_session_ref": vendor}, &binding); err != nil {
			t.Fatalf("parent native reference lookup: %v", err)
		}
		if binding.SessionID != parent.ID {
			t.Fatal("parent native reference no longer resolves to the parent")
		}
	}
	assertParentBinding()
	for _, family := range []string{"codex", "grok", "cursor", "pi"} {
		duplicate["harness"] = family
		duplicate["harness_session_ref"] = "child-family-reference-" + family
		duplicate["worker_lease"] = "synthetic-child-family-lease-00000000-" + family
		duplicate["vendor_session_ref"] = vendor
		err := c.Do(t.Context(), http.MethodPost, path, duplicate, nil)
		if err == nil || err.Error() != wantConflict || strings.Contains(err.Error(), vendor) {
			t.Fatalf("%s duplicate native reference diagnostic: %v", family, err)
		}
		assertParentBinding()

		// A child with its own native reference still registers on every harness.
		duplicate["vendor_session_ref"] = "child-family-native-reference-" + family
		var child harness.Session
		if err := c.Do(t.Context(), http.MethodPost, path, duplicate, &child); err != nil {
			t.Fatalf("%s child registration: %v", family, err)
		}
		if child.ParentID == nil || *child.ParentID != parent.ID || child.AgentPrincipalID != key.PrincipalID {
			t.Fatalf("%s child changed hierarchy or identity", family)
		}
		assertParentBinding()
	}

	wrongAgent := append([]string{}, firstArgs...)
	for i, arg := range wrongAgent {
		if arg == "--agent" {
			wrongAgent[i+1] = "another-agent"
		}
	}
	code, _, stderr = runCLI(wrongAgent, "")
	if code != 2 || !strings.Contains(stderr, fmt.Sprintf("authenticated agent %q", "aeon-coordinator")) || !strings.Contains(stderr, "--parent-session") {
		t.Fatal("wrong agent diagnostic omitted the authenticated identity or child guidance")
	}
}

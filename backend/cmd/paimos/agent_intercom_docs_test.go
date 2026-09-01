// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	messageharness "github.com/inspr-at/paimos/backend/agentmessage/harness"
)

// TestAgentIntercomRunbookUsesShippedCLI makes the public runbook a CI-owned
// contract. A command or flag rename must update the executable surface and
// its GitHub documentation together.
func TestAgentIntercomRunbookUsesShippedCLI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	root := rootCmd()
	tests := []struct {
		path  []string
		flags []string
	}{
		{[]string{"project", "show"}, nil},
		{[]string{"session", "start"}, []string{"project", "agent"}},
		{[]string{"tell"}, []string{"project", "level", "message"}},
		{[]string{"listen"}, []string{"as", "project", "follow", "deliver"}},
		{[]string{"message", "target", "set"}, []string{"project", "address", "adapter", "kind", "maximum-level", "role", "target-ref-file"}},
		{[]string{"message", "target", "list"}, []string{"project", "address"}},
		{[]string{"message", "target", "requeue"}, []string{"project", "address"}},
		{[]string{"message", "deliveries"}, []string{"project"}},
		{[]string{"harness", "list"}, []string{"project"}},
		{[]string{"harness", "status"}, []string{"project", "session"}},
		{[]string{"harness", "interrupt"}, []string{"project", "session"}},
		{[]string{"harness", "stop"}, []string{"project", "session"}},
		{[]string{"harness", "mark-stopped"}, []string{"project", "session", "agent", "worker-lease-file"}},
	}
	for _, test := range tests {
		name := strings.Join(test.path, " ")
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(doc, name) {
				t.Fatalf("runbook does not name shipped command %q", name)
			}
			command, remaining, findErr := root.Find(test.path)
			if findErr != nil || len(remaining) != 0 || command == root {
				t.Fatalf("command %q unavailable: command=%v remaining=%v error=%v", name, command, remaining, findErr)
			}
			for _, flag := range test.flags {
				if command.Flags().Lookup(flag) == nil && command.InheritedFlags().Lookup(flag) == nil {
					t.Errorf("documented command %q lost --%s", name, flag)
				}
			}
			if len(test.flags) > 0 && !documentedCommandHasFlags(doc, "paimos "+name, test.flags) {
				t.Errorf("runbook does not show %q with all required documented flags %v", name, test.flags)
			}
		})
	}
	if root.PersistentFlags().Lookup("json") == nil || !strings.Contains(doc, "paimos --json project show") {
		t.Error("numeric project lookup drifted from the documented --json project show command")
	}
}

func TestAgentIntercomREADMEQuickstartUsesShippedCLI(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	root := rootCmd()
	tests := []struct {
		path  []string
		flags []string
	}{
		{[]string{"project", "show"}, nil},
		{[]string{"session", "start"}, []string{"project", "agent"}},
		{[]string{"tell"}, []string{"project", "level", "message"}},
	}
	for _, test := range tests {
		name := strings.Join(test.path, " ")
		command, remaining, findErr := root.Find(test.path)
		if findErr != nil || len(remaining) != 0 || command == root {
			t.Fatalf("README command %q unavailable: command=%v remaining=%v error=%v", name, command, remaining, findErr)
		}
		for _, flag := range test.flags {
			if command.Flags().Lookup(flag) == nil && command.InheritedFlags().Lookup(flag) == nil {
				t.Errorf("README command %q lost --%s", name, flag)
			}
		}
		if len(test.flags) > 0 && !documentedCommandHasFlags(doc, "paimos "+name, test.flags) {
			t.Errorf("README does not show %q with all required flags %v", name, test.flags)
		}
	}
	if root.PersistentFlags().Lookup("json") == nil || !strings.Contains(doc, "paimos --json project show") {
		t.Error("README numeric project lookup drifted from --json project show")
	}
}

func documentedCommandHasFlags(doc, command string, flags []string) bool {
	for offset := 0; offset < len(doc); {
		index := strings.Index(doc[offset:], command)
		if index < 0 {
			return false
		}
		start := offset + index
		end := len(doc)
		if paragraph := strings.Index(doc[start:], "\n\n"); paragraph >= 0 {
			end = start + paragraph
		}
		snippet := doc[start:end]
		complete := true
		for _, flag := range flags {
			complete = complete && strings.Contains(snippet, "--"+flag)
		}
		if complete {
			return true
		}
		offset = start + len(command)
	}
	return false
}

func TestAgentIntercomPublicDocsDoNotContainResolvedLocalCapabilities(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "..", "README.md"),
		filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 -- fixed in-repo documentation paths.
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, forbidden := range []string{"/Users/", "/home/", `"target_ref":`, `"target_secret":`, `"message_id":"`, `"delivery_id":"`} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains resolved or raw private example marker %q", path, forbidden)
			}
		}
	}
}

func TestAgentIntercomRunbookKeepsUnmanagedClaudeAndGrokSimpleOnly(t *testing.T) {
	plugins := []messageharness.Plugin{
		messageharness.ClaudePlugin{},
		messageharness.ClaudePlugin{Channel: true},
		messageharness.GrokRoutinePlugin{},
	}
	for _, plugin := range plugins {
		if level := plugin.MaximumLevel(); level != messageharness.LevelSimple {
			t.Fatalf("unmanaged adapter %s now advertises %q; review the intercom truth matrix", plugin.Name(), level)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, claim := range []string{
		"Unmanaged Claude (`claude_resume` / `claude_channel`)",
		"requested steer records effective `simple` with `unsupported`",
		"Grok Bot routine / gated Grok Build path",
		"A webhook or CLI resume is never queue-faked as steer",
	} {
		if !strings.Contains(doc, claim) {
			t.Errorf("runbook lost simple-only unmanaged boundary %q", claim)
		}
	}
}

func TestAgentIntercomDocsKeepWorkerLeaseTrustAndRecoveryContract(t *testing.T) {
	paths := map[string]string{
		"runbook":     filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md"),
		"integration": filepath.Join("..", "..", "..", "docs", "AGENT_INTEGRATION.md"),
	}
	docs := make(map[string]string, len(paths))
	for name, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 -- fixed in-repo documentation paths.
		if err != nil {
			t.Fatal(err)
		}
		docs[name] = string(raw)
	}
	for name, doc := range docs {
		normalized := strings.Join(strings.Fields(doc), " ")
		for _, invariant := range []string{
			"per-generation worker lease",
			"shared API key",
			"domain-separated digest",
			"owner-only",
			"argv",
			"redirect",
			"exact project",
			"ownership_lost",
			"remote_closed",
			"lease_deleted",
			"prunable",
		} {
			if !strings.Contains(normalized, invariant) {
				t.Errorf("%s lost worker-lease invariant %q", name, invariant)
			}
		}
	}
	normalizedRunbook := strings.Join(strings.Fields(docs["runbook"]), " ")
	for _, claim := range []string{
		"public harness-session UUID plus caller-supplied agent attribution is not",
		"neither the vendor session reference nor the shared API key",
		"Missing, duplicate, wrong-generation, or cross-project proof fails",
		"mismatched successful response",
		"Inbox-capable registration is target-first and recoverable",
		"crash before that insert can leave only the reusable encrypted target, which grants no worker authority",
		"first completes any journaled claimed-control outcome with its exact recorded result",
	} {
		if !strings.Contains(normalizedRunbook, claim) {
			t.Errorf("runbook lost authorization or acknowledgement boundary %q", claim)
		}
	}
	if !documentedCommandHasFlags(docs["integration"], "paimos harness register", []string{"project", "agent", "harness", "host", "harness-session-file", "worker-lease-file"}) {
		t.Error("integration guide registration example lost its protected generation lease")
	}
	if !documentedCommandHasFlags(docs["runbook"], "paimos harness mark-stopped", []string{"project", "session", "agent", "worker-lease-file"}) {
		t.Error("runbook stop example lost its exact generation lease")
	}
	apiRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "api-minimal.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	api := strings.Join(strings.Fields(string(apiRaw)), " ")
	for _, claim := range []string{
		"domain-separated digest",
		"exact project path and public harness-session UUID",
		"X-Paimos-Harness-Worker-Lease",
		"Missing, duplicate, wrong-generation, and cross-project proofs",
		"--worker-lease-file",
		"rejects redirects",
		"commits the active session with both digest and target FK",
		"leaves only a reusable target, not worker authority",
	} {
		if !strings.Contains(api, claim) {
			t.Errorf("REST reference lost worker authorization claim %q", claim)
		}
	}
	readmeRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	readme := strings.Join(strings.Fields(string(readmeRaw)), " ")
	for _, claim := range []string{"per-generation lease", "kept out of argv", "server-side only as a digest", "shared API key are not worker proof"} {
		if !strings.Contains(readme, claim) {
			t.Errorf("README lost concise worker-lease boundary %q", claim)
		}
	}
}

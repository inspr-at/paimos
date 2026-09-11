// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentd"
)

func TestAgentIntercomRunbookUsesShippedAgentdCommandsAndFlags(t *testing.T) {
	paths := map[string]string{
		"runbook": filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md"),
	}
	docs := make(map[string]string, len(paths))
	for name, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 -- fixed in-repo documentation paths.
		if err != nil {
			t.Fatal(err)
		}
		docs[name] = string(raw)
	}
	for _, command := range []string{"serve", "start", "status", "steer", "interrupt", "stop", "held-queue", "resume-queue", "decisions", "inspect", "output", "answer"} {
		for name, doc := range docs {
			if !strings.Contains(doc, "paimos-agentd "+command) {
				t.Errorf("%s quickstart lost paimos-agentd %s", name, command)
			}
		}
		var output bytes.Buffer
		err := run([]string{command}, strings.NewReader("text"), &output)
		if err == nil || !strings.Contains(err.Error(), "--instance is required") {
			t.Errorf("paimos-agentd %s no longer reaches the common required-instance guard: %v", command, err)
		}
	}

	runbookFlags := map[string][]string{
		"serve":        {"instance", "socket", "report-host", "report-url", "report-api-key-file"},
		"start":        {"instance", "socket", "adapter", "workspace", "project-id", "identity"},
		"status":       {"instance", "socket"},
		"steer":        {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"interrupt":    {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"stop":         {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"held-queue":   {"instance", "socket", "session", "project-id", "identity"},
		"resume-queue": {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"decisions":    {"instance", "socket", "session", "project-id", "identity"},
		"inspect":      {"instance", "socket", "session", "project-id", "identity", "request-id", "digest"},
		"output":       {"instance", "socket", "session", "project-id", "identity"},
		"answer":       {"instance", "socket", "session", "project-id", "identity", "correlation-id", "request-id", "digest", "option-id"},
	}
	for name, contract := range map[string]map[string][]string{"runbook": runbookFlags} {
		for command, names := range contract {
			if !allDocumentedAgentdCommandsHaveFlags(docs[name], command, names) {
				t.Errorf("%s has a paimos-agentd %s example without exact scope flags %v", name, command, names)
			}
		}
	}

	actualFlags := map[string][]string{
		"serve":        {"instance", "socket", "codex-path", "claude-path", "node-path", "claude-sdk-path", "pi-path", "pi-accounts", "cursor-path", "cursor-accounts", "report-host", "report-url", "report-api-key-file", "paimos-path"},
		"start":        {"instance", "socket", "adapter", "workspace", "project-id", "identity", "account-key"},
		"status":       {"instance", "socket"},
		"steer":        {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"interrupt":    {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"stop":         {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"held-queue":   {"instance", "socket", "session", "project-id", "identity"},
		"resume-queue": {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"decisions":    {"instance", "socket", "session", "project-id", "identity"},
		"inspect":      {"instance", "socket", "session", "project-id", "identity", "request-id", "digest"},
		"output":       {"instance", "socket", "session", "project-id", "identity"},
		"answer":       {"instance", "socket", "session", "project-id", "identity", "correlation-id", "request-id", "digest", "option-id"},
	}
	for command, names := range actualFlags {
		for _, name := range names {
			var output bytes.Buffer
			err := run([]string{command, "--" + name}, strings.NewReader("text"), &output)
			if err == nil || !strings.Contains(err.Error(), "flag needs an argument") {
				t.Errorf("paimos-agentd %s lost --%s: %v", command, name, err)
			}
		}
	}
}

func TestAgentIntercomREADMELinksGuidedStartsToOwnedRuntime(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "README.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(raw)), " ")
	for _, claim := range []string{
		"runtime doctor --project",
		"runtime setup --project",
		"orchestrator start --guided",
		"worker start --guided",
		"docs/AGENT_INTERCOM.md#local-runtime-setup-doctor-repair-and-reset",
	} {
		if !strings.Contains(doc, claim) {
			t.Errorf("README lost guided runtime ownership boundary %q", claim)
		}
	}
}

func TestAgentIntercomDocsDistinguishVendorAndPublicSessionIDs(t *testing.T) {
	encoded, err := json.Marshal(agentd.Status{Sessions: []agentd.Session{{
		HarnessSessionID: "vendor-thread-or-session",
		Reporter:         agentd.ReporterState{PublicSessionID: "public-generation"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	status := string(encoded)
	if !strings.Contains(status, `"harness_session_id":"vendor-thread-or-session"`) ||
		!strings.Contains(status, `"public_session_id":"public-generation"`) {
		t.Fatalf("agentd status no longer exposes distinct vendor and public IDs: %s", status)
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(raw)), " ")
	for _, claim := range []string{
		"sessions[].harness_session_id",
		"sessions[].reporter.public_session_id",
		"harness_session_id",
	} {
		if !strings.Contains(doc, claim) {
			t.Errorf("runbook lost session-ID boundary %q", claim)
		}
	}
}

func allDocumentedAgentdCommandsHaveFlags(docs, command string, flags []string) bool {
	token := "paimos-agentd " + command
	found := false
	for offset := 0; offset < len(docs); {
		index := strings.Index(docs[offset:], token)
		if index < 0 {
			return found
		}
		found = true
		start := offset + index
		end := len(docs)
		if paragraph := strings.Index(docs[start:], "\n\n"); paragraph >= 0 {
			end = start + paragraph
		}
		snippet := docs[start:end]
		complete := true
		for _, flag := range flags {
			complete = complete && strings.Contains(snippet, "--"+flag)
		}
		if !complete {
			return false
		}
		offset = start + len(token)
	}
	return found
}

func someDocumentedAgentdCommandHasFlags(docs, command string, flags []string) bool {
	token := "paimos-agentd " + command
	for offset := 0; offset < len(docs); {
		index := strings.Index(docs[offset:], token)
		if index < 0 {
			return false
		}
		start := offset + index
		end := len(docs)
		if paragraph := strings.Index(docs[start:], "\n\n"); paragraph >= 0 {
			end = start + paragraph
		}
		snippet := docs[start:end]
		complete := true
		for _, flag := range flags {
			complete = complete && strings.Contains(snippet, "--"+flag)
		}
		if complete {
			return true
		}
		offset = start + len(token)
	}
	return false
}

func TestAgentIntercomSupportingStartExamplesKeepExactOwnedScope(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "..", "docs", "AGENT_INTEGRATION.md"),
		filepath.Join("..", "..", "..", "docs", "INSTALL.md"),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 -- fixed in-repo documentation paths.
		if err != nil {
			t.Fatal(err)
		}
		if !allDocumentedAgentdCommandsHaveFlags(string(raw), "start", []string{"instance", "adapter", "workspace", "project-id", "identity"}) {
			t.Errorf("%s has an owned start example without exact instance/project/identity scope", path)
		}
		if !someDocumentedAgentdCommandHasFlags(string(raw), "serve", []string{"instance", "report-host", "report-url", "report-api-key-file"}) {
			t.Errorf("%s has a reporting serve example without the all-or-none reporting tuple", path)
		}
	}
}

func TestAgentIntercomMatrixMatchesShippedControlBoundaries(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AGENT_INTERCOM.md")) // #nosec G304 -- fixed in-repo documentation path.
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	wantOwned := []agentd.Capability{
		agentd.CapabilityInbox,
		agentd.CapabilityStatus,
		agentd.CapabilitySteer,
		agentd.CapabilityInterrupt,
		agentd.CapabilityStop,
	}
	wantCursor := []agentd.Capability{
		agentd.CapabilityInbox,
		agentd.CapabilityStatus,
		agentd.CapabilityInterrupt,
		agentd.CapabilityStop,
	}
	for name, adapter := range map[string]agentd.Adapter{
		"agentd_codex":  agentd.NewCodexAdapter("codex", ""),
		"agentd_claude": agentd.NewClaudeAdapter("claude", "node", "sdk.mjs"),
		"agentd_pi":     agentd.NewPiAdapter("pi"),
		"agentd_cursor": agentd.NewCursorAdapter("cursor-agent", ""),
	} {
		want := wantOwned
		if name == "agentd_cursor" {
			want = wantCursor
		}
		if capabilities := adapter.Capabilities(); !slices.Equal(capabilities, want) {
			t.Fatalf("%s shipped capabilities=%v want=%v", name, capabilities, want)
		}
		row := intercomMatrixRow(doc, name)
		if name == "agentd_pi" {
			for _, claim := range []string{"clear_queue", "pi_context"} {
				if !strings.Contains(row, claim) {
					t.Errorf("%s matrix row lost supported control claim %q: %s", name, claim, row)
				}
			}
			continue
		}
		if name == "agentd_cursor" {
			for _, claim := range []string{"session/prompt", "session/cancel", "cursor_context"} {
				if !strings.Contains(row, claim) {
					t.Errorf("%s matrix row lost supported control claim %q: %s", name, claim, row)
				}
			}
			continue
		}
	}
}

func intercomMatrixRow(doc, receiver string) string {
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, "`"+receiver+"`") {
			return line
		}
	}
	return ""
}

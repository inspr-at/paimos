// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentd"
)

func TestAgentIntercomDocsUseShippedAgentdCommandsAndFlags(t *testing.T) {
	paths := map[string]string{
		"README":  filepath.Join("..", "..", "..", "README.md"),
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
	for _, command := range []string{"serve", "start", "status", "steer", "interrupt", "stop"} {
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

	readmeFlags := map[string][]string{
		"serve":     {"instance", "report-host", "report-url", "report-api-key-file"},
		"start":     {"instance", "adapter", "workspace", "project-id", "identity"},
		"status":    {"instance"},
		"steer":     {"instance", "session", "project-id", "identity", "correlation-id"},
		"interrupt": {"instance", "session", "project-id", "identity", "correlation-id"},
		"stop":      {"instance", "session", "project-id", "identity", "correlation-id"},
	}
	runbookFlags := map[string][]string{
		"serve":     {"instance", "socket", "report-host", "report-url", "report-api-key-file"},
		"start":     {"instance", "socket", "adapter", "workspace", "project-id", "identity"},
		"status":    {"instance", "socket"},
		"steer":     {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"interrupt": {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"stop":      {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
	}
	for name, contract := range map[string]map[string][]string{"README": readmeFlags, "runbook": runbookFlags} {
		for command, names := range contract {
			if !allDocumentedAgentdCommandsHaveFlags(docs[name], command, names) {
				t.Errorf("%s has a paimos-agentd %s example without exact scope flags %v", name, command, names)
			}
		}
	}

	actualFlags := map[string][]string{
		"serve":     {"instance", "socket", "codex-path", "claude-path", "node-path", "claude-sdk-path", "report-host", "report-url", "report-api-key-file", "paimos-path"},
		"start":     {"instance", "socket", "adapter", "workspace", "project-id", "identity"},
		"status":    {"instance", "socket"},
		"steer":     {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"interrupt": {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
		"stop":      {"instance", "socket", "session", "project-id", "identity", "correlation-id"},
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
	for name, adapter := range map[string]agentd.Adapter{
		"Owned Codex (`agentd_codex`)":   agentd.NewCodexAdapter("codex", ""),
		"Owned Claude (`agentd_claude`)": agentd.NewClaudeAdapter("claude", "node", "sdk.mjs"),
	} {
		if capabilities := adapter.Capabilities(); !slices.Equal(capabilities, wantOwned) {
			t.Fatalf("%s shipped capabilities=%v want=%v", name, capabilities, wantOwned)
		}
		row := intercomMatrixRow(doc, name)
		for _, claim := range []string{"Yes, exact live", "Local always", "durable typed interrupt/stop", "never inbox/steer"} {
			if !strings.Contains(row, claim) {
				t.Errorf("%s matrix row lost supported control claim %q: %s", name, claim, row)
			}
		}
	}

	unsupported := map[string][]string{
		"Unmanaged Claude (`claude_resume` / `claude_channel`)": {"| No; requested steer records effective `simple` with `unsupported` |", "| No |"},
		"Grok Bot routine / gated Grok Build path":              {"| No; effective behavior is simple |", "| No owned process status |", "| No |"},
	}
	for name, claims := range unsupported {
		row := intercomMatrixRow(doc, name)
		for _, claim := range claims {
			if !strings.Contains(row, claim) {
				t.Errorf("%s matrix row lost unsupported boundary %q: %s", name, claim, row)
			}
		}
	}
	if !strings.Contains(doc, "queue-faked") {
		t.Error("runbook must explicitly reject queue-faked steer claims")
	}
}

func intercomMatrixRow(doc, receiver string) string {
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "| "+receiver+" |") {
			return line
		}
	}
	return ""
}

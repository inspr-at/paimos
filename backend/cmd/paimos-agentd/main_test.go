// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentd"
)

func TestVersionAndRequiredInstance(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"version"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != Version {
		t.Fatalf("version output=%q", output.String())
	}
	output.Reset()
	if err := run([]string{"--version"}, strings.NewReader(""), &output); err != nil || strings.TrimSpace(output.String()) != Version {
		t.Fatalf("--version output=%q error=%v", output.String(), err)
	}
	if err := run([]string{"status"}, strings.NewReader(""), &output); err == nil || !strings.Contains(err.Error(), "--instance") {
		t.Fatalf("status without instance error=%v", err)
	}
}

func TestOwnedSessionCommandsRequireProjectAndIdentityScope(t *testing.T) {
	var output bytes.Buffer
	for _, args := range [][]string{
		{"start", "--instance", "ppm", "--identity", "codex:worker"},
		{"steer", "--instance", "ppm", "--session", "019d1234-1234-7123-8123-123456789abc", "--identity", "codex:worker"},
		{"interrupt", "--instance", "ppm", "--session", "019d1234-1234-7123-8123-123456789abc", "--project-id", "870"},
		{"stop", "--instance", "ppm", "--session", "019d1234-1234-7123-8123-123456789abc", "--project-id", "870"},
	} {
		err := run(args, strings.NewReader("body"), &output)
		if err == nil || (!strings.Contains(err.Error(), "--project-id") && !strings.Contains(err.Error(), "--identity")) {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

func TestServeRejectsPartialReporterConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"serve", "--instance", "ppm", "--report-host", "camyb"},
		{"serve", "--instance", "ppm", "--report-url", "https://ppm.example"},
		{"serve", "--instance", "ppm", "--report-api-key-file", "/run/credentials/ppm"},
		{"serve", "--instance", "ppm", "--paimos-path", "/opt/paimos"},
		{"serve", "--instance", "ppm", "--report-host", "camyb", "--report-url", "https://ppm.example"},
	} {
		err := run(args, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "requires --report-host, --report-url, and --report-api-key-file") {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

func TestServeRegistersOwnedCodexAndClaudeAdapters(t *testing.T) {
	adapters := serveAdapters("/operator/codex", "/operator/claude", "/runtime/node", "/operator/sdk.mjs")
	var names []string
	for _, adapter := range adapters {
		names = append(names, adapter.Name())
		if !slices.Contains(adapter.Capabilities(), agentd.CapabilityStop) {
			t.Fatalf("adapter %q has no stop capability", adapter.Name())
		}
	}
	if !slices.Equal(names, []string{agentd.AdapterCodex, agentd.AdapterClaude}) {
		t.Fatalf("adapters=%v", names)
	}
}

func TestDefaultSocketIsSeparatedByInstance(t *testing.T) {
	root := t.TempDir()
	_, one, err := resolvePaths(&commonFlags{instance: "ppm-one", stateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	_, two, err := resolvePaths(&commonFlags{instance: "ppm-two", stateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if one == two || filepath.Dir(one) == filepath.Dir(two) {
		t.Fatalf("instance sockets collided: %s %s", one, two)
	}
}

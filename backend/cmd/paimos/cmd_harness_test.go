package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessNounDoesNotCollideWithAttributionSession(t *testing.T) {
	root := rootCmd()
	if root.Commands()[0] == nil {
		t.Fatal("root command unexpectedly empty")
	}
	harness, _, err := root.Find([]string{"harness"})
	if err != nil || harness == root || harness.Name() != "harness" {
		t.Fatalf("harness command missing: command=%v err=%v", harness, err)
	}
	session, _, err := root.Find([]string{"session", "start"})
	if err != nil || session.Name() != "start" {
		t.Fatalf("existing attribution session command changed: command=%v err=%v", session, err)
	}
	for _, child := range harness.Commands() {
		if child.Name() == "start" {
			t.Fatal("managed harness control plane must not define a second ambiguous start command")
		}
	}
}

func TestHarnessRegisterRejectsCapabilityEscalationBeforeNetwork(t *testing.T) {
	refFile := filepath.Join(t.TempDir(), "session-ref")
	if err := os.WriteFile(refFile, []byte("session-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := harnessRegisterCmd()
	command.SetArgs([]string{"--project", "PAI", "--agent", "worker", "--harness", "claude", "--host", "mbp0",
		"--harness-session-file", refFile, "--message-target-id", "11111111-1111-4111-8111-111111111111", "--management", "unmanaged", "--role", "worker", "--capability", "inbox,status,steer",
		"--steer-mode", "codex_external"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unmanaged steer") {
		t.Fatalf("error=%v", err)
	}
}

func TestHarnessRegisterHasNoPlaintextSessionFlag(t *testing.T) {
	command := harnessRegisterCmd()
	if command.Flags().Lookup("harness-session") != nil {
		t.Fatal("private harness session reference must never be accepted as a process argument")
	}
	if command.Flags().Lookup("harness-session-file") == nil {
		t.Fatal("protected harness session file flag missing")
	}
}

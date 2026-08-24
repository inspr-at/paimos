package main

import (
	"strings"
	"testing"
)

func TestMessageListRequiresAddresseeBeforeNetwork(t *testing.T) {
	cmd := messageListCmd()
	cmd.SetArgs([]string{"--project", "PAI"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("error=%v", err)
	}
}

func TestTellRequiresExactlyOneMessageSource(t *testing.T) {
	cmd := tellCmd()
	cmd.SetArgs([]string{"codex:reviewer", "--project", "PAI", "--message", "one", "--message-file", "two"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error=%v", err)
	}
}

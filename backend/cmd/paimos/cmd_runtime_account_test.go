// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"strings"
	"testing"
)

func TestRuntimeAccountHelpExposesConnectDisconnectAndStatus(t *testing.T) {
	connect, err := captureRuntimeHelp(t, "runtime", "account", "connect", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"account-key", "adapter", "request-key", "expected-revision", "runtime-generation"} {
		if !strings.Contains(connect, "--"+flag) {
			t.Fatalf("connect help lost --%s\n%s", flag, connect)
		}
	}
	disconnect, err := captureRuntimeHelp(t, "runtime", "account", "disconnect", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(disconnect, "--account-key") {
		t.Fatalf("disconnect help=%q", disconnect)
	}
	status, err := captureRuntimeHelp(t, "runtime", "account", "status", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "Discover") && !strings.Contains(status, "attached") {
		t.Fatalf("status help=%q", status)
	}
}

func captureRuntimeHelp(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd()
	var stdoutBuf, stderrBuf strings.Builder
	cmd.SetArgs(args)
	cmd.SetOut(&stdoutBuf)
	cmd.SetErr(&stderrBuf)
	err := cmd.Execute()
	return stdoutBuf.String() + stderrBuf.String(), err
}

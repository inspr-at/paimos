// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeProjectionAndQuiesceRequireExactOwnedSessions(t *testing.T) {
	process := newFakeProcess(9876)
	adapter := &fakeAdapter{name: AdapterCodex, process: process, threadID: "fixture-target-never-render"}
	s, e := NewSupervisor(SupervisorConfig{Instance: "fixture", Adapters: []Adapter{adapter}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(context.Background())
	session, e := s.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "fixture-prompt-never-render", Identity: "codex:fixture", ProjectID: 922})
	if e != nil {
		t.Fatal(e)
	}
	status := s.RuntimeStatus(context.Background())
	raw, _ := json.Marshal(status)
	for _, canary := range []string{"fixture-target-never-render", "fixture-prompt-never-render", session.Workspace, "codex:fixture"} {
		if strings.Contains(string(raw), canary) {
			t.Fatal("runtime projection leaked private data")
		}
	}
	if status.ReporterConfigured || !status.ReporterLastSuccess.IsZero() || len(status.Sessions) != 1 || !status.Sessions[0].Owned || !status.Sessions[0].WorkspaceVerified {
		t.Fatal("runtime evidence incorrect")
	}
	if s.QuiesceRuntime(context.Background(), "other", []string{session.ID}) == nil {
		t.Fatal("foreign generation accepted")
	}
	if s.QuiesceRuntime(context.Background(), status.DaemonID, []string{"stale-session"}) == nil {
		t.Fatal("stale process preview accepted")
	}
	if e = s.QuiesceRuntime(context.Background(), status.DaemonID, []string{session.ID}); e != nil {
		t.Fatal(e)
	}
	after := s.RuntimeStatus(context.Background())
	if !after.Closed || after.Sessions[0].Owned {
		t.Fatal("quiesce did not reap owned child")
	}
	if _, e = s.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:fixture", ProjectID: 922}); e == nil {
		t.Fatal("quiesced generation accepted new start")
	}
}
func TestRuntimeJournalInspectionNeverRepairsAndRejectsUnknownPayload(t *testing.T) {
	for _, raw := range []string{"broken\n", `{"version":1,"op":"other"}` + "\n", `{"version":1,"records":[],"credential":"fixture-private"}`} {
		if ValidateRuntimeJournal([]byte(raw), nil) == nil {
			t.Fatal("corrupt checkpoint accepted")
		}
	}
	if ValidateRuntimeJournal([]byte(`{"version":1,"records":[]}`), nil) != nil {
		t.Fatal("empty valid journal rejected")
	}
	if ValidateRuntimeJournal(nil, []byte(`{"version":1,"op":"delete","key":"a"}`)) == nil {
		t.Fatal("torn journal reported ready")
	}
}

func TestRuntimeWorkspaceSymlinkReplacementIsUnknown(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if e := os.Mkdir(workspace, 0700); e != nil {
		t.Fatal(e)
	}
	process := newFakeProcess(9876)
	s, e := NewSupervisor(SupervisorConfig{Instance: "fixture", Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "fixture"}}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(context.Background())
	session, e := s.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: workspace, Prompt: "work", Identity: "codex:fixture", ProjectID: 922})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(session.Workspace, filepath.Join(parent, "old")); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(t.TempDir(), session.Workspace); e != nil {
		t.Fatal(e)
	}
	if s.RuntimeStatus(context.Background()).Sessions[0].WorkspaceVerified {
		t.Fatal("replaced workspace path retained ownership")
	}
}

func TestRuntimeQuiesceCancellationDoesNotWaitForSpawn(t *testing.T) {
	s, e := NewSupervisor(SupervisorConfig{Instance: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(context.Background())
	s.startMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e = s.QuiesceRuntime(ctx, s.daemonID, nil)
	s.startMu.Unlock()
	if e == nil || s.RuntimeStatus(context.Background()).Closed {
		t.Fatal("cancelled quiesce changed runtime")
	}
}

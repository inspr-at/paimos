//go:build unix && !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCodexInboxUsesOwnedIdleAppServerTurn(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	adapter.command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=persistent")
		return cmd
	}
	process, err := adapter.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "secret-not-persisted", KeepAlive: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Stop(context.Background(), ControlRequest{CorrelationID: "cleanup"})
	p := process.(*codexProcess)
	deadline := time.Now().Add(time.Second)
	for !p.InboxReady() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !p.InboxReady() {
		t.Fatal("first turn did not become idle")
	}
	effect, err := p.Inbox(context.Background(), ControlRequest{CorrelationID: "fixture", Text: "fixture-next-input"})
	if err != nil || effect.Primitive != "codex app-server turn/start" || effect.VendorMessageID != "turn-next" {
		t.Fatal("owned same-thread next-turn handoff missing", err)
	}
	if p.InboxReady() {
		t.Fatal("active next turn was reported idle")
	}
	if _, err = p.Inbox(context.Background(), ControlRequest{CorrelationID: "busy", Text: "must not deliver"}); err == nil {
		t.Fatal("busy thread accepted a second simple input")
	}
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

//go:build unix && !paimos_test_unsupported

package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentmessage/harness"
)

func TestUnixTransportIsPrivateAndRoundTrips(t *testing.T) {
	adapter := &fakeAdapter{name: AdapterCodex, process: newFakeProcess(9876), threadID: "thread-local"}
	supervisor, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "pai849-agentd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "agentd.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(ctx, socket, supervisor) }()

	client, err := NewClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err = client.Status(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not start: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	info, err := os.Lstat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode=%v", info.Mode())
	}
	second, err := NewSupervisor(SupervisorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), socket, second); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon on live socket error=%v", err)
	}

	session, err := client.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "secret prompt is body-only", Identity: "codex:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := json.Marshal(harness.AgentdTarget{Socket: socket, SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := harness.Deliver(context.Background(), harness.AdapterAgentdCodex, harness.DeliverRequest{
		Level: harness.LevelSteer, Body: "leased body", TargetRef: string(target), CorrelationID: "delivery-leased-122",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveLevel != harness.LevelSteer || result.Primitive != "codex app-server turn/steer" {
		t.Fatalf("managed delivery result=%+v", result)
	}
	if _, err := client.Steer(context.Background(), session.ID, ControlRequest{CorrelationID: "delivery-123", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Interrupt(context.Background(), session.ID, ControlRequest{CorrelationID: "control-124"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stop(context.Background(), session.ID, ControlRequest{CorrelationID: "control-125"}); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

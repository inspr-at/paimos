// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type fixtureConsumers struct {
	started, drained chan struct{}
	once             sync.Once
}

func (c *fixtureConsumers) Run(ctx context.Context)              { close(c.started); <-ctx.Done(); close(c.drained) }
func (c *fixtureConsumers) Snapshot() []runtimeconsumer.Evidence { return nil }
func TestConsumerShutdownDrainsBeforeChildStop(t *testing.T) {
	adapter := &dispatchAdapter{label: "unknown"}
	s, err := NewSupervisor(SupervisorConfig{Instance: "fixture", Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Start(context.Background(), StartRequest{Adapter: AdapterCodex, ProjectID: 42, Identity: "codex:fixture", Workspace: t.TempDir(), Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	consumers := &fixtureConsumers{started: make(chan struct{}), drained: make(chan struct{})}
	if err := s.AttachConsumers(consumers); err != nil {
		t.Fatal(err)
	}
	<-consumers.started
	if err := s.AttachConsumers(consumers); err == nil {
		t.Fatal("second consumer supervisor admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-consumers.drained:
	default:
		t.Fatal("shutdown did not drain consumer")
	}
	if s.Status().Sessions[0].State != StateStopped {
		t.Fatal("owned child not stopped")
	}
}

type stuckConsumers struct{ release chan struct{} }

func (c *stuckConsumers) Run(context.Context)                  { <-c.release }
func (c *stuckConsumers) Snapshot() []runtimeconsumer.Evidence { return nil }
func TestConsumerDeadlineStillReapsOwnedChildAndAllowsCloseRetry(t *testing.T) {
	s, err := NewSupervisor(SupervisorConfig{Instance: "fixture", Adapters: []Adapter{&dispatchAdapter{label: "unknown"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Start(context.Background(), StartRequest{Adapter: AdapterCodex, ProjectID: 42, Identity: "codex:fixture", Workspace: t.TempDir(), Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	c := &stuckConsumers{release: make(chan struct{})}
	if err = s.AttachConsumers(c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Close(ctx); err == nil {
		t.Fatal("incomplete drain reported success")
	}
	if s.Status().Sessions[0].State != StateStopped {
		t.Fatal("caller deadline stranded owned child")
	}
	close(c.release)
	if err = s.Close(context.Background()); err != nil {
		t.Fatal("cleanup retry did not finish", err)
	}
}
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

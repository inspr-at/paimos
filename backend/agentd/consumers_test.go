// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
	"os"
	"path/filepath"
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
func TestCodexInboxUsesPinnedQueueAndDiscardsVendorOutput(t *testing.T) {
	// The only executable is a fixture script; no operator Codex process runs.
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-fixture")
	script := "#!/bin/sh\n[ \"$1\" = queue ] && [ \"$2\" = --thread ] && [ \"$3\" = fixture-thread ] && [ \"$4\" = --message ] && [ \"$5\" = fixture-text ] || exit 9\nprintf 'fixture vendor output'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := &codexProcess{executable: path, threadID: "fixture-thread"}
	effect, err := p.Inbox(context.Background(), ControlRequest{CorrelationID: "fixture", Text: "fixture-text"})
	if err != nil || effect.Primitive != "codex queue --thread" || effect.CorrelationID != "fixture" || effect.VendorMessageID != "" {
		t.Fatal("queue handoff not proven")
	}
}

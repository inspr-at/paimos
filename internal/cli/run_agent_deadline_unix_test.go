// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

func TestLocalRunnerCancelsBothPhasesAndDescendants(t *testing.T) {
	for _, tests := range []bool{false, true} {
		for _, stop := range []bool{false, true} {
			t.Run(map[bool]string{false: "agent", true: "tests"}[tests]+"/"+map[bool]string{false: "parent_cancel", true: "stop"}[stop], func(t *testing.T) {
				fixture := processtest.New(t)
				opts := runAgentOptions{Exec: fixture.Script, Yes: true}
				if tests {
					opts.Exec = "true"
					opts.TestExec = fixture.Script
				}
				a := &localClaudeAdapter{opts: opts, homes: map[string]string{"local": ""}, stdout: io.Discard, stderr: io.Discard, maxRun: time.Minute}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				p, err := a.Start(ctx, agentd.StartRequest{Run: agentd.Run{ID: "fixture"}, AccountKey: "local", Workspace: t.TempDir(), StateRoot: t.TempDir()}, nil)
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- p.Wait() }()
				fixture.Ready(t)
				if stop {
					stopCtx, stopCancel := context.WithTimeout(t.Context(), 3*time.Second)
					err := p.Stop(stopCtx)
					stopCancel()
					if err != nil {
						t.Error(err)
					}
				} else {
					cancel()
				}
				select {
				case err := <-done:
					if err == nil {
						t.Error("canceled run reported success")
					}
				case <-time.After(3 * time.Second):
					t.Error("run outlived cancellation")
				}
				fixture.AssertExited(t)
			})
		}
	}
}

func TestLocalRunnerProbeCancelsDescendants(t *testing.T) {
	fixture := processtest.New(t)
	bin := t.TempDir()
	// Private synthetic executable; no vendor binary or account is accessed.
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+fixture.Script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	a := &localClaudeAdapter{opts: runAgentOptions{Exec: "claude"}, homes: map[string]string{"local": ""}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- a.Probe(ctx, "local") }()
	fixture.Ready(t)
	cancel()
	select {
	case available := <-done:
		if available {
			t.Error("canceled account probe reported available")
		}
	case <-time.After(3 * time.Second):
		t.Error("account probe stuck on inherited pipe")
	}
	fixture.AssertExited(t)
}

// The deadline is advanced explicitly after the test process opens its witness
// pipe. No scheduler delay determines whether cancellation happened mid-tests.
type acceptedDeadline struct {
	context.Context
	expired chan struct{}
	at      time.Time
}

func (c *acceptedDeadline) Done() <-chan struct{}       { return c.expired }
func (c *acceptedDeadline) Deadline() (time.Time, bool) { return c.at, true }
func (c *acceptedDeadline) Err() error {
	select {
	case <-c.expired:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestLocalRunnerAcceptedDeadlineSurvivesDispatch(t *testing.T) {
	fixture := processtest.New(t)
	a := &localClaudeAdapter{opts: runAgentOptions{Exec: "true", TestExec: fixture.Script, Yes: true}, homes: map[string]string{"local": ""}, stdout: io.Discard, stderr: io.Discard, maxRun: time.Hour}
	lifetime := &acceptedDeadline{Context: context.Background(), expired: make(chan struct{}), at: time.Now().Add(time.Hour)}
	defer func() {
		select {
		case <-lifetime.expired:
		default:
			close(lifetime.expired)
		}
	}()
	dispatch, finishDispatch := context.WithCancel(t.Context())
	p, err := a.Start(dispatch, agentd.StartRequest{Lifetime: lifetime, Run: agentd.Run{ID: "fixture"}, AccountKey: "local", Workspace: t.TempDir(), StateRoot: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Ready(t)
	finishDispatch()
	// Inspect the accepted context directly; an absence-of-exit timer could
	// falsely pass if the wrong cancellation simply had not been scheduled yet.
	if err := p.(*localRunnerProcess).ctx.Err(); err != nil {
		t.Fatalf("dispatch ended the accepted run: %v", err)
	}
	close(lifetime.expired)
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("accepted deadline lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("tests outlived accepted deadline")
	}
	fixture.AssertExited(t)
}

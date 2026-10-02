// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
)

func TestDaemonProbeInheritedPipes(t *testing.T) {
	for _, cancelRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit", true: "cancel"}[cancelRoot], func(t *testing.T) {
			fixture := processtest.New(t)
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "probe")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+fixture.Script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, _, err := probeRun(ctx, path, nil); done <- err }()
			fixture.Ready(t)
			if cancelRoot {
				cancel()
			} else {
				fixture.ExitRoot()
			}
			select {
			case err := <-done:
				if cancelRoot && err == nil {
					t.Error("canceled probe reported success")
				}
			case <-time.After(3 * time.Second):
				t.Error("probe stuck on inherited pipe")
			}
			fixture.AssertExited(t)
		})
	}
}

func TestWireBlockedWriteCleansOwnedDescendants(t *testing.T) {
	for _, control := range []bool{false, true} {
		t.Run(map[bool]string{false: "startup", true: "control"}[control], func(t *testing.T) {
			fixture := processtest.New(t)
			path, err := filepath.EvalSymlinks("/bin/sh")
			if err != nil {
				t.Fatal(err)
			}
			p, err := launchWire(path, []string{"-c", fixture.Script}, t.TempDir(), nil, "bridge", nil)
			if err != nil {
				t.Fatal(err)
			}
			fixture.Ready(t)
			input := &enteredPipe{File: p.stdin.(*os.File), entered: make(chan struct{})}
			p.stdin = input
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if control {
					cp := &claudeProcess{wireProcess: p, controls: map[string]chan bool{}}
					done <- cp.Control(ctx, "steer", strings.Repeat("x", 4<<20))
				} else {
					_, err := p.request(ctx, "jsonrpc", "initialize", strings.Repeat("x", 4<<20))
					done <- err
				}
			}()
			<-input.entered
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				_ = input.Close()
				<-done
				t.Error("blocked wire write ignored cancellation")
			}
			if !p.ProcessExited() {
				t.Error("write returned before owned child cleanup")
			}
			fixture.ExitRoot()
			p.discardReader()
			fixture.AssertExited(t)
		})
	}
}

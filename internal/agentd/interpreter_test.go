// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/piprobe"
)

func TestNpmAdapterProbeAndLaunch(t *testing.T) {
	for _, harness := range []string{Codex, Cursor} {
		t.Run(harness, func(t *testing.T) {
			r := adapterRequest(t)
			home := r.StateRoot
			path := fakeVendorPath(t, harness)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			node := harnesslaunch.Node{Path: filepath.Join(home, "node"), Version: "22.19.0"}
			if err := os.WriteFile(node.Path, []byte("#!/bin/sh\n[ -z \"$NODE_OPTIONS\" ] && [ -z \"$NODE_PATH\" ] || exit 126\nexec /bin/sh \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			script := "#!/usr/bin/env node\nif [ \"$1\" = --version ]; then echo 1.2.3; exit; fi\n" + strings.TrimPrefix(string(raw), "#!/bin/sh\n")
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", t.TempDir())
			t.Setenv("NODE_OPTIONS", "synthetic")
			t.Setenv("NODE_PATH", "synthetic")
			nodes := map[string]harnesslaunch.Node{"account": node}
			var a interface {
				Adapter
				ProbeStatus(context.Context, string) (bool, error)
			}
			if harness == Codex {
				c := NewCodexAdapter(path, map[string]string{"account": home})
				c.Nodes = nodes
				c.SetExpectedEmails(map[string]string{"account": "agent@example.test"})
				a = c
			} else {
				c := NewCursorAdapter(path, map[string]string{"account": "42"})
				c.Nodes = nodes
				a = c
			}
			if available, err := a.ProbeStatus(t.Context(), "account"); !available || err != nil {
				t.Fatal("pinned probe failed", err)
			}
			r.Profile.Harness = harness
			p, err := a.Start(t.Context(), r, func(AdapterEvent) {})
			if err != nil {
				t.Fatal("pinned run failed", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := p.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			nodes["account"] = harnesslaunch.Node{}
			if available, err := a.ProbeStatus(t.Context(), "account"); available || !errors.Is(err, harnesslaunch.ErrStart) {
				t.Fatal("missing pin reported as login", err)
			}
			if _, err := a.Start(t.Context(), r, func(AdapterEvent) {}); !errors.Is(err, harnesslaunch.ErrStart) {
				t.Fatal("start lost failure classification", err)
			}
		})
	}
}

func TestPiProbeLockIsPerAccount(t *testing.T) {
	r := adapterRequest(t)
	slow, fast := filepath.Join(r.StateRoot, "slow"), filepath.Join(r.StateRoot, "fast")
	for _, dir := range []string{slow, fast} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := fakeVendorPath(t, "pi")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entered := filepath.Join(slow, "entered")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$PI_CODING_AGENT_DIR\" = %q ]; then : > %q; IFS= read -r line; IFS= read -r line; exit; fi\n", slow, entered) + strings.TrimPrefix(string(raw), "#!/bin/sh\n")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := NewPiAdapter(path, map[string]string{"slow": slow, "fast": fast})
	a.SetExpectedProviders(map[string]string{"slow": "anthropic", "fast": "anthropic"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan struct{})
	go func() { defer close(finished); a.Probe(ctx, "slow") }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow probe did not enter")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fastDone := make(chan bool, 1)
	go func() { fastDone <- a.Probe(t.Context(), "fast") }()
	select {
	case available := <-fastDone:
		if !available {
			t.Fatal("healthy account failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("another account held global probe lock")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not cancel")
	}
	if err := os.Chmod(fast, 0755); err != nil {
		t.Fatal(err)
	}
	if available, err := a.ProbeStatus(t.Context(), "fast"); available || !errors.Is(err, piprobe.ErrPrivateProfile) {
		t.Fatal("permissions need a distinct hint", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
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

type probeLog struct {
	*fakeAPI
	mu     sync.Mutex
	probes []probeCall
}

type probeCall struct {
	id string
	ok bool
}

func (p *probeLog) Probe(_ context.Context, id, _, _ string, ok bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probes = append(p.probes, probeCall{id, ok})
	return nil
}

type recordingAdapter struct {
	fakeAdapter
	mu      sync.Mutex
	keys    []string
	starts  int
	started []string
}

func (a *recordingAdapter) Probe(ctx context.Context, key string) bool {
	a.mu.Lock()
	a.keys = append(a.keys, key)
	a.mu.Unlock()
	return a.fakeAdapter.Probe(ctx, key)
}

func (a *recordingAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	a.mu.Lock()
	a.starts++
	a.started = append(a.started, r.AccountKey)
	a.mu.Unlock()
	return a.fakeAdapter.Start(ctx, r, observe)
}

func TestUnpinnedAccountDoesNotBlockSibling(t *testing.T) {
	s, api, process := testSupervisor(t)
	rec := &recordingAdapter{fakeAdapter: fakeAdapter{proc: process}}
	logged := &probeLog{fakeAPI: api}
	s.mu.Lock()
	s.api = logged
	s.adapters[Codex] = rec
	s.accounts = append(s.accounts, EnrolledAccount{ID: "old", Key: "old-local", Harness: Codex, DependencyBlocked: true})
	s.blockedAccounts["old"] = true
	if s.harnessFailed == nil {
		s.harnessFailed = map[string]bool{}
	}
	s.harnessFailed["old"] = true
	s.mu.Unlock()
	before := s.Lifecycle("")
	if before.Ready || before.HarnessFailed || len(before.HarnessFailedAccountIDs) != 1 || before.HarnessFailedAccountIDs[0] != "old" || len(before.BlockedAccounts) != 1 || before.BlockedAccounts[0].Reason != "pin_missing" || before.BlockedAccounts[0].Fix != "add-harness" || before.BlockedAccounts[0].Harness != Codex {
		t.Fatalf("sibling marked failed before probe: %+v", before)
	}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	keys, starts, started := append([]string(nil), rec.keys...), rec.starts, append([]string(nil), rec.started...)
	rec.mu.Unlock()
	if len(keys) != 1 || keys[0] != "local" || starts != 1 || len(started) != 1 || started[0] != "local" {
		t.Fatalf("unpinned account was probed or launched: keys=%v starts=%d started=%v", keys, starts, started)
	}
	logged.mu.Lock()
	sawBlocked := false
	for _, call := range logged.probes {
		if call.id == "old" {
			sawBlocked = !call.ok
		}
		if call.id == "account" && !call.ok {
			t.Fatal("healthy account reported unavailable")
		}
	}
	logged.mu.Unlock()
	if !sawBlocked || len(api.routeAccounts) != 1 || api.routeAccounts[0] != "account" || api.claims != 1 {
		t.Fatalf("sibling starved: blocked=%v routes=%v claims=%d", sawBlocked, api.routeAccounts, api.claims)
	}
	status := s.Lifecycle("")
	if !status.Ready || status.HarnessFailed || len(status.HarnessFailedAccountIDs) != 1 || status.HarnessFailedAccountIDs[0] != "old" || len(status.BlockedAccounts) != 1 || status.BlockedAccounts[0].AccountID != "old" || status.BlockedAccounts[0].Reason != "pin_missing" || status.BlockedAccounts[0].Fix != "add-harness" {
		t.Fatalf("status after a blocked sibling: %+v", status)
	}
	run := api.run
	run.ID = "blocked-run"
	run.AccountID = "old"
	run.RequestedAccountID = "old"
	if err := s.StartRun(t.Context(), run); err == nil || rec.starts != 1 {
		t.Fatal("unpinned account accepted a run", err)
	}
}

func TestBlockedEnrollmentStartsWithoutAdapters(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	if err := os.Mkdir(workspace, 0700); err != nil || os.Mkdir(state, 0700) != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	s, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: state, DaemonID: "daemon", Workspace: workspace, EstimatedUnits: map[string]int64{"requests": 1},
		Accounts: []EnrolledAccount{{ID: "old", Key: "old-local", Harness: Codex, DependencyBlocked: true}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	status := s.Lifecycle("")
	if status.Ready || !status.HarnessFailed || len(status.HarnessFailedAccountIDs) != 1 || status.HarnessFailedAccountIDs[0] != "old" {
		t.Fatalf("sole unpinned enrollment did not stay blocked: %+v", status)
	}
	if s.accountAvailable("old") {
		t.Fatal("sole unpinned enrollment is launchable")
	}
	if len(status.BlockedAccounts) != 1 || status.BlockedAccounts[0].Reason != "pin_missing" || status.BlockedAccounts[0].Fix != "add-harness" {
		t.Fatalf("sole block omitted the fix: %+v", status.BlockedAccounts)
	}
}

func TestDriftedPinRefreshKeepsSiblingPolling(t *testing.T) {
	s, api, process := testSupervisor(t)
	rec := &recordingAdapter{fakeAdapter: fakeAdapter{proc: process}}
	logged := &probeLog{fakeAPI: api}
	s.mu.Lock()
	s.api = logged
	s.adapters[Codex] = rec
	s.mu.Unlock()
	refreshed := []EnrolledAccount{
		{ID: "account", Key: "local", Harness: Codex},
		{ID: "drifted", Key: "drifted-local", Harness: Codex, DependencyBlocked: true, PinReason: agentsetup.PinDrifted, PinFix: agentsetup.FixRepin},
	}
	if err := s.RefreshAccounts(refreshed, []Adapter{rec}); err != nil {
		t.Fatal(err)
	}
	if s.accountAvailable("drifted") || !s.PinHealthMatches(refreshed) {
		t.Fatal("drifted account stayed launchable")
	}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	keys, starts, started := append([]string(nil), rec.keys...), rec.starts, append([]string(nil), rec.started...)
	rec.mu.Unlock()
	if len(keys) != 1 || keys[0] != "local" || starts != 1 || len(started) != 1 || started[0] != "local" {
		t.Fatalf("drifted account was probed or launched: keys=%v starts=%d started=%v", keys, starts, started)
	}
	status := s.Lifecycle("")
	raw, err := json.Marshal(status)
	if err != nil || !status.Ready || status.HarnessFailed || len(status.BlockedAccounts) != 1 || status.BlockedAccounts[0].AccountID != "drifted" || status.BlockedAccounts[0].Harness != Codex || status.BlockedAccounts[0].Reason != agentsetup.PinDrifted || status.BlockedAccounts[0].Fix != agentsetup.FixRepin || !strings.Contains(string(raw), `"blocked_accounts"`) {
		t.Fatalf("status lost the drifted pin: %+v %s", status.BlockedAccounts, raw)
	}
	repaired := []EnrolledAccount{{ID: "drifted", Key: "drifted-local", Harness: Codex}}
	if err := s.RefreshAccounts(repaired, []Adapter{rec}); err != nil || !s.accountAvailable("drifted") || s.PinHealthMatches(refreshed) {
		t.Fatal("repaired pin stayed blocked", err)
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

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type machineReporter struct {
	machine string
	err     error
}

func (*machineReporter) ReportStatus(context.Context, Status) error { return nil }
func (r *machineReporter) AuthenticatedMachineID(context.Context) (string, error) {
	return r.machine, r.err
}

func TestStartConstraintsProbeBeforeAnySpawn(t *testing.T) {
	for _, tc := range []struct {
		name, account, expectedAccount, machine, expectedMachine string
		unavailable, pass                                        bool
	}{
		{name: "matching", account: "chatgpt", expectedAccount: "chatgpt", machine: "fixture-host", expectedMachine: "fixture-host", pass: true},
		{name: "account mismatch", account: "api_key", expectedAccount: "chatgpt"},
		{name: "unknown account", account: "unknown", expectedAccount: "chatgpt"},
		{name: "unknown is not a constraint", account: "chatgpt", expectedAccount: "unknown"},
		{name: "machine mismatch", machine: "fixture-host", expectedMachine: "other-host"},
		{name: "unauthenticated host is not authority", machine: "fixture-host", expectedMachine: "fixture-host", unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &dispatchAdapter{label: tc.account}
			reporter := &machineReporter{machine: tc.machine}
			if tc.unavailable {
				reporter.err = errors.New("unavailable")
			}
			supervisor, err := NewSupervisor(SupervisorConfig{Instance: "fixture", Adapters: []Adapter{adapter}, Reporter: reporter})
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close(context.Background())
			_, err = supervisor.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), ProjectID: 42, Prompt: "fixture", Identity: "codex:worker", ExpectedAccountLabel: tc.expectedAccount, ExpectedMachineID: tc.expectedMachine})
			if (err == nil) != tc.pass || (len(adapter.requests) == 1) != tc.pass {
				t.Fatalf("pass=%v spawned=%d error=%v", tc.pass, len(adapter.requests), err)
			}
		})
	}
}

func TestTrustedReservedStartNeverReusesGenerationForAnotherIntent(t *testing.T) {
	a := &dispatchAdapter{label: "chatgpt"}
	s, e := NewSupervisor(SupervisorConfig{Instance: "fixture", StateRoot: t.TempDir(), Adapters: []Adapter{a}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close(context.Background())
	request := StartRequest{Adapter: AdapterCodex, ProjectID: 42, Identity: "codex:fixture", Workspace: t.TempDir(), Prompt: "fixture", IdempotencyKey: "lifecycle:first"}
	generation := uuid.NewString()
	session, e := s.StartReserved(context.Background(), request, generation)
	if e != nil || session.ID != generation {
		t.Fatal("reserved generation not used", e)
	}
	if _, e = s.StartReserved(context.Background(), request, generation); e != nil || len(a.requests) != 1 {
		t.Fatal("same intent respawned", e)
	}
	request.IdempotencyKey = "lifecycle:other"
	if _, e = s.StartReserved(context.Background(), request, generation); !errors.Is(e, ErrStartReplayConflict) || len(a.requests) != 1 {
		t.Fatal("generation was reused by another intent", e)
	}
}

func TestStartIdempotencyConcurrentReplayRecoveryAndPrivacy(t *testing.T) {
	root := t.TempDir()
	adapter := &dispatchAdapter{label: "chatgpt"}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), ProjectID: 42, Identity: "codex:worker", IdempotencyKey: "private-fixture-key", Prompt: "private-fixture-prompt"}
	var wg sync.WaitGroup
	results := make(chan Session, 8)
	for range 8 {
		wg.Go(func() {
			s, e := supervisor.Start(context.Background(), request)
			if e != nil {
				t.Error(e)
			}
			results <- s
		})
	}
	wg.Wait()
	close(results)
	first := ""
	for s := range results {
		if first == "" {
			first = s.ID
		}
		if s.ID != first {
			t.Fatal("duplicate generation")
		}
	}
	if len(adapter.requests) != 1 {
		t.Fatal("duplicate spawn")
	}
	lookup, e := supervisor.LookupStart(context.Background(), request.IdempotencyKey)
	if e != nil || lookup.ID != first {
		t.Fatal("lost original outcome")
	}
	conflict := request
	conflict.Prompt = "different"
	if _, err := supervisor.Start(context.Background(), conflict); !errors.Is(err, ErrStartReplayConflict) {
		t.Fatal("conflicting retry accepted")
	}
	dir, _ := InstanceStateDir(root, "fixture")
	paths, _ := filepath.Glob(filepath.Join(dir, "starts*"))
	for _, p := range paths {
		raw, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(raw), request.Prompt) || strings.Contains(string(raw), request.IdempotencyKey) {
			t.Fatal("start journal persisted private input")
		}
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0600 {
			t.Fatal("unsafe journal")
		}
	}
	// Simulate loss of the final outcome checkpoint after the session is durable.
	for _, record := range supervisor.starts.journal.Snapshot() {
		record.Outcome = "unknown"
		record.Session = nil
		if err := supervisor.starts.journal.Put(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewSupervisor(SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	if err := os.Remove(request.Workspace); err != nil {
		t.Fatal(err)
	}
	replay, err := recovered.Start(context.Background(), request)
	if err != nil || replay.ID != first || replay.State != StateOwnershipLost || replay.PID != 0 || len(adapter.requests) != 1 {
		t.Fatal("recovery did not preserve original generation without adopting ownership")
	}
}

type ambiguousStartAdapter struct{ dispatchAdapter }

func (*ambiguousStartAdapter) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	return nil, errors.New("ambiguous adapter result")
}
func TestStartAmbiguousAttemptNeverRespawns(t *testing.T) {
	root := t.TempDir()
	adapter := &ambiguousStartAdapter{}
	s, err := NewSupervisor(SupervisorConfig{StateRoot: root, Instance: "fixture", Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), ProjectID: 42, Prompt: "fixture", Identity: "codex:worker", IdempotencyKey: "attempt"}
	if _, err := s.Start(context.Background(), request); err == nil {
		t.Fatal("expected ambiguous failure")
	}
	s.Close(context.Background())
	s, err = NewSupervisor(SupervisorConfig{StateRoot: root, Instance: "fixture", Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if _, err := s.Start(context.Background(), request); !errors.Is(err, ErrStartOutcomeUnknown) {
		t.Fatal("ambiguous attempt was retried")
	}
}

func TestStartJournalRejectsConflictingAccountKeyRetry(t *testing.T) {
	adapter := &keyedDispatchAdapter{dispatchAdapter: dispatchAdapter{label: "chatgpt"}, keys: map[string]bool{"one": true, "two": true}}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "account-retry", StateRoot: t.TempDir(), Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	request := StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:worker", ProjectID: 952, IdempotencyKey: "retry-key", AccountKey: "one"}
	session, err := supervisor.Start(context.Background(), request)
	if err != nil || session.AccountKey != "one" {
		t.Fatalf("first start=%+v err=%v", session, err)
	}
	conflict := request
	conflict.AccountKey = "two"
	if _, err := supervisor.Start(context.Background(), conflict); !errors.Is(err, ErrStartReplayConflict) || len(adapter.requests) != 1 {
		t.Fatalf("conflicting account retry spawned=%d err=%v", len(adapter.requests), err)
	}
	dropped := request
	dropped.AccountKey = ""
	if _, err := supervisor.Start(context.Background(), dropped); !errors.Is(err, ErrStartReplayConflict) || len(adapter.requests) != 1 {
		t.Fatalf("dropped account retry spawned=%d err=%v", len(adapter.requests), err)
	}
	replay, err := supervisor.Start(context.Background(), request)
	if err != nil || replay.ID != session.ID || replay.AccountKey != "one" || len(adapter.requests) != 1 {
		t.Fatalf("exact account retry respawned: %+v err=%v spawned=%d", replay, err, len(adapter.requests))
	}
}

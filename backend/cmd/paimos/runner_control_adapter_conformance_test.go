// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDurableControlAdapter struct {
	store          *fakeDurableControlStore
	reconcileCount int
}

type fakeDurableControlStore struct {
	mu           sync.Mutex
	results      map[string]runnerControlDecision
	applyCount   map[string]int
	input        runnerControlEffect
	runtimeState string
}

func newFakeDurableControlAdapter() *fakeDurableControlAdapter {
	return &fakeDurableControlAdapter{store: &fakeDurableControlStore{results: map[string]runnerControlDecision{},
		applyCount: map[string]int{}, runtimeState: "running"}}
}

func (a *fakeDurableControlAdapter) restart() *fakeDurableControlAdapter {
	return &fakeDurableControlAdapter{store: a.store}
}

func (*fakeDurableControlAdapter) SupportedActions() []string {
	return []string{"run.cancel.running", "input.respond", "run.pause", "run.resume"}
}

func (a *fakeDurableControlAdapter) Apply(_ context.Context, effect runnerControlEffect) (runnerControlDecision, error) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if result, exists := a.store.results[effect.CommandID]; exists {
		return result, nil
	}
	a.store.applyCount[effect.CommandID]++
	switch effect.Action {
	case "input.respond":
		a.store.input = effect
	case "run.pause":
		a.store.runtimeState = "paused"
	case "run.resume":
		a.store.runtimeState = "running"
	}
	result := runnerControlDecision{Outcome: "applied"}
	a.store.results[effect.CommandID] = result
	return result, nil
}

func (a *fakeDurableControlAdapter) Reconcile(_ context.Context, effect runnerControlEffect) (runnerControlDecision, bool, error) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	a.reconcileCount++
	result, exists := a.store.results[effect.CommandID]
	return result, exists, nil
}

func TestRunnerControlFakeAdapterConformance(t *testing.T) {
	t.Run("typed input pause resume and durable dedupe", func(t *testing.T) {
		target := runnerControlTargetFixture(17)
		lease := runnerControlLease{LeaseID: "lease-opaque", Revision: 1, DeliveryKey: target.DeliveryKey,
			IssueKey: "PAI-809", Actions: append([]string(nil), runnerControlActionOrder...),
			ExpiresAt: time.Now().Add(time.Minute), Target: target}
		input := runnerControlEffect{OutboxID: 1, CommandID: "command-input", Action: "input.respond",
			EffectSequence: 1, LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, Target: target,
			InputRequestID: "request-opaque", InputRevision: 3, InputResponse: "choice", ChoiceOrdinal: 2,
			ChoiceCode: "choice_2"}
		pause := runnerControlEffect{OutboxID: 2, CommandID: "command-pause", Action: "run.pause",
			EffectSequence: 1, LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, Target: target, RuntimeRevision: 7}
		resume := runnerControlEffect{OutboxID: 3, CommandID: "command-resume", Action: "run.resume",
			EffectSequence: 1, LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, Target: target, RuntimeRevision: 8}
		effects := []runnerControlEffect{input, pause, resume}
		byCommand := map[string]runnerControlEffect{input.CommandID: input, pause.CommandID: pause, resume.CommandID: resume}
		var mu sync.Mutex
		results := map[string]int{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var raw map[string]json.RawMessage
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &raw)
			var operation string
			_ = json.Unmarshal(raw["operation"], &operation)
			switch {
			case strings.HasSuffix(r.URL.Path, "/control-capability-leases") && operation == "":
				var actions []string
				_ = json.Unmarshal(raw["supported_actions"], &actions)
				if !reflect.DeepEqual(actions, runnerControlActionOrder) {
					t.Errorf("leased actions=%v", actions)
				}
				_ = json.NewEncoder(w).Encode(lease)
			case strings.HasSuffix(r.URL.Path, "/control-commands"):
				var cursor int64
				_ = json.Unmarshal(raw["cursor"], &cursor)
				if cursor == 0 {
					_ = json.NewEncoder(w).Encode(runnerControlPull{SnapshotHighWater: 3, NextCursor: 3, Effects: effects})
				} else {
					_ = json.NewEncoder(w).Encode(runnerControlPull{SnapshotHighWater: cursor, NextCursor: cursor})
				}
			case operation == "claim":
				commandID := strings.TrimPrefix(r.URL.Path, "/api/control-commands/")
				_ = json.NewEncoder(w).Encode(byCommand[commandID])
			case operation == "result":
				commandID := strings.TrimPrefix(r.URL.Path, "/api/control-commands/")
				mu.Lock()
				results[commandID]++
				mu.Unlock()
				_, _ = w.Write([]byte(`{}`))
			case operation == "revoke":
				_, _ = w.Write([]byte(`{}`))
			default:
				http.Error(w, "unexpected control request", http.StatusBadRequest)
			}
		}))
		defer server.Close()

		adapter := newFakeDurableControlAdapter()
		arbiter := newRunControlArbiterWithAdapter(&Client{baseURL: server.URL, apiKey: "test", http: server.Client()},
			17, "device-a", nil, adapter)
		arbiter.pullInterval, arbiter.renewEvery = time.Millisecond, time.Hour
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := arbiter.start(ctx, true); err != nil {
			t.Fatal(err)
		}
		waitForRunnerControl(t, ctx, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return results[input.CommandID] == 1 && results[pause.CommandID] == 1 && results[resume.CommandID] == 1
		})
		arbiter.stop(ctx)
		if replay, err := adapter.Apply(context.Background(), pause); err != nil || replay.Outcome != "applied" {
			t.Fatalf("dedupe replay=%+v err=%v", replay, err)
		}

		adapter.store.mu.Lock()
		defer adapter.store.mu.Unlock()
		if adapter.store.input.InputRequestID != "request-opaque" || adapter.store.input.InputRevision != 3 ||
			adapter.store.input.InputResponse != "choice" || adapter.store.input.ChoiceOrdinal != 2 ||
			adapter.store.input.ChoiceCode != "choice_2" {
			t.Fatalf("typed input=%+v", adapter.store.input)
		}
		if adapter.store.runtimeState != "running" || adapter.store.applyCount[pause.CommandID] != 1 ||
			adapter.store.applyCount[input.CommandID] != 1 || adapter.store.applyCount[resume.CommandID] != 1 {
			t.Fatalf("state=%s applyCount=%v", adapter.store.runtimeState, adapter.store.applyCount)
		}
	})

	t.Run("claimed effect reconciles without reapply", func(t *testing.T) {
		target := runnerControlTargetFixture(17)
		lease := runnerControlLease{LeaseID: "lease-opaque", Revision: 1, DeliveryKey: target.DeliveryKey,
			IssueKey: "PAI-809", Actions: append([]string(nil), runnerControlActionOrder...),
			ExpiresAt: time.Now().Add(time.Minute), Target: target}
		effect := runnerControlEffect{CommandID: "command-input", Action: "input.respond", EffectSequence: 1,
			LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, Target: target, InputRequestID: "request-opaque",
			InputRevision: 3, InputResponse: "approve"}
		adapter := newFakeDurableControlAdapter()
		if _, err := adapter.Apply(context.Background(), effect); err != nil {
			t.Fatal(err)
		}
		journal, err := openRunnerControlJournal(filepath.Join(t.TempDir(), "runner-state"))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(effect.CommandID + "\x00claimed"))
		if err := journal.put(runnerControlJournalRecord{CommandID: effect.CommandID, LeaseID: effect.LeaseID,
			LeaseRevision: effect.LeaseRevision, EffectSequence: 1, ClaimSequence: 1, ResultSequence: 1,
			RequestDigest: hex.EncodeToString(digest[:]), Outcome: "outcome_unknown", State: "claimed", Effect: &effect}); err != nil {
			t.Fatal(err)
		}
		var resultCount int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Operation string `json:"operation"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Operation {
			case "":
				_ = json.NewEncoder(w).Encode(lease)
			case "claim":
				_ = json.NewEncoder(w).Encode(effect)
			case "result":
				resultCount++
				_, _ = w.Write([]byte(`{}`))
			case "revoke":
				_, _ = w.Write([]byte(`{}`))
			default:
				http.Error(w, "unexpected operation", http.StatusBadRequest)
			}
		}))
		defer server.Close()
		restartedAdapter := adapter.restart()
		arbiter := newRunControlArbiterWithAdapter(&Client{baseURL: server.URL, apiKey: "test", http: server.Client()},
			17, "device-a", journal, restartedAdapter)
		arbiter.pullInterval, arbiter.renewEvery = time.Hour, time.Hour
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := arbiter.start(ctx, true); err != nil {
			t.Fatal(err)
		}
		arbiter.stop(ctx)
		adapter.store.mu.Lock()
		defer adapter.store.mu.Unlock()
		if adapter.store.applyCount[effect.CommandID] != 1 || restartedAdapter.reconcileCount != 1 || resultCount != 1 ||
			len(journal.snapshot()) != 0 {
			t.Fatalf("apply=%d reconcile=%d results=%d journal=%+v", adapter.store.applyCount[effect.CommandID],
				restartedAdapter.reconcileCount, resultCount, journal.snapshot())
		}
	})
}

func TestOneShotRunnerControlAdapterAdvertisesOnlyOwnedCancellation(t *testing.T) {
	actions, err := canonicalRunnerControlActions((oneShotRunnerControlAdapter{}).SupportedActions())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"run.cancel.running"}) {
		t.Fatalf("one-shot actions=%v", actions)
	}
	for _, invalid := range [][]string{nil, {"run.pause"}, {"run.resume"}, {"run.pause", "run.resume", "run.resume"}} {
		if _, err := canonicalRunnerControlActions(invalid); err == nil {
			t.Fatalf("invalid capability set was accepted: %v", invalid)
		}
	}
}

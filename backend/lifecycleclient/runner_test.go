// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

type fixtureAuthority struct {
	in                          lifecycleintents.Intent
	calls                       []string
	loseExecuting, loseComplete bool
}

func (f *fixtureAuthority) RegisterRuntime(context.Context, lifecycleintents.Registration) (lifecycleintents.Runtime, error) {
	panic("unused")
}
func (f *fixtureAuthority) RegisterSession(context.Context, string, lifecycleintents.SessionRegistration, string) error {
	panic("unused")
}
func (f *fixtureAuthority) Claim(context.Context, string) (*lifecycleintents.Intent, error) {
	f.calls = append(f.calls, "claim")
	if f.in.State == "completed" || f.in.State == "failed" {
		return nil, nil
	}
	out := f.in
	return &out, nil
}
func (f *fixtureAuthority) Transition(_ context.Context, id string, tr lifecycleintents.Transition) (lifecycleintents.Intent, error) {
	f.calls = append(f.calls, tr.State)
	if f.in.ID != id {
		return lifecycleintents.Intent{}, ErrOwnership
	}
	if f.in.State == "completed" || f.in.State == "failed" {
		if f.in.State != tr.State || f.in.Reason != tr.Reason || f.in.ResultSessionID != tr.ResultSessionID {
			return lifecycleintents.Intent{}, ErrOwnership
		}
		return f.in, nil
	}
	if tr.ExpectedRevision != f.in.Revision {
		return lifecycleintents.Intent{}, ErrOwnership
	}
	f.in.State = tr.State
	f.in.Reason = tr.Reason
	f.in.Revision++
	f.in.ResultSessionID = tr.ResultSessionID
	if tr.State == "executing" && f.loseExecuting {
		f.loseExecuting = false
		return lifecycleintents.Intent{}, ErrTransport
	}
	if tr.State == "completed" && f.loseComplete {
		f.loseComplete = false
		return lifecycleintents.Intent{}, ErrTransport
	}
	return f.in, nil
}

type fixtureExecutor struct {
	effects, commits int
	before           func()
	fail             bool
}

func (f *fixtureExecutor) Prepare(context.Context, lifecycleintents.Intent) error { return nil }
func (f *fixtureExecutor) Execute(context.Context, lifecycleintents.Intent) (Result, error) {
	if f.before != nil {
		f.before()
	}
	f.effects++
	if f.fail {
		return Result{}, ErrUnknown
	}
	return Result{Reason: "applied", SessionID: "33333333-3333-4333-8333-333333333333"}, nil
}
func (f *fixtureExecutor) Committed(context.Context, lifecycleintents.Intent) error {
	f.commits++
	return nil
}
func runtimeFixture() (lifecycleintents.Runtime, *fixtureAuthority) {
	r := lifecycleintents.Runtime{ID: uuid.NewString(), Generation: uuid.NewString(), ProjectID: 42, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	a := &fixtureAuthority{in: lifecycleintents.Intent{SchemaVersion: 1, ID: uuid.NewString(), ProjectID: 42, State: "claimed", Revision: 2, NewGeneration: uuid.NewString(), Request: lifecycleintents.Request{RequestKey: uuid.NewString(), RuntimeID: r.ID, RuntimeGeneration: r.Generation, Operation: "start", AgentName: "fixture", AccountLabel: "chatgpt"}}}
	return r, a
}
func openFixtureRunner(t *testing.T, dir string, r lifecycleintents.Runtime, a *fixtureAuthority, e *fixtureExecutor) *Runner {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(dir, r.Generation, a, e)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
func TestLifecycleJournalBeforeEffectAndLostCompletionReplay(t *testing.T) {
	r, a := runtimeFixture()
	a.loseComplete = true
	e := &fixtureExecutor{}
	dir := t.TempDir()
	runner := openFixtureRunner(t, dir, r, a, e)
	e.before = func() {
		entries := runner.journal.Snapshot()
		if len(entries) != 1 || entries[0].Phase != "executing" || entries[0].Intent.State != "executing" {
			t.Fatal("effect preceded durable executing journal")
		}
	}
	if err := runner.Step(context.Background(), r); !errors.Is(err, ErrTransport) || e.effects != 1 {
		t.Fatal("lost complete fixture failed", err)
	}
	runner = openFixtureRunner(t, dir, r, a, e)
	if err := runner.Step(context.Background(), r); err != nil || e.effects != 1 || e.commits != 1 {
		t.Fatal("terminal completion did not replay without spawn", err)
	}
	if err := runner.Step(context.Background(), r); err != nil || e.effects != 1 {
		t.Fatal("completed intent repeated")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "lifecycle-*"))
	for _, f := range files {
		info, _ := os.Stat(f)
		if info.Mode().Perm() != 0600 {
			t.Fatal("journal mode unsafe")
		}
		raw, _ := os.ReadFile(f)
		if strings.Contains(string(raw), "prompt") || strings.Contains(string(raw), "target_ref") {
			t.Fatal("journal persisted execution content")
		}
	}
}
func TestLifecycleLostExecutingNeverCallsExecutorOnRecovery(t *testing.T) {
	r, a := runtimeFixture()
	a.loseExecuting = true
	e := &fixtureExecutor{}
	dir := t.TempDir()
	runner := openFixtureRunner(t, dir, r, a, e)
	if err := runner.Step(context.Background(), r); err == nil || e.effects != 0 {
		t.Fatal("lost execute response ran effect")
	}
	runner = openFixtureRunner(t, dir, r, a, e)
	if err := runner.Step(context.Background(), r); err != nil || e.effects != 0 || a.in.State != "failed" || a.in.Reason != "outcome_unknown" {
		t.Fatal("ambiguous execution was respawned", err)
	}
}
func TestLifecycleNamedAccountSchema2CompletesOnce(t *testing.T) {
	r, a := runtimeFixture()
	a.in.SchemaVersion = lifecycleintents.AccountChoiceSchemaV2
	a.in.Request.AccountKey = "coordinator"
	e := &fixtureExecutor{}
	runner := openFixtureRunner(t, t.TempDir(), r, a, e)
	if err := runner.Step(context.Background(), r); err != nil || e.effects != 1 || e.commits != 1 || a.in.State != "completed" || a.in.SchemaVersion != lifecycleintents.AccountChoiceSchemaV2 {
		t.Fatal("named-account schema2 did not complete", err)
	}
	if err := runner.Step(context.Background(), r); err != nil || e.effects != 1 {
		t.Fatal("named-account schema2 repeated", err)
	}
}
func TestLifecycleUnknownAndMalformedIntentSchemasRefuseEffect(t *testing.T) {
	for _, mutate := range []func(*lifecycleintents.Intent){
		func(in *lifecycleintents.Intent) { in.SchemaVersion = 3 },
		func(in *lifecycleintents.Intent) { in.SchemaVersion = lifecycleintents.AccountChoiceSchemaV2 },
		func(in *lifecycleintents.Intent) {
			in.SchemaVersion = lifecycleintents.RuntimeSchemaV1
			in.Request.AccountKey = "coordinator"
		},
	} {
		r, a := runtimeFixture()
		mutate(&a.in)
		e := &fixtureExecutor{}
		runner := openFixtureRunner(t, t.TempDir(), r, a, e)
		if !errors.Is(runner.Step(context.Background(), r), ErrOwnership) || e.effects != 0 {
			t.Fatal("unsupported intent schema executed")
		}
	}
}
func TestLifecycleExpiredAndChangedGenerationRefuseEffect(t *testing.T) {
	r, a := runtimeFixture()
	e := &fixtureExecutor{}
	runner := openFixtureRunner(t, t.TempDir(), r, a, e)
	r.ExpiresAt = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	if !errors.Is(runner.Step(context.Background(), r), ErrOwnership) || len(a.calls) != 0 {
		t.Fatal("expired runtime called authority")
	}
	r.ExpiresAt = time.Now().Add(time.Minute).Format(time.RFC3339Nano)
	r.Generation = uuid.NewString()
	if !errors.Is(runner.Step(context.Background(), r), ErrOwnership) || e.effects != 0 {
		t.Fatal("foreign generation executed")
	}
}

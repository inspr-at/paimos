// SPDX-License-Identifier: AGPL-3.0-only

package nativedispatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const testID = "11111111-1111-4111-8111-111111111111"
const workID = "22222222-2222-4222-8222-222222222222"
const parentID = "33333333-3333-4333-8333-333333333333"
const childID = "44444444-4444-4444-8444-444444444444"
const runID = "55555555-5555-4555-8555-555555555555"

func testBinding() nativecontract.Binding {
	run := runID
	return nativecontract.Binding{TenantID: testID, ProjectID: testID, OwnedSessionID: testID, HarnessSessionID: childID, RuntimeEpoch: testID, DaemonID: "owner-daemon", DaemonGeneration: "generation-1", BindingRevision: 2, RunID: &run}
}

func testResult(operation, request, state string) nativecontract.OperationResult {
	b := testBinding()
	return nativecontract.OperationResult{
		Receipt: nativecontract.Receipt{RequestID: request, OperationID: testID, Binding: &b, Operation: operation, State: state, SourceSequence: "1", UpdatedAt: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)},
		Session: &nativecontract.SessionView{OwnedSessionID: b.OwnedSessionID, CurrentBinding: &b, OwnerPersonID: testID, Role: "worker", Profile: "native", Harness: "codex", State: "running", Mode: "structured", Revision: 2, Generation: 1, ConversationRef: "conversation-1", Capabilities: nativecontract.Capabilities{Input: true, ChatSupported: true, Interrupt: true, InterruptSupported: true, Stop: true, Steer: "native", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}},
	}
}

func testRequest(t *testing.T, now time.Time) (Request, Target) {
	t.Helper()
	work, parent := workID, parentID
	cwd := t.TempDir()
	r := Request{TenantID: testID, ProjectID: testID, Start: nativecontract.LocalStart{Cwd: cwd, AccountKey: "owner-codex", Intent: nativecontract.StartIntent{RequestID: testID, ExpectedProjectRevision: 1, Role: "worker", Profile: "native", Harness: "codex", ComputerID: testID, AccountID: testID, WorkspaceID: "assigned-workspace", ModelProfileID: testID, WorkID: &work, ParentHarnessSessionID: &parent, PolicyID: testID}}}
	target := Target{Actor: nativecontract.Actor{TenantID: testID, PrincipalID: testID, PrincipalKind: "agent", OwnerPersonID: testID, GrantID: testID, GrantRevision: 1, KeyCeiling: []string{"harness.control", "run.create"}}, Policy: nativecontract.Policy{GrantID: testID, GrantRevision: 1, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64), PermissionPolicy: "bounded_preapproved", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 100000}, WorkID: work, ParentHarnessSessionID: parent, Worktree: cwd, Leaf: true}
	return r, target
}

type testAuthority struct{ target Target }

func (a *testAuthority) ResolveWorker(context.Context, Request) (Target, error) { return a.target, nil }

type testHost struct {
	mu                                                       sync.Mutex
	registered, reserved, spawned, inputs, interrupts, stops int
	preFork, allowFork                                       chan struct{}
	startErr                                                 error
	stopState                                                string
	startState                                               string
	wrongInput, wrongTurn                                    bool
	view                                                     *nativecontract.SessionView
	snapshots                                                int
}

func (h *testHost) Start(_ context.Context, in nativecontract.LocalStart) (nativecontract.OperationResult, error) {
	h.mu.Lock()
	h.registered++
	h.reserved++
	h.mu.Unlock()
	if h.preFork != nil {
		h.preFork <- struct{}{}
		<-h.allowFork
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.startErr != nil {
		return nativecontract.OperationResult{}, h.startErr
	}
	h.spawned++
	state := h.startState
	if state == "" {
		state = "applied"
	}
	return testResult("start", in.Intent.RequestID, state), nil
}
func (h *testHost) Input(_ context.Context, in nativecontract.Input) (nativecontract.OperationResult, error) {
	h.mu.Lock()
	h.inputs++
	h.mu.Unlock()
	out := testResult("input", in.RequestID, "turn_completed")
	out.Receipt.InputID, out.Receipt.TurnRef = &in.InputID, ptr("turn-1")
	if h.wrongInput {
		out.Receipt.InputID = ptr(parentID)
	}
	return out, nil
}
func (h *testHost) Interrupt(_ context.Context, in nativecontract.Interrupt) (nativecontract.OperationResult, error) {
	h.mu.Lock()
	h.interrupts++
	h.mu.Unlock()
	out := testResult("interrupt", in.RequestID, "applied")
	out.Receipt.TurnRef = &in.ExpectedTurnRef
	if h.wrongTurn {
		out.Receipt.TurnRef = ptr("another-turn")
	}
	return out, nil
}
func (h *testHost) Stop(_ context.Context, in nativecontract.Stop) (nativecontract.OperationResult, error) {
	h.mu.Lock()
	h.stops++
	state := h.stopState
	h.mu.Unlock()
	if state == "" {
		state = "accepted"
	}
	return testResult("stop", in.RequestID, state), nil
}

func (h *testHost) Snapshot(context.Context, nativecontract.Binding) (nativecontract.SessionView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.snapshots++
	if h.view != nil {
		return *h.view, nil
	}
	return *testResult("start", testID, "applied").Session, nil
}

type testAncestry struct {
	mu     sync.Mutex
	events []TaskEvent
}

func (a *testAncestry) UpdateExisting(_ context.Context, event TaskEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
	return nil
}
func ptr(s string) *string { return &s }

func requireReason(t *testing.T, err error, reason nativecontract.Reason) {
	t.Helper()
	var validation *nativecontract.ValidationError
	if !errors.As(err, &validation) || validation.Reason != reason {
		t.Fatalf("wanted %s, got %v", reason, err)
	}
}

// Risk: duplicate launch and native child-start can race, creating two children
// or freeing uncertain account/host capacity after a final rather than exit.
func TestDispatchDuplicateChildBarrierAndOccupancy(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, target := testRequest(t, now)
	a := &testAuthority{target}
	h := &testHost{preFork: make(chan struct{}, 2), allowFork: make(chan struct{})}
	ancestry := &testAncestry{}
	d, err := New(h, a, ancestry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan string, 2)
	d.onWait = func(kind string) { joined <- kind }
	type dispatchResult struct {
		worker *Worker
		err    error
	}
	results := make(chan dispatchResult, 2)
	go func() { w, err := d.Dispatch(ctx, r); results <- dispatchResult{w, err} }()
	select {
	case <-h.preFork:
	case <-ctx.Done():
		t.Fatal("first launch did not reach registration barrier")
	}
	h.mu.Lock()
	if h.registered != 1 || h.reserved != 1 || h.spawned != 0 {
		t.Fatal("child/reservation were not established before fork")
	}
	h.mu.Unlock()
	go func() { w, err := d.Dispatch(ctx, r); results <- dispatchResult{w, err} }()
	event := TaskEvent{Binding: testBinding(), ParentHarnessSessionID: parentID, TaskRef: "native-task-1", State: "started", Sequence: 1}
	events := make(chan error, 1)
	go func() { events <- d.ChildEvent(ctx, r.TenantID, r.Start.Intent.RequestID, event) }()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case kind := <-joined:
			seen[kind] = true
		case <-ctx.Done():
			t.Fatal("duplicate/event did not overlap pending launch")
		}
	}
	// Both paths are inside the pending operation before any native fork.
	select {
	case got := <-events:
		t.Fatalf("event escaped pre-fork barrier: %v", got)
	default:
	}
	close(h.allowFork)
	var w *Worker
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if w != nil && result.worker != w {
				t.Fatal("duplicate returned another child")
			}
			w = result.worker
		case <-ctx.Done():
			t.Fatal("dispatch hung")
		}
	}
	select {
	case err := <-events:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("event hung")
	}
	h.mu.Lock()
	if h.registered != 1 || h.reserved != 1 || h.spawned != 1 {
		t.Fatal("duplicate allocated a second child/process/reservation")
	}
	h.mu.Unlock()
	if len(ancestry.events) != 1 || ancestry.events[0].Binding.HarnessSessionID != childID || ancestry.events[0].ParentHarnessSessionID != parentID {
		t.Fatal("child ancestry changed")
	}
	// Replay updates only the existing row; wrong parents never reach the sink.
	d.onWait = nil // both barrier participants have returned
	if err := d.ChildEvent(ctx, r.TenantID, r.Start.Intent.RequestID, event); err != nil {
		t.Fatal(err)
	}
	wrong := event
	wrong.ParentHarnessSessionID = workID
	wrong.Sequence++
	requireReason(t, d.ChildEvent(ctx, r.TenantID, r.Start.Intent.RequestID, wrong), nativecontract.ReasonIdentityMismatch)
	if len(ancestry.events) != 1 {
		t.Fatal("replay or wrong parent updated ancestry")
	}
	// Snapshot pointers are copies; caller mutation cannot steer another child.
	copy := w.Result()
	copy.Session.CurrentBinding.RuntimeEpoch = parentID
	b := testBinding()
	in := nativecontract.Input{RequestID: workID, ExpectedBinding: b, InputID: workID, Mode: "now", Text: "approved inbox input", ExpiresAt: now.Add(time.Minute)}
	if _, err := w.Input(ctx, in); err != nil {
		t.Fatal(err)
	}
	if !w.Reservation().Held {
		t.Fatal("turn/final completion freed a running writer")
	}
	badInput := in
	h.wrongInput = true
	_, err = w.Input(ctx, in)
	requireReason(t, err, nativecontract.ReasonIdentityMismatch)
	h.wrongInput = false
	badInput.ExpectedBinding.RuntimeEpoch = parentID
	_, err = w.Input(ctx, badInput)
	requireReason(t, err, nativecontract.ReasonWrongGeneration)
	if _, err := w.Interrupt(ctx, nativecontract.Interrupt{RequestID: workID, ExpectedBinding: b, ExpectedTurnRef: "turn-1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	stop := nativecontract.Stop{RequestID: workID, ExpectedBinding: b, Kind: "graceful", ExpectedProcessIdentityDigest: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute)}
	h.wrongTurn = true
	_, err = w.Interrupt(ctx, nativecontract.Interrupt{RequestID: workID, ExpectedBinding: b, ExpectedTurnRef: "turn-1", ExpiresAt: now.Add(time.Minute)})
	requireReason(t, err, nativecontract.ReasonIdentityMismatch)
	h.wrongTurn = false
	view, err := w.Status(ctx, b)
	if err != nil || view.State != "running" {
		t.Fatal("owned status lost", view, err)
	}
	view.State = "idle"
	finalReceipt := testResult("input", workID, "turn_completed").Receipt
	view.LastReceipt = &finalReceipt
	h.view = &view
	if _, err = w.Status(ctx, b); err != nil || !w.Reservation().Held {
		t.Fatal("idle/final snapshot released occupancy", err)
	}
	_, err = w.Status(ctx, badInput.ExpectedBinding)
	requireReason(t, err, nativecontract.ReasonWrongGeneration)
	h.stopState = "uncertain"
	_, err = w.Stop(ctx, stop)
	requireReason(t, err, nativecontract.ReasonNativeFailure)
	reservation := w.Reservation()
	if !reservation.Held || !reservation.Uncertain || reservation.AccountID != r.Start.Intent.AccountID || reservation.ComputerID != r.Start.Intent.ComputerID || reservation.WorkID != workID {
		t.Fatal("uncertain exact occupancy lost", reservation)
	}
	requireReason(t, d.Forget(w), nativecontract.ReasonOwnershipUnconfirmed)
	h.stopState = "exit_confirmed"
	if _, err := w.Stop(ctx, stop); err != nil {
		t.Fatal(err)
	}
	if w.Reservation().Held || w.Reservation().Uncertain {
		t.Fatal("verified owned exit did not release monitoring hold")
	}
	if err := d.Forget(w); err != nil {
		t.Fatal(err)
	}
	// Parent tickets cannot even submit a native start.
	a.target.Leaf = false
	_, err = d.Dispatch(ctx, r)
	requireReason(t, err, nativecontract.ReasonInvalidInput)
	if h.registered != 1 || h.inputs != 2 || h.interrupts != 2 || h.stops != 2 || h.snapshots != 2 {
		t.Fatal("refused work reached a native writer")
	}
}

func TestDispatchChangedGrantAndUnconfirmedStart(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, target := testRequest(t, now)
	a := &testAuthority{target}
	h := &testHost{startErr: errors.New("transport lost after possible fork")}
	d, err := New(h, a, &testAncestry{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	w, err := d.Dispatch(t.Context(), r)
	if !errors.Is(err, h.startErr) || w == nil || !w.Reservation().Held || !w.Reservation().Uncertain {
		t.Fatal("ambiguous start lost its hold", err)
	}
	w2, err := d.Dispatch(t.Context(), r)
	if !errors.Is(err, h.startErr) || w2 != w || h.registered != 1 {
		t.Fatal("unconfirmed start was automatically retried", err)
	}
	a.target.Actor.GrantRevision++
	a.target.Policy.GrantRevision++
	_, err = d.Dispatch(t.Context(), r)
	requireReason(t, err, nativecontract.ReasonIdentityMismatch)
	a.target = target
	a.target.Policy.ExpiresAt = now
	_, err = d.Dispatch(t.Context(), r)
	requireReason(t, err, nativecontract.ReasonGrantExpired)
	if h.registered != 1 {
		t.Fatal("changed/expired grant launched")
	}
	// A rejection can arrive after possible fork. It does not prove no-fork.
	h2 := &testHost{startState: "rejected"}
	d2, err := New(h2, &testAuthority{target}, &testAncestry{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	w3, err := d2.Dispatch(t.Context(), r)
	requireReason(t, err, nativecontract.ReasonNativeFailure)
	if w3 == nil || !w3.Reservation().Held || h2.spawned != 1 {
		t.Fatal("rejection was misread as no-fork evidence")
	}
	requireReason(t, d2.Forget(w3), nativecontract.ReasonOwnershipUnconfirmed)
}

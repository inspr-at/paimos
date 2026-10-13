// SPDX-License-Identifier: AGPL-3.0-only

package nativedrain

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type drainClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []drainTimer
}

type drainTimer struct {
	at    time.Time
	ticks chan time.Time
}

func (c *drainClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *drainClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := make(chan time.Time, 1)
	c.timers = append(c.timers, drainTimer{at: c.now.Add(d), ticks: timer})
	return timer
}
func (c *drainClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	pending := c.timers[:0]
	for _, timer := range c.timers {
		if !c.now.Before(timer.at) {
			timer.ticks <- c.now
		} else {
			pending = append(pending, timer)
		}
	}
	c.timers = pending
	c.mu.Unlock()
}

type drainRuntime struct {
	mu                                                    sync.Mutex
	clock                                                 *drainClock
	bindings                                              []nativecontract.Binding
	writer                                                nativecontract.WriterRecord
	exit                                                  nativecontract.ExitEvidence
	background                                            <-chan struct{}
	closeEntered                                          chan struct{}
	closeRelease                                          <-chan struct{}
	checkpointErr, flushErr, confirmErr                   error
	truncated                                             bool
	order                                                 []string
	starts, lists, closes, confirms, checkpoints, flushes int
}

func (r *drainRuntime) List(_ context.Context, limit int) ([]nativecontract.Binding, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit != MaxSessions {
		panic("inventory work was not bounded")
	}
	r.lists++
	return append([]nativecontract.Binding(nil), r.bindings...), r.truncated, nil
}
func (r *drainRuntime) QuiesceAtBoundary(context.Context, nativecontract.Binding) (nativecontract.WriterRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.background != nil {
		select {
		case <-r.background:
		default:
			return nativecontract.WriterRecord{}, ErrBusy
		}
	}
	r.order = append(r.order, "quiesce")
	return r.writer, nil
}
func (r *drainRuntime) Close(context.Context, nativecontract.WriterRecord) (nativecontract.ExitEvidence, error) {
	r.mu.Lock()
	r.closes++
	r.order = append(r.order, "close")
	entered, release, exit := r.closeEntered, r.closeRelease, r.exit
	r.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	} // Deliberately ignores cancellation to test the outer bound.
	exit.ObservedAt = r.clock.Now()
	return exit, nil
}
func (r *drainRuntime) ConfirmExit(context.Context, nativecontract.WriterRecord, nativecontract.ExitEvidence) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirms++
	r.order = append(r.order, "confirm")
	if r.confirmErr == nil {
		r.bindings = nil
	} // Registry removal cannot discard a failed checkpoint.
	return r.confirmErr
}
func (r *drainRuntime) Checkpoint(context.Context, nativecontract.WriterRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoints++
	r.order = append(r.order, "checkpoint")
	return r.checkpointErr
}
func (r *drainRuntime) Flush(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
	r.order = append(r.order, "flush")
	return r.flushErr
}

func drainFixture(t *testing.T, approved bool) (*Coordinator, *drainRuntime, *drainClock) {
	t.Helper()
	clock := &drainClock{now: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)}
	const id = "00000000-0000-4000-8000-000000000001"
	binding := nativecontract.Binding{TenantID: id, ProjectID: id, OwnedSessionID: id,
		HarnessSessionID: id, RuntimeEpoch: id, DaemonID: "drain-test", DaemonGeneration: "generation-1", BindingRevision: 1}
	writer := nativecontract.WriterRecord{Binding: binding, PossibleFork: true,
		Identity: nativecontract.NativeIdentity{RuntimeEpoch: id, PID: 42, StartedAt: clock.Now().Add(-time.Hour), ProcessIdentityDigest: strings.Repeat("a", 64)}}
	runtime := &drainRuntime{clock: clock, bindings: []nativecontract.Binding{binding}, writer: writer,
		exit: nativecontract.ExitEvidence{ProcessIdentityDigest: writer.Identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: true}}
	coordinator, err := New(Config{Runtime: runtime, Clock: clock, CoordinatorApproved: approved})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, runtime, clock
}

func waitDrainBarrier(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("drain barrier did not complete")
	}
}

func deferredDrain(t *testing.T, c *Coordinator, want error, reason string) Status {
	t.Helper()
	status, err := c.Drain(t.Context())
	if !errors.Is(err, want) || status.State != "deferred" || status.Reason != reason || !status.AdmissionClosed {
		t.Fatalf("drain=%+v, error=%v; want deferred %s (%v)", status, err, reason, want)
	}
	return status
}

// Risk: an upgrade racing LEAD dispatch can miss a possible fork, close live
// background work, or report success on root exit without full writer proof.
// Barriers preserve the asserted writer; injected time never substitutes for exit.
func TestNativeDrainRetainsBackgroundAndUnconfirmedWriter(t *testing.T) {
	c, runtime, clock := drainFixture(t, true)
	backgroundDone, admissionEntered, publish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	runtime.background, runtime.bindings = backgroundDone, nil
	admitted := make(chan error, 1)
	go func() {
		admitted <- c.WithAdmission(t.Context(), func(context.Context) error {
			close(admissionEntered)
			<-publish
			runtime.mu.Lock()
			runtime.starts++
			runtime.bindings = []nativecontract.Binding{runtime.writer.Binding}
			runtime.mu.Unlock()
			return nil
		})
	}()
	waitDrainBarrier(t, admissionEntered)
	if _, err := c.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := c.WithAdmission(t.Context(), func(context.Context) error { t.Fatal("new launch during drain"); return nil }); !errors.Is(err, ErrDraining) {
		t.Fatalf("new launch refusal=%v", err)
	}
	status := deferredDrain(t, c, ErrLaunchPending, "admission_pending")
	if status.PendingLaunches != 1 || runtime.lists != 0 || runtime.closes != 0 || runtime.flushes != 1 {
		t.Fatal("drain missed in-flight publication or omitted its flush", status)
	}
	close(publish)
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not settle")
	}
	status = deferredDrain(t, c, ErrBusy, "active_or_background_work")
	if len(status.Sessions) != 1 || status.Sessions[0].State != "retained" || runtime.starts != 1 || runtime.closes != 0 || runtime.flushes != 2 {
		t.Fatal("background writer was lost/closed or its delivery flush was omitted", status)
	}
	clock.advance(time.Second)
	close(backgroundDone)
	runtime.exit.GroupAbsent = false
	status = deferredDrain(t, c, ErrOwnershipUnconfirmed, "ownership_unconfirmed")
	if status.Sessions[0].State != "unconfirmed" || runtime.confirms != 0 || runtime.checkpoints != 0 {
		t.Fatal("root exit was mistaken for full writer exit", status)
	}
	runtime.exit.GroupAbsent = true
	runtime.checkpointErr = errors.New("synthetic disk failure")
	deferredDrain(t, c, ErrCheckpointUnconfirmed, "checkpoint_unconfirmed")
	if runtime.closes != 2 || runtime.confirms != 1 || runtime.checkpoints != 1 || len(runtime.bindings) != 0 {
		t.Fatal("checkpoint did not follow confirmed exit")
	}
	runtime.checkpointErr, runtime.flushErr = nil, errors.New("synthetic API outage")
	status = deferredDrain(t, c, ErrFlushUnconfirmed, "flush_unconfirmed")
	if status.FlushConfirmed || status.Sessions[0].State != "checkpointed" || runtime.closes != 2 || runtime.confirms != 1 || runtime.checkpoints != 2 {
		t.Fatal("retry lost its checkpoint or closed an exited writer again", status)
	}
	runtime.flushErr = nil
	status, err := c.Drain(t.Context())
	if err != nil || status.State != "ready" || !status.AdmissionClosed || !status.FlushConfirmed || runtime.starts != 1 || runtime.closes != 2 || runtime.checkpoints != 2 {
		t.Fatal("confirmed drain was not ready or repeated effects", status, err)
	}
	if got := strings.Join(runtime.order, ","); got != "flush,flush,quiesce,close,flush,quiesce,close,confirm,checkpoint,flush,checkpoint,flush,flush" {
		t.Fatalf("exit/checkpoint/flush order=%s", got)
	}
	if err := c.WithAdmission(t.Context(), func(context.Context) error { t.Fatal("ready drain reopened admission"); return nil }); !errors.Is(err, ErrDraining) {
		t.Fatal(err)
	}
}

// Risk: a callback that ignores cancellation must not hang the owner, overlap a
// retry, or publish late readiness after the injected drain deadline expires.
func TestNativeDrainDeadlineKeepsUnconfirmedAttemptFenced(t *testing.T) {
	c, runtime, clock := drainFixture(t, true)
	entered, release := make(chan struct{}), make(chan struct{})
	runtime.closeEntered, runtime.closeRelease = entered, release
	result := make(chan error, 1)
	go func() { _, err := c.Drain(t.Context()); result <- err }()
	waitDrainBarrier(t, entered)
	if status := c.Status(); status.State != "draining" || status.Sessions[0].State != "closing" {
		t.Fatal(status)
	}
	c.mu.Lock()
	finished := c.attemptDone
	c.mu.Unlock()
	clock.advance(MaxDuration)
	select {
	case err := <-result:
		if !errors.Is(err, ErrDeadline) {
			t.Fatalf("deadline error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("uncooperative close blocked the owner")
	}
	if status, err := c.Drain(t.Context()); !errors.Is(err, ErrDraining) || status.State != "deferred" || status.Reason != "deadline_expired" {
		t.Fatal("retry overlapped an unconfirmed close", status, err)
	}
	close(release)
	waitDrainBarrier(t, finished)
	status := c.Status()
	if status.State != "deferred" || status.Reason != "deadline_expired" || !status.AdmissionClosed || runtime.closes != 1 || runtime.confirms != 0 || runtime.checkpoints != 0 || runtime.flushes != 0 {
		t.Fatal("late callback claimed readiness or continued past deadline", status)
	}
}

func TestNativeDrainRejectsIncompleteInventoryAndMismatchedProof(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		alter  func(*drainRuntime)
		want   error
		reason string
	}{
		{"truncated inventory", func(r *drainRuntime) { r.truncated = true }, ErrInventoryUnconfirmed, "inventory_unconfirmed"},
		{"duplicate inventory", func(r *drainRuntime) { r.bindings = append(r.bindings, r.bindings[0]) }, ErrInventoryUnconfirmed, "inventory_unconfirmed"},
		{"oversized inventory", func(r *drainRuntime) { r.bindings = make([]nativecontract.Binding, MaxSessions+1) }, ErrInventoryUnconfirmed, "inventory_unconfirmed"},
		{"invalid binding", func(r *drainRuntime) { r.bindings[0].DaemonGeneration = strings.Repeat("x", 129) }, ErrIdentityMismatch, "identity_mismatch"},
		{"wrong writer epoch", func(r *drainRuntime) { r.writer.Identity.RuntimeEpoch = "00000000-0000-4000-8000-000000000002" }, ErrIdentityMismatch, "identity_mismatch"},
		{"wrong exit digest", func(r *drainRuntime) { r.exit.ProcessIdentityDigest = strings.Repeat("b", 64) }, ErrIdentityMismatch, "identity_mismatch"},
		{"unobserved root exit", func(r *drainRuntime) { r.exit.RootExited = false }, ErrOwnershipUnconfirmed, "ownership_unconfirmed"},
		{"confirmation failure", func(r *drainRuntime) { r.confirmErr = errors.New("synthetic ledger failure") }, ErrOwnershipUnconfirmed, "ownership_unconfirmed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c, runtime, _ := drainFixture(t, true)
			scenario.alter(runtime)
			deferredDrain(t, c, scenario.want, scenario.reason)
			if runtime.checkpoints != 0 || runtime.flushes != 1 {
				t.Fatal("unsafe proof reached checkpoint or suppressed bounded delivery flush")
			}
		})
	}
}

func TestNativeDrainRequiresCoordinatorPilotApproval(t *testing.T) {
	c, runtime, _ := drainFixture(t, false)
	status, err := c.Drain(t.Context())
	if !errors.Is(err, ErrPilotFrozen) || status.State != "deferred" || status.Reason != "pilot_frozen" || status.AdmissionClosed || runtime.lists != 0 || runtime.closes != 0 || runtime.flushes != 0 {
		t.Fatal("drain changed a frozen live pilot", status, err)
	}
	if err := c.WithAdmission(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatal("pilot freeze disrupted ordinary approved operation", err)
	}
}

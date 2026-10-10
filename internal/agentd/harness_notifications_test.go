// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
)

type hintAPI struct {
	*fakeAPI
	connected  chan func()
	deliveries chan struct{}
	controls   chan string
	queued     []HarnessDelivery
}

func (a *hintAPI) WatchHarness(ctx context.Context, _ HarnessSession, wake func()) error {
	select {
	case a.connected <- wake:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}
func (a *hintAPI) CompleteHarnessDelivery(ctx context.Context, session HarnessSession, delivery HarnessDelivery) error {
	err := a.fakeAPI.CompleteHarnessDelivery(ctx, session, delivery)
	a.mu.Lock()
	a.queued = a.queued[1:]
	a.mu.Unlock()
	a.deliveries <- struct{}{}
	return err
}
func (a *hintAPI) DrainHarness(context.Context, HarnessSession) ([]HarnessDelivery, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queued) == 0 {
		return nil, nil
	}
	return append([]HarnessDelivery(nil), a.queued[:1]...), nil
}
func (a *hintAPI) CompleteHarnessControl(ctx context.Context, session HarnessSession, id, outcome, reason string) error {
	err := a.fakeAPI.CompleteHarnessControl(ctx, session, id, outcome, reason)
	a.controls <- id + ":" + outcome
	return err
}

func TestHarnessHintsDrainAndStopWithoutHeartbeatOrPoll(t *testing.T) {
	// Risk: a pushed hint is ignored until a timer, or becomes an unauthorized
	// process command. No timer can fire in this loop; only queue reads act.
	s, api, process := testSupervisor(t)
	hints := &hintAPI{fakeAPI: api, connected: make(chan func(), 1), deliveries: make(chan struct{}, 2), controls: make(chan string, 2)}
	s.api = hints
	entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", Generation: s.generation, State: "running", PID: process.PID(), Controls: map[string]replay{}}, harness: HarnessSession{ID: "session", ProjectID: "project"}, process: process, inboxCapable: true, replies: map[string]InboxReplyTarget{}, monitorDone: make(chan struct{}), harnessWake: make(chan struct{}, 1)}
	s.runs["run"] = entry
	ended := make(chan struct{})
	go func() { defer close(ended); s.heartbeatLoop(entry, nil, nil) }()
	go s.watchHarness(entry)
	t.Cleanup(func() { close(entry.monitorDone); <-ended })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var wake func()
	select {
	case wake = <-hints.connected:
	case <-ctx.Done():
		t.Fatal("notification watcher never connected")
	}
	api.mu.Lock()
	hints.queued = []HarnessDelivery{{ID: "delivery", MessageID: "message", SenderPrincipalID: "person", Body: "check the current work"}, {ID: "next-delivery", MessageID: "next-message", SenderPrincipalID: "person", Body: "also check the final handover"}}
	api.mu.Unlock()
	wake()
	for range 2 {
		select {
		case <-hints.deliveries:
		case <-ctx.Done():
			t.Fatal("one hint did not drain the full queued burst")
		}
	}
	process.mu.Lock()
	calls := process.calls
	process.mu.Unlock()
	if calls != 2 {
		t.Fatalf("message inserted %d times", calls)
	}
	api.mu.Lock()
	beats := api.harnessBeats
	api.harnessControls = []HarnessControl{{ID: "stop", Kind: "stop"}}
	api.mu.Unlock()
	if beats != 0 {
		t.Fatal("hint emitted a lease heartbeat")
	}
	wake()
	select {
	case result := <-hints.controls:
		if result != "stop:applied" {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("hint did not apply queued Stop")
	}
	select {
	case <-process.stopped:
	default:
		t.Fatal("Stop completion preceded process stop")
	}
}

func TestRemoteHarnessNotificationsBoundFramesAndCredentials(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/projects/project/harness-sessions/session/notifications" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-Aeon-Worker-Lease") != "fixture-lease" {
					t.Error("notification request lost its exact binding")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if oversized {
					_, _ = fmt.Fprint(w, "event: harness.wake\ndata: "+strings.Repeat("x", 17<<10)+"\n\n")
					return
				}
				_, _ = fmt.Fprint(w, "event: stream.ping\ndata: {}\n\nevent: harness.wake\ndata: {}\n\nevent: other\ndata: {}\n\n")
			}))
			defer server.Close()
			remote := NewRemote(server.URL, "fixture-token")
			wakes := 0
			err := remote.WatchHarness(t.Context(), HarnessSession{ID: "session", ProjectID: "project", Lease: "fixture-lease"}, func() { wakes++ })
			if oversized {
				if err == nil || err == io.EOF || wakes != 0 {
					t.Fatal("oversized frame produced a wake")
				}
				return
			}
			if err != io.EOF || wakes != 1 {
				t.Fatalf("wake parsing: count=%d error=%v", wakes, err)
			}
		})
	}
	t.Run("retains healthy connection until parent cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		remote := NewRemote("http://fixture.invalid", "fixture-token")
		remote.Client.HTTP.Transport = harnessRoundTrip(func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			if !ok || deadline.Before(time.Now().Add(db.ListenerMaxLifetime)) {
				t.Error("client would recycle a healthy stream before its server")
			}
			reader, writer := io.Pipe()
			go func() {
				_, _ = io.WriteString(writer, "event: harness.wake\ndata: {}\n\n")
				<-req.Context().Done()
				_ = writer.CloseWithError(req.Context().Err())
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
		})
		wakes := 0
		err := remote.WatchHarness(ctx, HarnessSession{}, func() { wakes++; cancel() })
		if !errors.Is(err, context.Canceled) || wakes != 1 {
			t.Fatalf("stream did not obey cancellation: wakes=%d error=%v", wakes, err)
		}
	})
}

type harnessRoundTrip func(*http.Request) (*http.Response, error)

func (f harnessRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type scriptedHarnessTimer struct {
	ticks  chan time.Time
	resets chan time.Duration
}

func (t *scriptedHarnessTimer) Ticks() <-chan time.Time { return t.ticks }
func (t *scriptedHarnessTimer) Reset(d time.Duration)   { t.resets <- d }
func (t *scriptedHarnessTimer) Stop()                   {}

type safetyHarnessAPI struct {
	*fakeAPI
	yields, drains int
	lostCompletion bool
	lastDelivery   HarnessDelivery
	pause          *HarnessPause
}

func (a *safetyHarnessAPI) YieldHarness(ctx context.Context, session HarnessSession) ([]HarnessControl, error) {
	a.mu.Lock()
	a.yields++
	a.mu.Unlock()
	return a.fakeAPI.YieldHarness(ctx, session)
}
func (a *safetyHarnessAPI) DrainHarness(ctx context.Context, session HarnessSession) ([]HarnessDelivery, error) {
	a.mu.Lock()
	a.drains++
	a.mu.Unlock()
	return a.fakeAPI.DrainHarness(ctx, session)
}
func (a *safetyHarnessAPI) CompleteHarnessDelivery(ctx context.Context, session HarnessSession, item HarnessDelivery) error {
	a.mu.Lock()
	a.lastDelivery = item
	lost := a.lostCompletion
	a.lostCompletion = false
	a.mu.Unlock()
	if lost {
		return errors.New("fixture lost completion")
	}
	return a.fakeAPI.CompleteHarnessDelivery(ctx, session, item)
}
func (a *safetyHarnessAPI) HeartbeatHarnessPause(ctx context.Context, session HarnessSession, phase string) (*HarnessPause, error) {
	err := a.fakeAPI.HeartbeatHarness(ctx, session, phase)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pause, err
}

type safetyHarnessFixture struct {
	t     *testing.T
	s     *Supervisor
	a     *safetyHarnessAPI
	e     *owned
	p     *fakeProcess
	poll  *scriptedHarnessTimer
	wake  *scriptedHarnessTimer
	beats chan time.Time
	clock atomic.Int64
	guard context.Context
}

func newSafetyHarnessFixture(t *testing.T, connected bool) *safetyHarnessFixture {
	t.Helper()
	s, api, p := testSupervisor(t)
	f := &safetyHarnessFixture{t: t, s: s, a: &safetyHarnessAPI{fakeAPI: api}, p: p, beats: make(chan time.Time)}
	f.clock.Store(time.Now().UnixNano())
	guard, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	f.guard = guard
	t.Cleanup(cancel)
	f.e = &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", Generation: s.generation, State: "running", PID: p.PID(), Controls: map[string]replay{}}, harness: HarnessSession{ID: "session", ProjectID: "project", Lease: "fixture-lease"}, process: p, inboxCapable: true, replies: map[string]InboxReplyTarget{}, monitorDone: make(chan struct{}), harnessWake: make(chan struct{}, 1), harnessConnected: connected}
	s.runs["run"], s.api = f.e, f.a
	newTimer := func() *scriptedHarnessTimer {
		return &scriptedHarnessTimer{ticks: make(chan time.Time), resets: make(chan time.Duration, 128)}
	}
	f.poll, f.wake = newTimer(), newTimer()
	timers := []harnessTimer{f.poll, f.wake}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		s.heartbeatLoopScheduled(f.e, f.beats, nil, f.now, func() harnessTimer {
			timer := timers[0]
			timers = timers[1:]
			return timer
		})
	}()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		close(f.e.monitorDone)
		select {
		case <-ended:
		case <-cleanup.Done():
			t.Error("heartbeat loop did not stop")
		}
	})
	return f
}
func (f *safetyHarnessFixture) now() time.Time { return time.Unix(0, f.clock.Load()) }
func (f *safetyHarnessFixture) pollReset() time.Duration {
	f.t.Helper()
	select {
	case delay := <-f.poll.resets:
		return delay // barrier: the preceding service cycle has finished
	case <-f.guard.Done():
		f.t.Fatal("poll scheduler stalled")
		return 0
	}
}
func (f *safetyHarnessFixture) tick(channel chan time.Time, advance time.Duration) time.Duration {
	f.t.Helper()
	f.clock.Add(int64(advance))
	select {
	case channel <- f.now():
	case <-f.guard.Done():
		f.t.Fatal("scheduler did not accept injected tick")
	}
	return f.pollReset()
}

type scriptedHarnessWatcher func(context.Context, HarnessSession, func()) error

func (f scriptedHarnessWatcher) WatchHarness(ctx context.Context, session HarnessSession, wake func()) error {
	return f(ctx, session, wake)
}

// Risks: reducing empty calls delays controls/input, postpones durable deadlines,
// retries an uncertain insertion, or treats a reconnect hint as process authority.
// Injected clocks, scheduler barriers and call counters establish every boundary.
func TestHarnessSafetyPollingRecoveryAndOwnership(t *testing.T) {
	t.Run("empty call budget with independent heartbeats", func(t *testing.T) {
		counts := make([]int, 2)
		for mode, connected := range []bool{false, true} {
			t.Run(fmt.Sprint(connected), func(t *testing.T) {
				f := newSafetyHarnessFixture(t, connected)
				due := f.now().Add(f.pollReset())
				for second := 1; second <= 60; second++ {
					f.clock.Add(int64(time.Second))
					if !f.now().Before(due) {
						due = f.now().Add(f.tick(f.poll.ticks, 0))
					}
					if second%15 == 0 {
						due = f.now().Add(f.tick(f.beats, 0))
					}
				}
				f.a.mu.Lock()
				defer f.a.mu.Unlock()
				counts[mode] = f.a.yields + f.a.drains
				if f.a.harnessBeats != 4 {
					t.Fatalf("lease heartbeats changed: %d", f.a.harnessBeats)
				}
			})
		}
		if counts[0] != 68 || counts[1] != 4 || counts[1]*10 > counts[0] {
			t.Fatalf("empty calls/minute: fallback=%d healthy=%d", counts[0], counts[1])
		}
		t.Logf("empty yield/drain calls per injected minute: 68 -> 4 (94.1%% reduction); four lease heartbeats retained")
	})
	t.Run("missed input and stop notification", func(t *testing.T) {
		f := newSafetyHarnessFixture(t, true)
		if f.pollReset() != 30*time.Second {
			t.Fatal("healthy fallback is not bounded at 30 seconds")
		}
		f.a.mu.Lock()
		f.a.harnessDeliveries = []HarnessDelivery{{ID: "missed", Body: "missed input"}}
		f.a.mu.Unlock()
		f.tick(f.poll.ticks, 30*time.Second)
		f.pollReset() // the delivered item immediately wakes the next FIFO drain
		f.a.mu.Lock()
		f.a.harnessControls = []HarnessControl{{ID: "missed-stop", Kind: "stop"}}
		f.a.mu.Unlock()
		f.tick(f.poll.ticks, 30*time.Second)
		select {
		case <-f.p.stopped:
		default:
			t.Fatal("safety poll missed Stop")
		}
		f.p.mu.Lock()
		defer f.p.mu.Unlock()
		if f.p.calls != 1 {
			t.Fatal("safety poll missed input")
		}
	})
	t.Run("pause heartbeat and deadline independent of safety poll", func(t *testing.T) {
		f := newSafetyHarnessFixture(t, true)
		f.pollReset()
		f.a.mu.Lock()
		f.a.pause = &HarnessPause{ControlID: "12345678-1234-4234-9234-123456789012", State: "requested", DeadlineAt: time.Now().Add(time.Minute)}
		f.a.mu.Unlock()
		f.tick(f.beats, 15*time.Second)
		f.p.mu.Lock()
		pauseDelivered := f.p.calls == 1 && strings.Contains(f.p.texts[0], "Pause requested")
		f.p.mu.Unlock()
		if !pauseDelivered {
			t.Fatal("missed notification delayed cooperative pause")
		}
		f.e.mu.Lock()
		f.e.pauseWakeAt = f.now().Add(time.Second)
		f.e.mu.Unlock()
		f.e.wakeHarness()
		f.pollReset()
		select {
		case delay := <-f.wake.resets:
			if delay != time.Second {
				t.Fatal("deadline inherited safety interval", delay)
			}
		case <-f.guard.Done():
			t.Fatal("deadline timer not armed")
		}
		f.tick(f.wake.ticks, time.Second)
		f.a.mu.Lock()
		defer f.a.mu.Unlock()
		if f.a.harnessBeats != 2 {
			t.Fatal("deadline failed to wake heartbeat independently")
		}
	})
	t.Run("uncertain completion retries quickly without reinsertion", func(t *testing.T) {
		f := newSafetyHarnessFixture(t, true)
		f.pollReset()
		f.a.mu.Lock()
		f.a.harnessDeliveries = []HarnessDelivery{{ID: "uncertain", Body: "one insertion"}}
		f.a.lostCompletion = true
		f.a.mu.Unlock()
		f.e.wakeHarness()
		if f.pollReset() != 2*time.Second {
			t.Fatal("uncertain settlement entered slow polling")
		}
		f.tick(f.poll.ticks, 2*time.Second)
		f.p.mu.Lock()
		defer f.p.mu.Unlock()
		f.a.mu.Lock()
		defer f.a.mu.Unlock()
		if f.p.calls != 1 || f.a.harnessDeliveryCompletions != 1 {
			t.Fatal("uncertain completion repeated insertion or failed to settle")
		}
	})
	t.Run("generation revocation rejects hinted input", func(t *testing.T) {
		f := newSafetyHarnessFixture(t, true)
		f.pollReset()
		f.e.mu.Lock()
		f.e.record.Generation = "revoked-generation"
		f.e.mu.Unlock()
		f.a.mu.Lock()
		f.a.harnessDeliveries = []HarnessDelivery{{ID: "revoked", Body: "do not deliver"}}
		f.a.mu.Unlock()
		f.e.wakeHarness()
		f.pollReset()
		f.p.mu.Lock()
		defer f.p.mu.Unlock()
		f.a.mu.Lock()
		defer f.a.mu.Unlock()
		if f.p.calls != 0 || f.a.lastDelivery.Outcome != "failed" {
			t.Fatal("hint bypassed generation fence or reported success")
		}
	})
	t.Run("disconnect backoff and reconnect catch-up", func(t *testing.T) {
		f := newSafetyHarnessFixture(t, true)
		f.pollReset()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls, delays, retry := make(chan func()), make(chan time.Duration), make(chan struct{})
		disconnect := make(chan struct{})
		ended := make(chan struct{})
		watcher := scriptedHarnessWatcher(func(ctx context.Context, session HarnessSession, wake func()) error {
			if session.ID != "session" || session.Lease != "fixture-lease" {
				t.Error("reconnect changed generation proof")
			}
			select {
			case calls <- wake:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-disconnect:
				return io.EOF
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		go func() {
			defer close(ended)
			f.s.watchHarnessLoop(ctx, f.e, f.e.harness, watcher, f.now, func(ctx context.Context, delay time.Duration) bool {
				select {
				case delays <- delay:
				case <-ctx.Done():
					return false
				}
				select {
				case <-retry:
					return true
				case <-ctx.Done():
					return false
				}
			}, func(d time.Duration) time.Duration { return d })
		}()
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			cancel()
			select {
			case <-ended:
			case <-cleanup.Done():
				t.Error("watcher ignored cancellation")
			}
		})
		for _, expected := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
			select {
			case <-calls:
			case <-f.guard.Done():
				t.Fatal("watcher did not reconnect")
			}
			select {
			case disconnect <- struct{}{}:
			case <-f.guard.Done():
				t.Fatal("stream did not close")
			}
			select {
			case delay := <-delays:
				if delay != expected {
					t.Fatalf("reconnect backoff=%s want=%s", delay, expected)
				}
			case <-f.guard.Done():
				t.Fatal("missing reconnect backoff")
			}
			if f.pollReset() != 2*time.Second {
				t.Fatal("disconnect did not restore fast fallback")
			}
			select {
			case retry <- struct{}{}:
			case <-f.guard.Done():
				t.Fatal("reconnect stalled")
			}
		}
		var wake func()
		select {
		case wake = <-calls:
		case <-f.guard.Done():
			t.Fatal("missing final reconnect")
		}
		f.a.mu.Lock()
		f.a.harnessDeliveries = []HarnessDelivery{{ID: "during-outage", Body: "reconnect input"}}
		f.a.mu.Unlock()
		wake()
		if f.pollReset() != 30*time.Second {
			t.Fatal("authenticated reconnect did not restore healthy polling")
		}
		f.pollReset() // drain the rest of the FIFO burst without waiting for a poll
		f.p.mu.Lock()
		insertions := f.p.calls
		f.p.mu.Unlock()
		if insertions != 1 {
			t.Fatal("reconnect initial wake failed to catch up missed input")
		}
		f.clock.Add(int64(harnessStreamIdle))
		select {
		case disconnect <- struct{}{}:
		case <-f.guard.Done():
			t.Fatal("healthy stream did not close")
		}
		select {
		case delay := <-delays:
			if delay != time.Second {
				t.Fatal("stable stream failed to reset reconnect backoff")
			}
		case <-f.guard.Done():
			t.Fatal("missing final disconnect")
		}
		f.pollReset()
		wake() // stale callback from the closed connection must be ignored
		f.e.mu.Lock()
		stillConnected := f.e.harnessConnected
		f.e.mu.Unlock()
		if stillConnected {
			t.Fatal("closed stream restored healthy polling")
		}
		for _, backoff := range []time.Duration{time.Second, 30 * time.Second} {
			for range 20 {
				delay := jitterHarnessReconnect(backoff)
				if delay < backoff/2 || delay > backoff {
					t.Fatal("reconnect jitter exceeded its bounds")
				}
			}
		}
	})
}

type levelHintAPI struct {
	*hintAPI
	levels []string
}

func (a *levelHintAPI) DrainHarnessInput(_ context.Context, _ HarnessSession, level string) ([]HarnessDelivery, error) {
	a.levels = append(a.levels, level)
	for _, item := range a.queued {
		if level == "" || item.Level == level {
			return []HarnessDelivery{item}, nil
		}
	}
	return nil, nil
}
func (a *levelHintAPI) CompleteHarnessDelivery(ctx context.Context, session HarnessSession, item HarnessDelivery) error {
	for i, queued := range a.queued {
		if queued.ID == item.ID {
			a.queued = append(a.queued[:i], a.queued[i+1:]...)
			break
		}
	}
	return a.fakeAPI.CompleteHarnessDelivery(ctx, session, item)
}

// Risk: after-turn input is injected while busy, or blocks a later steer and
// controls. Explicit adapter activity, with no timers, establishes the boundary.
func TestAfterTurnInputWaitsWhileSteerAndControlsStayAvailable(t *testing.T) {
	s, api, process := testSupervisor(t)
	entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", Generation: s.generation, State: "running", PID: process.PID(), Controls: map[string]replay{}}, harness: HarnessSession{ID: "session", ProjectID: "project", Activity: "busy"}, process: process, inboxCapable: true, replies: map[string]InboxReplyTarget{}, harnessWake: make(chan struct{}, 1)}
	s.runs["run"] = entry
	hints := &levelHintAPI{hintAPI: &hintAPI{fakeAPI: api, controls: make(chan string, 1), queued: []HarnessDelivery{{ID: "later", Level: "simple", Body: "after turn"}, {ID: "now", Level: "steer", Body: "steer now"}}}}
	s.api = hints
	if err := s.serviceHarnessCycle(t.Context(), entry, false); err != nil {
		t.Fatal(err)
	}
	if len(hints.queued) != 1 || hints.queued[0].ID != "later" {
		t.Fatalf("busy drain took after-turn input: %+v", hints.queued)
	}
	process.mu.Lock()
	calls := process.calls
	process.mu.Unlock()
	if calls != 1 {
		t.Fatalf("busy injection count %d", calls)
	}
	api.harnessControls = []HarnessControl{{ID: "interrupt", Kind: "interrupt"}}
	if err := s.serviceHarnessWake(t.Context(), entry, false, true); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-hints.controls:
		if result != "interrupt:applied" {
			t.Fatal(result)
		}
	default:
		t.Fatal("waiting after-turn input blocked interrupt")
	}
	if len(hints.queued) != 1 || hints.queued[0].ID != "later" {
		t.Fatal("interrupt released input before idle")
	}
	s.observe(entry, AdapterEvent{Activity: "idle"})
	select {
	case <-entry.harnessWake:
	default:
		t.Fatal("idle did not wake queued input")
	}
	if err := s.serviceHarnessCycle(t.Context(), entry, false); err != nil {
		t.Fatal(err)
	}
	process.mu.Lock()
	calls = process.calls
	process.mu.Unlock()
	if calls != 3 || len(hints.queued) != 0 || len(hints.levels) != 3 || hints.levels[0] != "steer" || hints.levels[1] != "steer" || hints.levels[2] != "" {
		t.Fatalf("after-turn input not released once: calls=%d levels=%v", calls, hints.levels)
	}
}

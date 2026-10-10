// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

type routineRoundTrip func(*http.Request) (*http.Response, error)

func (f routineRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRoutineDispatcherUsesEncryptedTargetAndFencedCompletion(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	const senderKey = "fixture-routine-sender-key"
	const targetRef = "https://8.8.8.8/fixture-hook"
	_, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: targetRef, Secret: senderKey, Role: "primary", MaximumLevel: "simple"})
	if err != nil {
		t.Fatal(err)
	}
	in := compatInput("grok_bot:worker", "routine-once")
	in.Level = "steer"
	message := mustCompatSend(t, m, w.sender, project, in)
	called := 0
	dispatcher := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(req *http.Request) (*http.Response, error) {
		called++
		if req.URL.String() != targetRef || req.Header.Get("Authorization") != "Bearer "+senderKey || req.Header.Get("Idempotency-Key") == "" {
			t.Fatal("routine request lost private binding")
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var wake routineWake
		if err := json.Unmarshal(body, &wake); err != nil {
			t.Fatal(err)
		}
		if wake.Event != "agent_message.available" || wake.MessageID != message.ID || wake.EffectiveLevel != "simple" || wake.FallbackReason != "unsupported" || !strings.Contains(wake.Content, "SECURITY NOTICE") || !strings.Contains(wake.Content, in.Body) || strings.Contains(string(body), senderKey) || strings.Contains(string(body), targetRef) {
			t.Fatal("routine wake content or redaction mismatch")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	worked, err := dispatcher.DispatchOne(t.Context(), w.agent.TenantID)
	if err != nil || !worked || called != 1 {
		t.Fatalf("dispatch worked=%t calls=%d err=%v", worked, called, err)
	}
	worked, err = dispatcher.DispatchOne(t.Context(), w.agent.TenantID)
	if err != nil || worked || called != 1 {
		t.Fatalf("duplicate dispatch worked=%t calls=%d err=%v", worked, called, err)
	}
	var state, effective, reason string
	err = db.InTenant(dbtest.Seed(context.Background()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state,effective_level,fallback_reason FROM inbox_message_deliveries WHERE message_id=$1::uuid`, message.ID).Scan(&state, &effective, &reason)
	})
	if err != nil || state != "delivered" || effective != "simple" || reason != "unsupported" {
		t.Fatalf("delivery state=%s level=%s reason=%s err=%v", state, effective, reason, err)
	}
}

type routineFixtureListener struct {
	next   func(context.Context) (*pgconn.Notification, error)
	closed int
}

func (l *routineFixtureListener) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	return l.next(ctx)
}

func (l *routineFixtureListener) Close(context.Context) error { l.closed++; return nil }

// Risk: an empty or disconnected queue must not keep querying at 250 ms;
// injected timer firings measure calls without elapsed-time assertions.
func TestRoutineIdleFallbackAndReconnectScheduling(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		t.Run(map[bool]string{false: "connected", true: "disconnected"}[disconnected], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			listener := &routineFixtureListener{}
			checks, ticks, connects := 0, 0, 0
			var elapsed time.Duration
			err := runRoutine(ctx, "tenant", func(context.Context, string) (bool, time.Duration, error) {
				checks++
				return false, routineFallback, nil
			}, func(context.Context) (routineListener, error) {
				connects++
				if disconnected {
					return nil, errors.New("offline")
				}
				return listener, nil
			}, func(_ context.Context, got routineListener, tenant string, delay time.Duration) error {
				if tenant != "tenant" || delay != 5*time.Second || (got == nil) != disconnected {
					t.Fatalf("wait tenant=%s delay=%s connected=%t", tenant, delay, got != nil)
				}
				ticks++
				elapsed += delay
				if elapsed == time.Minute {
					cancel() // The next scan belongs to the next minute.
				}
				return context.DeadlineExceeded
			})
			if err != nil || checks != 12 || ticks != 12 || (!disconnected && (connects != 1 || listener.closed != 1)) {
				t.Fatalf("checks=%d ticks=%d connects=%d closed=%d err=%v", checks, ticks, connects, listener.closed, err)
			}
			t.Logf("empty checks per virtual minute: before=%d after=%d", time.Minute/(250*time.Millisecond), checks)
		})
	}

	t.Run("disconnect scans then waits before reconnect", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		listener := &routineFixtureListener{}
		var order []string
		waits := 0
		_ = runRoutine(ctx, "tenant", func(context.Context, string) (bool, time.Duration, error) {
			order = append(order, "scan")
			return false, routineFallback, nil
		}, func(context.Context) (routineListener, error) {
			order = append(order, "listen")
			return listener, nil
		}, func(_ context.Context, got routineListener, _ string, delay time.Duration) error {
			waits++
			if delay != routineFallback {
				t.Fatal("disconnect lost bounded fallback")
			}
			if got == nil {
				order = append(order, "timer")
				return context.DeadlineExceeded
			}
			order = append(order, "wait")
			if waits == 1 {
				return errors.New("disconnected")
			}
			cancel()
			return ctx.Err()
		})
		if strings.Join(order, ",") != "listen,scan,wait,scan,timer,listen,scan,wait" || listener.closed != 2 {
			t.Fatalf("reconnect order=%v closes=%d", order, listener.closed)
		}
	})
}

func TestRoutineHintsFilterTenantAndChannel(t *testing.T) {
	notifications := []*pgconn.Notification{
		{Channel: "aeon_events", Payload: `{"tenant_id":"tenant"}`},
		{Channel: routineChannel, Payload: `{"tenant_id":"other"}`},
		{Channel: routineChannel, Payload: `invalid`},
		{Channel: routineChannel, Payload: `{"tenant_id":"tenant"}`},
	}
	seen := 0
	listener := &routineFixtureListener{next: func(context.Context) (*pgconn.Notification, error) {
		if seen >= len(notifications) {
			t.Fatal("matching hint did not wake the worker")
		}
		n := notifications[seen]
		seen++
		return n, nil
	}}
	if err := waitRoutine(t.Context(), listener, "tenant", time.Hour); err != nil || seen != 4 {
		t.Fatalf("hint filtering seen=%d err=%v", seen, err)
	}
}

type routineWaitStep struct {
	delay  time.Duration
	resume chan string
}

// A wait barrier prevents every fallback timer from firing until the test
// advances it. "hint" waits for a real committed PostgreSQL notification;
// its one-hour timer is only a hang guard, never a route to a passing test.
func routineTestRunner(t *testing.T, d *RoutineDispatcher, tenantID string) (<-chan routineWaitStep, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	steps := make(chan routineWaitStep)
	done := make(chan error, 1)
	go func() {
		done <- runRoutine(ctx, tenantID, d.dispatchOne, d.listenRoutine, func(ctx context.Context, listener routineListener, tenant string, delay time.Duration) error {
			step := routineWaitStep{delay: delay, resume: make(chan string)}
			select {
			case steps <- step:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case action := <-step.resume:
				if action == "hint" {
					if listener == nil {
						t.Error("fixture listener unavailable")
						return errors.New("fixture listener unavailable")
					}
					err := waitRoutine(ctx, listener, tenant, time.Hour)
					if err != nil {
						t.Errorf("committed notification unavailable: %v", err)
					}
					return err
				}
				return context.DeadlineExceeded
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("runner stopped: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Error("runner shutdown hung")
			}
		})
	}
	t.Cleanup(stop)
	return steps, stop
}

func routineStep(t *testing.T, steps <-chan routineWaitStep) routineWaitStep {
	t.Helper()
	select {
	case step := <-steps:
		return step
	case <-time.After(30 * time.Second):
		t.Fatal("routine did not reach the wait barrier")
		return routineWaitStep{}
	}
}

// Risk: idle backoff could delay new work, reclaim a live retry lease, or
// strand work whose hint was missed across restart. Timers never run freely.
func TestRoutineDurableWakeRetryLeaseAndRestart(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	_, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "grok_bot:worker", Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/fixture-hook", Secret: "fixture-routine-sender-key", Role: "primary", MaximumLevel: "simple"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var failNext atomic.Bool
	d := &RoutineDispatcher{m: m, client: &http.Client{Transport: routineRoundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		status := 200
		if failNext.Swap(false) {
			status = 503
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}}
	steps, stop := routineTestRunner(t, d, w.agent.TenantID)
	step := routineStep(t, steps) // LISTEN established, initial durable scan empty.
	if step.delay != routineFallback || calls.Load() != 0 {
		t.Fatal("startup was not empty")
	}
	mustCompatSend(t, m, w.sender, project, compatInput("grok_bot:worker", "prompt-wake"))
	step.resume <- "hint"
	step = routineStep(t, steps)
	if calls.Load() != 1 || step.delay != routineFallback {
		t.Fatal("new committed work did not wake and drain before a fallback timer")
	}

	failNext.Store(true)
	retry := mustCompatSend(t, m, w.sender, project, compatInput("grok_bot:worker", "retry-wake"))
	step.resume <- "hint"
	step = routineStep(t, steps)
	if calls.Load() != 2 || step.delay <= 0 || step.delay > 2*time.Second {
		t.Fatalf("retry failed to use persisted deadline: calls=%d delay=%s", calls.Load(), step.delay)
	}
	var id, state, reason string
	var attempts int
	var token *string
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT id::text,state,reason,attempts,lease_token::text FROM inbox_message_deliveries WHERE message_id=$1::uuid`, retry.ID).Scan(&id, &state, &reason, &attempts, &token); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || reason != "http_error" || attempts != 1 || token != nil {
		t.Fatalf("retry did not retain its durable state: state=%s reason=%s attempts=%d token-present=%t", state, reason, attempts, token != nil)
	}
	// Pin a live lease far ahead, so the proof cannot depend on machine speed.
	setDeliveryLease(t, w, id, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	step.resume <- "hint" // Real retry hint must not override a live lease.
	step = routineStep(t, steps)
	if calls.Load() != 2 || step.delay != routineFallback {
		t.Fatal("wake hint reclaimed a live retry lease")
	}
	setDeliveryLease(t, w, id, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	step.resume <- "timer"
	step = routineStep(t, steps)
	if calls.Load() != 3 || step.delay != routineFallback {
		t.Fatal("expired retry lease was not recovered by the fallback scan")
	}
	stop()

	// A process disappears after claiming: the next process must preserve the
	// live lease, then recover from durable rows without receiving a hint.
	restart := mustCompatSend(t, m, w.sender, project, compatInput("grok_bot:worker", "restart-recovery"))
	work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "grok_bot:worker", Adapter: "grok_bot_routine"})
	if err != nil || work == nil || work.Message == nil || work.Message.ID != restart.ID || work.LeaseToken == "" {
		t.Fatalf("restart fixture claim failed: %v", err)
	}
	setDeliveryLease(t, w, work.ID, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	steps, stop = routineTestRunner(t, d, w.agent.TenantID)
	step = routineStep(t, steps)
	if calls.Load() != 3 || step.delay != routineFallback {
		t.Fatal("restart reclaimed a live lease")
	}
	stop()
	setDeliveryLease(t, w, work.ID, time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	steps, stop = routineTestRunner(t, d, w.agent.TenantID)
	_ = routineStep(t, steps) // No hint or timer delivered since this restart.
	if calls.Load() != 4 {
		t.Fatal("startup scan missed expired durable work")
	}
	_, err = m.complete(t.Context(), w.agent, project, completeInput{ID: work.ID, LeaseToken: work.LeaseToken, EffectiveLevel: "simple"})
	var conflict *httpError
	if !errors.As(err, &conflict) || conflict.status != 409 || conflict.code != "lease_conflict" || conflict.msg != "delivery lease changed" {
		t.Fatalf("stale lease did not fail for the lease fence: %v", err)
	}
	stop()
}

// Risk: ready work behind one address's live head must not hide another
// address's earlier lease deadline. Database-clock injection makes boundaries
// exact even when the host clock or test runner is slow.
func TestRoutineEarliestDeadlineBehindLeasedHead(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	insertPrincipal(t, w.db, w.agent.TenantID, tenant.Agent, "other", nil)
	for _, address := range []string{"grok_bot:worker", "grok_bot:other"} {
		_, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: address, Adapter: "grok_bot_routine", Kind: "https_webhook", Ref: "https://8.8.8.8/fixture-hook", Secret: "fixture-routine-sender-key", Role: "primary", MaximumLevel: "simple"})
		if err != nil {
			t.Fatal(err)
		}
	}
	head := mustCompatSend(t, m, w.sender, project, compatInput("grok_bot:worker", "leased-head"))
	work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "grok_bot:worker", Adapter: "grok_bot_routine"})
	if err != nil || work == nil || work.Message == nil || work.Message.ID != head.ID {
		t.Fatalf("head fixture claim failed: %v", err)
	}
	blocked := mustCompatSend(t, m, w.sender, project, compatInput("grok_bot:worker", "behind-head"))
	earlier := mustCompatSend(t, m, w.sender, project, compatInput("grok_bot:other", "earlier-deadline"))
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	setDeliveryLease(t, w, work.ID, now.Add(4*time.Second))
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_deliveries SET lease_until=$2 WHERE message_id=$1::uuid`, earlier.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	m.databaseClock = func(context.Context, pgx.Tx) (time.Time, error) { return now, nil }
	d := &RoutineDispatcher{m: m}
	worked, delay, err := d.dispatchOne(t.Context(), w.agent.TenantID)
	if worked || err != nil || delay != 2*time.Second {
		t.Fatalf("blocked head hid earlier deadline: worked=%t delay=%s err=%v", worked, delay, err)
	}
	var attempts int
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT attempts FROM inbox_message_deliveries WHERE message_id=$1::uuid`, blocked.ID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("blocked message was claimed: attempts=%d err=%v", attempts, err)
	}
	now = now.Add(2 * time.Second)
	candidate, delay, err := d.nextRoutine(dbtest.Seed(t.Context()), w.agent.TenantID)
	// The ready worker row still sorts first; its fenced claim cannot skip the
	// live head. Verify the newly due address directly after holding that row.
	if err != nil || candidate.address != "grok_bot:worker" || delay != 2*time.Second {
		t.Fatalf("deadline boundary changed queue ordering: address=%s delay=%s err=%v", candidate.address, delay, err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_deliveries SET lease_until=$2 WHERE message_id=$1::uuid`, blocked.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	candidate, delay, err = d.nextRoutine(dbtest.Seed(t.Context()), w.agent.TenantID)
	if err != nil || candidate.address != "grok_bot:other" || delay != 2*time.Second {
		t.Fatalf("expired lease did not become due at the database boundary: address=%s delay=%s err=%v", candidate.address, delay, err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

type retryTestClock struct {
	now   time.Time
	waits []time.Duration
	wait  func(context.Context) error
}

func (c *retryTestClock) Now() time.Time { return c.now }
func (c *retryTestClock) Wait(ctx context.Context, d time.Duration) error {
	c.waits = append(c.waits, d)
	if c.wait != nil {
		if err := c.wait(ctx); err != nil {
			return err
		}
	}
	c.now = c.now.Add(d)
	return nil
}

func missingPollKey() error {
	return &client.StatusError{Status: 403, Message: "daemon poll key rejected", AttachRefusal: attachwatch.RefusalPollKeyUnknown}
}

func TestAttachTransportRefusalsNeverRegister(t *testing.T) {
	for name, refusal := range map[string]error{
		"wrong or legacy key": &client.StatusError{Status: 403, Message: "daemon poll key rejected"},
		"forbidden":           &client.StatusError{Status: 403, Message: "forbidden"},
		"revoked":             &client.StatusError{Status: 401, AttachRefusal: attachwatch.RefusalPairing},
		"revoked computer":    &client.StatusError{Status: 403, AttachRefusal: attachwatch.RefusalPairing},
		"draining":            &client.StatusError{Status: 409, AttachRefusal: attachwatch.RefusalDraining},
		"removed":             &client.StatusError{Status: 409, AttachRefusal: attachwatch.RefusalEnrollment},
		"wrong tenant":        &client.StatusError{Status: 403, ReasonCode: "missing_project_access"},
		"scope denial wins":   &client.StatusError{Status: 403, AttachRefusal: attachwatch.RefusalPollKeyUnknown, ReasonCode: "missing_key_scope"},
		"ended":               &client.StatusError{Status: 410},
		"wrong status":        &client.StatusError{Status: 500, AttachRefusal: attachwatch.RefusalPollKeyUnknown},
		"offline":             errors.New("network unavailable"),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			clock := &retryTestClock{}
			transport := NewAttachTransport(func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error) {
				calls++
				return attachwatch.View{}, refusal
			}, func(context.Context) error { t.Fatal("refusal triggered registration"); return nil }, clock)
			_, err := transport.Exchange(t.Context(), attachwatch.DeviceRequest{})
			if err != refusal || calls != 1 || len(clock.waits) != 0 {
				t.Fatal("refusal changed or retried")
			}
		})
	}
}

func TestAttachTransportOneReplayAndBoundedBackoff(t *testing.T) {
	clock := &retryTestClock{}
	registrations, exchanges := 0, 0
	transport := NewAttachTransport(func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error) {
		exchanges++
		return attachwatch.View{}, missingPollKey()
	}, func(context.Context) error { registrations++; return nil }, clock)
	for round := 0; round < 12; round++ {
		// Exercise both jitter endpoints deterministically at every window.
		transport.random = func(n int64) int64 {
			if round%2 == 0 {
				return 0
			}
			return n - 1
		}
		before := exchanges
		_, err := transport.Exchange(t.Context(), attachwatch.DeviceRequest{})
		if err == nil || registrations != round+1 || exchanges != before+2 {
			t.Fatal("recovery hid refusal or replayed more than once")
		}
		// A flood during cooldown can make ordinary calls, but no registrations.
		for i := 0; i < 100; i++ {
			_, _ = transport.Exchange(t.Context(), attachwatch.DeviceRequest{})
		}
		if registrations != round+1 || exchanges != before+102 {
			t.Fatal("registration retry storm")
		}
		delay := clock.waits[round]
		window := min(500*time.Millisecond*time.Duration(1<<round), 8*time.Second)
		if delay < window/2 || delay > window {
			t.Fatalf("backoff out of bounds: %v", delay)
		}
		if round%2 == 0 && delay != window/2 || round%2 == 1 && delay != window {
			t.Fatal("jitter ignored the random draw")
		}
		clock.now = clock.now.Add(8 * time.Second)
	}
}

func TestAttachTransportRegistrationFailureAndCancellation(t *testing.T) {
	for _, cancelDuringWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "registration fails", true: "cancelled wait"}[cancelDuringWait], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			clock := &retryTestClock{}
			if cancelDuringWait {
				clock.wait = func(context.Context) error { cancel(); return nil }
			}
			want := &client.StatusError{Status: 403, AttachRefusal: attachwatch.RefusalPairing}
			exchanges, registrations := 0, 0
			transport := NewAttachTransport(func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error) {
				exchanges++
				return attachwatch.View{}, missingPollKey()
			}, func(context.Context) error { registrations++; return want }, clock)
			_, err := transport.Exchange(ctx, attachwatch.DeviceRequest{})
			if exchanges != 1 {
				t.Fatal("failed registration replayed operation")
			}
			if cancelDuringWait {
				if !errors.Is(err, context.Canceled) || registrations != 0 {
					t.Fatal("cancelled wait registered")
				}
			} else if err != want || registrations != 1 {
				t.Fatal("registration failure was hidden")
			}
			_, _ = transport.Exchange(t.Context(), attachwatch.DeviceRequest{})
			if len(clock.waits) != 1 {
				t.Fatal("failure/cancellation bypassed cooldown")
			}
		})
	}
}

func TestAttachTransportConcurrentCallsShareRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := &retryTestClock{}
		entered, release := make(chan struct{}), make(chan struct{})
		registered, registrations := false, 0
		transport := NewAttachTransport(func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error) {
			if !registered {
				return attachwatch.View{}, missingPollKey()
			}
			return attachwatch.View{State: "pending"}, nil
		}, func(context.Context) error {
			registrations++
			close(entered)
			<-release
			registered = true
			return nil
		}, clock)
		const callers = 32
		results := make(chan error, callers)
		call := func() {
			out, err := transport.Exchange(t.Context(), attachwatch.DeviceRequest{})
			if err == nil && out.State != "pending" {
				err = errors.New("lost response")
			}
			results <- err
		}
		go call()
		<-entered
		for i := 1; i < callers; i++ {
			go call()
		}
		// Every competing call is blocked on the transport gate while the first
		// registration is still in flight. Virtual time proves the overlap.
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := transport.Exchange(ctx, attachwatch.DeviceRequest{}); !errors.Is(err, context.Canceled) {
			t.Fatal("queued cancellation ignored")
		}
		close(release)
		for i := 0; i < callers; i++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if registrations != 1 || len(clock.waits) != 1 {
			t.Fatal("concurrent requests duplicated registration")
		}
	})
}

func TestAttachTransportDeadlineBoundsBackoffAndRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls, registrations := 0, 0
		transport := NewAttachTransport(func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error) {
			calls++
			return attachwatch.View{}, missingPollKey()
		}, func(ctx context.Context) error {
			registrations++
			<-ctx.Done()
			return ctx.Err()
		}, nil)
		transport.random = func(int64) int64 { return 0 }
		start := time.Now()
		_, err := transport.Exchange(t.Context(), attachwatch.DeviceRequest{})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 20*time.Second || calls != 1 || registrations != 1 {
			t.Fatal("registration/backoff exceeded the total deadline or replayed a timed-out operation")
		}
	})
}

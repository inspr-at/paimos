// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
)

const (
	attachRetryMin        = 500 * time.Millisecond
	attachRetryMax        = 8 * time.Second
	attachExchangeTimeout = 20 * time.Second
)

// AttachRetryClock makes registration backoff deterministic in tests.
type AttachRetryClock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

type attachRetryClock struct{}

func (attachRetryClock) Now() time.Time { return time.Now() }
func (attachRetryClock) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// AttachTransport recovers only explicitly lost server registration. Callbacks
// retain the memory-only poll key and pinned origin. Production registration
// re-reads the long-lived lifecycle proof from the approved setup store for each
// attempt; that proof is not retained by the transport.
// Registration still invalidates earlier approvals, including active watches.
type AttachTransport struct {
	exchange    func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error)
	register    func(context.Context) error
	clock       AttachRetryClock
	random      func(int64) int64
	gate        chan struct{}
	next        time.Time
	backoff     time.Duration
	serverNext  time.Time
	serverError error
}

func NewAttachTransport(exchange func(context.Context, attachwatch.DeviceRequest) (attachwatch.View, error), register func(context.Context) error, clock AttachRetryClock) *AttachTransport {
	if clock == nil {
		clock = attachRetryClock{}
	}
	return &AttachTransport{exchange: exchange, register: register, clock: clock,
		gate: make(chan struct{}, 1), backoff: attachRetryMin, random: rand.Int64N}
}

func (t *AttachTransport) Exchange(ctx context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
	ctx, cancel := context.WithTimeout(ctx, attachExchangeTimeout)
	defer cancel()
	// Serialize the complete exchange/re-registration/replay, not just the
	// registration call. Queued calls cannot act on a stale rejection.
	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-ctx.Done():
		return attachwatch.View{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return attachwatch.View{}, err
	}
	if t.clock.Now().Before(t.serverNext) {
		return attachwatch.View{}, t.serverError
	}
	t.serverError = nil
	rememberDelay := func(err error) {
		var status *client.StatusError
		if errors.As(err, &status) && status.RetryAfter > 0 {
			t.serverNext = t.clock.Now().Add(status.RetryAfter)
			t.serverError = err
		}
	}
	out, err := t.exchange(ctx, in)
	rememberDelay(err)
	if err == nil {
		t.backoff = attachRetryMin
		return out, nil
	}
	var status *client.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusForbidden || status.AttachRefusal != attachwatch.RefusalPollKeyUnknown || status.ReasonCode != "" || t.clock.Now().Before(t.next) || t.clock.Now().Before(t.serverNext) {
		return out, err
	}
	delay := t.backoff/2 + time.Duration(t.random(int64(t.backoff/2)+1))
	t.backoff = min(t.backoff*2, attachRetryMax)
	// Keep a cooldown even after cancellation, failed registration or another
	// unknown response. There is no background loop and at most one replay.
	defer func() { t.next = t.clock.Now().Add(delay) }()
	if err = t.clock.Wait(ctx, delay); err != nil {
		return attachwatch.View{}, err
	}
	if err = ctx.Err(); err != nil {
		return attachwatch.View{}, err
	}
	if err = t.register(ctx); err != nil {
		rememberDelay(err)
		return attachwatch.View{}, err
	}
	out, err = t.exchange(ctx, in)
	rememberDelay(err)
	if err == nil {
		t.backoff = attachRetryMin
	}
	return out, err
}

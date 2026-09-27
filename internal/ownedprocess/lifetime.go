// SPDX-License-Identifier: AGPL-3.0-only

package ownedprocess

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

var ErrAuthorizationExpired = errors.New("process signal authorization expired")

// Lifetime keeps the group leader's PID reserved until all signaling has
// finished. Wait first observes exit without reaping, then takes the same lock
// as Signal before cmd.Wait releases the PID. A stored PID alone is never used.
type Lifetime struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	available bool
	reaped    bool
}

func Track(cmd *exec.Cmd) *Lifetime { return &Lifetime{cmd: cmd, available: waitObservationSupported} }
func TrackingSupported() bool       { return waitObservationSupported }
func (l *Lifetime) Wait() error     { return l.wait(observeExit) }

func (l *Lifetime) wait(observe func(int) error) error {
	_ = observe(l.cmd.Process.Pid)
	l.mu.Lock()
	// Reject all future signals before releasing the PID. The observer can
	// also fail while the child lives, or because another reaper released it:
	// fail closed in both cases, without holding the lock across a blocking
	// Wait. Cleanup reports unavailable instead of guessing a process group.
	l.available = false
	l.mu.Unlock()
	result := l.cmd.Wait()
	l.mu.Lock()
	l.reaped = true
	l.mu.Unlock()
	return result
}
func (l *Lifetime) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.verify()
}
func (l *Lifetime) verify() error {
	if !l.available || l.reaped {
		return errors.New("live process ownership unavailable")
	}
	return Verify(l.cmd, true)
}
func (l *Lifetime) Signal(force bool) error {
	return l.signal(context.Background(), force, nil)
}

// SignalBefore carries the immutable server authorization to the final signal
// lock. A queued request cannot outlive its deadline while waiting for this
// mutex, even if its caller already checked the same deadline earlier.
func (l *Lifetime) SignalBefore(ctx context.Context, force bool, expiresAt time.Time) error {
	return l.signal(ctx, force, &expiresAt)
}

func (l *Lifetime) signal(ctx context.Context, force bool, expiresAt *time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.verify(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if expiresAt != nil && !time.Now().Before(*expiresAt) {
		return ErrAuthorizationExpired
	}
	return Signal(l.cmd, force)
}

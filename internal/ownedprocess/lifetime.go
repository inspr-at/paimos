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

var ErrCleanupUnconfirmed = errors.New("owned process group cleanup unconfirmed")

// Lifetime keeps the group leader's PID reserved until all signaling has
// finished. Wait first observes exit without reaping, then takes the same lock
// as Signal before cmd.Wait releases the PID. A stored PID alone is never used.
type Lifetime struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	available bool
	reaped    bool
	verified  bool
}

func Track(cmd *exec.Cmd) *Lifetime { return &Lifetime{cmd: cmd, available: waitObservationSupported} }
func TrackingSupported() bool       { return waitObservationSupported }
func (l *Lifetime) Wait() error     { return l.wait(observeExit) }

func (l *Lifetime) wait(observe func(int) error) error {
	return l.waitOwned(observe, false)
}

// WaitGroup terminates remaining group members before reaping the leader. The
// unreaped child reserves the initially verified identity throughout signaling.
// Darwin hides zombie leaders from getpgid, so successful WNOWAIT observation
// and the launch-time verification authorize this final group cleanup.
func (l *Lifetime) WaitGroup() error { return l.waitOwned(observeExit, true) }

func (l *Lifetime) waitOwned(observe func(int) error, killGroup bool) error {
	observeErr := observe(l.cmd.Process.Pid)
	l.mu.Lock()
	var groupErr error
	if killGroup {
		groupErr = observeErr
		if groupErr == nil {
			if !l.available || !l.verified || l.reaped {
				groupErr = errors.New("verified process group unavailable")
			}
		}
		if groupErr == nil {
			groupErr = Signal(l.cmd, true)
			if groupErr != nil && emptyExitedGroup(l.cmd.Process.Pid, groupErr) {
				groupErr = nil
			}
		}
	}
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
	if groupErr != nil {
		return errors.Join(result, ErrCleanupUnconfirmed, groupErr)
	}
	return result
}
func (l *Lifetime) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.verify()
	if err == nil {
		l.verified = true
	}
	return err
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

// SignalBefore carries a local monotonic deadline, derived from the server's
// remaining authorization budget, to the final signal lock. Never pass a remote
// wall-clock timestamp here. Waiting for this mutex cannot extend the deadline.
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

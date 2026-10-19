// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// commandInterrupt is one command's signal state. It lives behind a pointer so
// a copied runtime shares the state instead of copying atomic values.
type commandInterrupt struct {
	state *commandSignalState
}

type commandSignalState struct {
	interrupted atomic.Bool // set only after SIGINT or SIGTERM
	armed       atomic.Bool // the command watchdog listens for SIGINT and SIGTERM
	claimed     atomic.Bool // the command owns its bounded shutdown; the watchdog never exits for it
}

// ensure publishes the state before a runtime is copied. The command goroutine
// does this before it starts work that runs on other goroutines.
func (c *commandInterrupt) ensure() {
	if c.state == nil {
		c.state = new(commandSignalState)
	}
}

func (c *commandInterrupt) Load() bool {
	return c != nil && c.state != nil && c.state.interrupted.Load()
}

func (c *commandInterrupt) Store(v bool) {
	c.ensure()
	c.state.interrupted.Store(v)
}

// claim marks the command's shutdown as its own and reports whether the
// command watchdog already listens for SIGINT and SIGTERM.
func (c *commandInterrupt) claim() bool {
	c.ensure()
	c.state.claimed.Store(true)
	return c.state.armed.Load()
}

const commandInterruptGrace = 2 * time.Second

// armCommandSignals cancels the request context on SIGINT and SIGTERM.
// A command that ignores that context still exits after a short grace period
// instead of waiting silently, unless it claimed its own bounded shutdown with
// claimSignals. Callers that already installed a context, including tests, keep
// it: cancelling that context is the caller's cancellation, not a user
// interrupt. The signal context is released when the command returns so the
// next command on this runtime does not inherit a canceled context.
func (rt *runtime) armCommandSignals() func() {
	if rt.requestContext != nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.requestContext = ctx
	rt.commandInterrupted.ensure()
	state := rt.commandInterrupted.state
	state.interrupted.Store(false)
	state.claimed.Store(false)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	state.armed.Store(true)
	finished := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer signal.Stop(sigs)
		select {
		case <-sigs:
			state.interrupted.Store(true)
			cancel()
			timer := time.NewTimer(commandInterruptGrace)
			select {
			case <-finished:
				timer.Stop()
			case <-timer.C:
				if state.claimed.Load() {
					// Owned children, sessions and terminals finish their own
					// bounded shutdown; exiting here would orphan them.
					<-finished
					return
				}
				fmt.Fprintln(rt.stderr, rt.program+": interrupted")
				os.Exit(130)
			}
		case <-finished:
		}
	}()
	return func() {
		close(finished)
		cancel()
		<-done
		state.armed.Store(false)
		state.claimed.Store(false)
		state.interrupted.Store(false)
		if rt.requestContext == ctx {
			rt.requestContext = nil
		}
	}
}

// claimSignals is for commands with their own bounded shutdown: owned child
// processes, harness sessions, terminal state or a supervisor. The returned
// context ends on SIGINT, SIGTERM and any extra signals, and the command
// watchdog no longer exits the process before that shutdown completes. Under
// the watchdog, SIGINT and SIGTERM arrive through the request context, so the
// interrupt is recorded before the command sees the cancellation.
func (rt *runtime) claimSignals(extra ...os.Signal) (context.Context, context.CancelFunc) {
	sigs := extra
	if !rt.commandInterrupted.claim() {
		sigs = append([]os.Signal{syscall.SIGINT, syscall.SIGTERM}, extra...)
	}
	if len(sigs) == 0 {
		// signal.NotifyContext without signals would relay every signal.
		return context.WithCancel(rt.context())
	}
	return signal.NotifyContext(rt.context(), sigs...)
}

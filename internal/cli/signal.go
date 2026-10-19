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

// commandInterrupt is one command's signal flag. It lives behind a pointer so
// a copied runtime shares the flag instead of copying an atomic value.
type commandInterrupt struct {
	flag *atomic.Bool
}

// ensure publishes the flag before a runtime is copied. The command goroutine
// does this before it starts work that runs on other goroutines.
func (c *commandInterrupt) ensure() {
	if c.flag == nil {
		c.flag = new(atomic.Bool)
	}
}

func (c *commandInterrupt) Load() bool {
	return c != nil && c.flag != nil && c.flag.Load()
}

func (c *commandInterrupt) Store(v bool) {
	c.ensure()
	c.flag.Store(v)
}

const commandInterruptGrace = 2 * time.Second

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// armCommandSignals cancels the request context on SIGINT and SIGTERM.
// A command that ignores that context still exits after a short grace period
// instead of waiting silently. Callers that already installed a context, including
// tests, keep it: cancelling that context is the caller's cancellation, not a
// user interrupt. The signal context is released when the command returns so
// the next command on this runtime does not inherit a canceled context.
func (rt *runtime) armCommandSignals() func() {
	if rt.requestContext != nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.requestContext = ctx
	rt.commandInterrupted.Store(false)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	finished := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer signal.Stop(sigs)
		select {
		case <-sigs:
			rt.commandInterrupted.Store(true)
			cancel()
			timer := time.NewTimer(commandInterruptGrace)
			select {
			case <-finished:
				timer.Stop()
			case <-timer.C:
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
		rt.commandInterrupted.Store(false)
		if rt.requestContext == ctx {
			rt.requestContext = nil
		}
	}
}

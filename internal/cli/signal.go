// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const commandInterruptGrace = 2 * time.Second

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// armCommandSignals cancels the request context on SIGINT and SIGTERM.
// A command that ignores that context still exits after a short grace period
// instead of waiting silently. Callers that already installed a context, including
// tests, keep it.
func (rt *runtime) armCommandSignals() func() {
	if rt.requestContext != nil {
		return func() {}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	rt.requestContext = ctx
	finished := make(chan struct{})
	go func() {
		<-ctx.Done()
		timer := time.NewTimer(commandInterruptGrace)
		select {
		case <-finished:
			timer.Stop()
		case <-timer.C:
			fmt.Fprintln(rt.stderr, rt.program+": interrupted")
			os.Exit(130)
		}
	}()
	return func() {
		close(finished)
		cancel()
	}
}

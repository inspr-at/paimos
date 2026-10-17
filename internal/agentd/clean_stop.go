// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"sync"
)

// ArmCleanStop runs the clean stop when ctx ends and when the returned
// function is called. Both paths remove this process's socket, token and
// hook pin before the listener closes, and neither waits for drain. A second
// call is a no-op. SIGKILL cannot run this: the kernel does not deliver it
// to user code, and a later start relies on stale-file recovery.
func ArmCleanStop(ctx context.Context, local *LocalServer) (stop func()) {
	if local == nil {
		return func() {}
	}
	stopped := make(chan struct{})
	var once sync.Once
	run := func() {
		once.Do(func() {
			close(stopped)
			_ = local.Close()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			run()
		case <-stopped:
		}
	}()
	return run
}

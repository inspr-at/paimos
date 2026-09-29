// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

func limitEvent(hit *capacity.LimitHit) AdapterEvent {
	return AdapterEvent{Kind: "status", ErrorCode: "vendor_limit", Activity: "throttled", Capacity: hit.Readings, VendorLimit: hit}
}

func (s *Supervisor) observeVendorLimit(e *owned, hit *capacity.LimitHit) {
	e.mu.Lock()
	if e.record.Generation != s.generation || e.harnessArchived || e.vendorLimit != nil {
		e.mu.Unlock()
		return
	}
	e.vendorLimit = hit
	e.harness.Activity = "throttled"
	e.mu.Unlock()
	s.observeCapacity(e, hit.Readings)
	// Never block a stream reader on stopping its own process. A hit can also
	// arrive during Start before the supervisor has installed the process.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		s.flushCapacity(ctx, e)
		for {
			e.mu.Lock()
			proc := e.process
			exited := e.record.ExitObserved
			e.mu.Unlock()
			if exited {
				return
			}
			if proc != nil {
				_ = proc.Stop(ctx)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
}

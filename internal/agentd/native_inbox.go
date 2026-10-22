// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"time"
)

// Only committed hints reach this loop. Claims and P3a still recheck current
// consent, pairing, runtime and deadlines before any possible provider write.
func (e *nativeOwned) watchInbox(ctx context.Context) {
	e.mu.Lock()
	b, lease, wake := *e.grant.ExpectedBinding, e.proof.WorkerLease, e.inboxWake
	e.mu.Unlock()
	session := HarnessSession{ID: b.HarnessSessionID, ProjectID: b.ProjectID, Lease: lease}
	backoff := time.Second
	for ctx.Err() == nil {
		_ = e.host.remote.WatchHarness(ctx, session, func() {
			select {
			case wake <- struct{}{}:
			default:
			}
		})
		if !waitHarnessReconnect(ctx, jitterHarnessReconnect(backoff)) {
			return
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (e *nativeOwned) serviceInboxHints(ctx context.Context) {
	e.mu.Lock()
	wake := e.inboxWake
	e.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			_ = e.poll(ctx)
		}
	}
}

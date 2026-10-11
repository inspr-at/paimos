// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"sync"
	"time"
)

const authFailureHold = 15 * time.Second
const authRecoveryHold = 30 * time.Second
const authObservationGap = 30 * time.Second

// Only the shared read-only authentication canary changes global readiness.
// Invalid keys and tenant-specific handler failures cannot evict other tenants.
// Observations count at most once per second so probe bursts cannot trip it.
type authReadiness struct {
	mu          sync.Mutex
	unavailable bool
	since, last time.Time
	failed      bool
	samples     int
}

func (h *authReadiness) observe(now time.Time, failed bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.last.IsZero() && now.Before(h.last) {
		return !h.unavailable
	}
	if h.last.IsZero() || failed != h.failed || now.Sub(h.last) > authObservationGap {
		h.since, h.samples, h.failed, h.last = now, 1, failed, now
	} else if now.Sub(h.last) >= time.Second {
		h.samples++
		h.last = now
	}
	hold := authFailureHold
	if !failed {
		hold = authRecoveryHold
	}
	if h.samples >= 3 && now.Sub(h.since) >= hold {
		h.unavailable = failed
	}
	return !h.unavailable
}

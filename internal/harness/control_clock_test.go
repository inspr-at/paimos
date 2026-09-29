// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"fmt"
	"testing"
	"time"
)

func TestControlRelayUsesDatabaseBudgetAndMonotonicRetention(t *testing.T) {
	for _, year := range []int{2001, 2099} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			dbNow := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			for _, remaining := range []time.Duration{-time.Second, 0, time.Second, time.Hour} {
				expires := dbNow.Add(remaining)
				ttl := controlTTL(&expires, dbNow)
				if ttl != max(0, min(remaining, ownershipWindow)) {
					t.Fatalf("database budget: %s", ttl)
				}
				q := &controlRelay{}
				if !q.put("key", "private fixture", time.Now(), ttl) {
					t.Fatal("relay full")
				}
				if got := q.take("key"); (got != "") != (remaining > 0) {
					t.Fatalf("skewed relay returned %q for %s", got, remaining)
				}
				// A delayed DB reply consumes the budget before put can run.
				if !q.put("slow", "private fixture", time.Now().Add(-time.Minute), ttl) || q.take("slow") != "" {
					t.Fatal("slow query extended retention")
				}
			}
			q := &controlRelay{}
			if !q.put("timer", "private fixture", time.Now(), 5*time.Millisecond) {
				t.Fatal("relay full")
			}
			limit := time.Now().Add(time.Second)
			for {
				q.mu.Lock()
				n := len(q.items)
				q.mu.Unlock()
				if n == 0 {
					break
				}
				if time.Now().After(limit) {
					t.Fatal("timer retained private input without traffic")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

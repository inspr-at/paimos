// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"testing"
	"time"
)

func TestLeavingLevelSelection(t *testing.T) {
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		budget, estimate time.Duration
		known, inbox     bool
		level            string
		start            time.Duration
	}{
		{"finish fits", 15 * time.Minute, 5 * time.Minute, true, true, "wrap_up", 0},
		{"ten minutes requires handover", 15 * time.Minute, 10 * time.Minute, true, true, "pause", 5 * time.Minute},
		{"finish misses deadline", 4 * time.Minute, 6 * time.Minute, true, true, "pause", 0},
		{"unknown estimate waits", 15 * time.Minute, 0, false, true, "pause", 5 * time.Minute},
		{"quickly", 2 * time.Minute, time.Minute, true, true, "pause_quickly", 0},
		{"at deadline", 0, 0, false, true, "stop_now", 0},
		{"overdue", -time.Second, 0, false, true, "stop_now", 0},
		{"no inbox waits for deadline", 15 * time.Minute, time.Minute, true, false, "stop_now", 15 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var remaining *time.Duration
			if tc.known {
				remaining = &tc.estimate
			}
			level, start := leavingLevel(now, now.Add(tc.budget), remaining, tc.inbox)
			if level != tc.level || !start.Equal(now.Add(tc.start)) {
				t.Fatalf("got %s at %s", level, start)
			}
		})
	}
}

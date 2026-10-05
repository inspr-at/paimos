// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"testing"
	"time"
)

func TestWaitingClockTerminalWithoutStatusAndLifetimeBounds(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Second)
	waiting, running := "waiting", "running"
	tests := []struct {
		name  string
		marks []struct {
			status *string
			offset time.Duration
		}
		want int64
	}{
		{"terminal omits status", []struct {
			status *string
			offset time.Duration
		}{{&waiting, 2 * time.Second}, {nil, 4 * time.Second}, {nil, 10 * time.Second}}, 8000},
		{"repeated waits do not restart", []struct {
			status *string
			offset time.Duration
		}{{&waiting, 2 * time.Second}, {&waiting, 4 * time.Second}, {&running, 7 * time.Second}}, 5000},
		{"clip prestart and postend", []struct {
			status *string
			offset time.Duration
		}{{&waiting, -2 * time.Second}, {&running, 12 * time.Second}}, 10000},
		{"two waits and heartbeat", []struct {
			status *string
			offset time.Duration
		}{{&waiting, time.Second}, {&running, 3 * time.Second}, {nil, 4 * time.Second}, {&waiting, 6 * time.Second}}, 6000},
		{"no waiting", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := waitingClock{start: start, end: end}
			for _, m := range tt.marks {
				c.observe(m.status, start.Add(m.offset))
			}
			if got := c.finish(); got != tt.want {
				t.Fatalf("waiting=%d want=%d", got, tt.want)
			}
			if got := c.finish(); got != tt.want {
				t.Fatalf("finish doubled span: %d", got)
			}
		})
	}
}

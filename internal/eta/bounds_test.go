// SPDX-License-Identifier: AGPL-3.0-only

package eta

import (
	"testing"
	"time"
)

func TestAllowedWindow(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"now", now, true},
		{"30 days ago", now.Add(-MaxPast), true},
		{"skew before 30 days", now.Add(-MaxPast - ClockSkew), true},
		{"31 days ago", now.Add(-MaxPast - ClockSkew - time.Minute), false},
		{"365 days ahead", now.Add(MaxFuture), true},
		{"skew after 365 days", now.Add(MaxFuture + ClockSkew), true},
		{"366 days ahead", now.Add(MaxFuture + ClockSkew + time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Allowed(tc.at, now); got != tc.ok {
				t.Fatalf("Allowed(%s) = %v, want %v", tc.at, got, tc.ok)
			}
		})
	}
}

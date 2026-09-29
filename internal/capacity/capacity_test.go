// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func TestReadingFreshnessAcrossReset(t *testing.T) {
	now := instant("2026-09-28T08:00:00Z")
	r := Reading{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 20, ResetsAt: now.Add(4 * time.Hour), ReadAt: now, Source: "harness"}
	if err := r.Validate(now); err != nil {
		t.Fatal(err)
	}
	if !r.StartsAt().Equal(now.Add(-time.Hour)) {
		t.Fatal(r.StartsAt())
	}
	for _, tt := range []struct {
		age  time.Duration
		want string
	}{{10 * time.Minute, "fresh"}, {11 * time.Minute, "aging"}, {50 * time.Minute, "aging"}, {51 * time.Minute, "stale"}, {4 * time.Hour, "expired"}} {
		if got := r.Freshness(now.Add(tt.age)); got != tt.want {
			t.Fatalf("%s %s", tt.want, got)
		}
	}
	no := false
	r.OrdinaryUsageAllowed = &no
	if r.Routable(now) {
		t.Fatal("vendor denial ignored")
	}
	r.OrdinaryUsageAllowed = nil
	r.Source = "estimate"
	if r.Routable(now) {
		t.Fatal("estimate routed")
	}
}

func TestReadingRequiresObservedPercent(t *testing.T) {
	raw := `{"window_kind":"5h","window_minutes":300,"used_percent":0,"resets_at":"2026-09-28T09:00:00Z","read_at":"2026-09-28T08:00:00Z","source":"harness"}`
	var r Reading
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(raw, `"used_percent":0,`, "", 1), strings.Replace(raw, `"used_percent":0`, `"used_percent":null`, 1), strings.Replace(raw, `"used_percent":0`, `"extra":"x","used_percent":0`, 1)} {
		if err := json.Unmarshal([]byte(bad), &r); err == nil {
			t.Fatal("unknown or unobserved usage accepted")
		}
	}
	var s Schedule
	if err := json.Unmarshal([]byte(`{"timezone":"UTC"}`), &s); err == nil {
		t.Fatal("partial schedule accepted")
	}
}

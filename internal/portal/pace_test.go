// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"testing"
	"time"
)

func TestPaceFromIncludesSlowGapsAndSkipsTheFuture(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	released := []time.Time{
		now.Add(-50 * 24 * time.Hour),
		now.Add(-2 * time.Hour),
		now.Add(-10 * 24 * time.Hour),
		now.Add(24 * time.Hour),
	}
	got := paceFrom(now, true, released, []int{10, 20})
	if got.Releases30d == nil || *got.Releases30d != 2 {
		t.Fatalf("releases in 30 days %+v", got.Releases30d)
	}
	if got.MedianReleaseGapDays == nil || *got.MedianReleaseGapDays != 25 {
		t.Fatalf("median gap %+v", got.MedianReleaseGapDays)
	}
	if got.WishToLiveMedianDays == nil || *got.WishToLiveMedianDays != 15 {
		t.Fatalf("wish median %+v", got.WishToLiveMedianDays)
	}
	empty := paceFrom(now, false, nil, nil)
	if !empty.empty() {
		t.Fatalf("unlinked pace %+v", empty)
	}
	zero := paceFrom(now, true, nil, nil)
	if zero.Releases30d == nil || *zero.Releases30d != 0 || zero.MedianReleaseGapDays != nil {
		t.Fatalf("linked with no releases %+v", zero)
	}
}

func TestMedianInts(t *testing.T) {
	if _, ok := medianInts(nil); ok {
		t.Fatal("empty median")
	}
	if got, ok := medianInts([]int{7}); !ok || got != 7 {
		t.Fatalf("single %d %v", got, ok)
	}
	if got, _ := medianInts([]int{1, 2, 3}); got != 2 {
		t.Fatalf("odd %d", got)
	}
	if got, _ := medianInts([]int{10, 11}); got != 11 {
		t.Fatalf("half up %d", got)
	}
}

func TestSourceAge(t *testing.T) {
	today := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		days           int
		stale, recheck bool
	}{
		{181, true, true},
		{180, false, true},
		{90, false, true},
		{89, false, false},
		{0, false, false},
		{-1, false, false},
	}
	for _, tc := range cases {
		day := today.AddDate(0, 0, -tc.days).Format("2006-01-02")
		if tc.days < 0 {
			day = today.AddDate(0, 0, -tc.days).Format("2006-01-02")
		}
		stale, recheck := ageFlags(day, today)
		if stale != tc.stale || recheck != tc.recheck {
			t.Fatalf("%d days stale %v recheck %v", tc.days, stale, recheck)
		}
	}
}

func TestValidSourceURL(t *testing.T) {
	ok, good := validSourceURL("https://example.com/help")
	if !good || ok != "https://example.com/help" {
		t.Fatalf("accepted %q %v", ok, good)
	}
	for _, raw := range []string{
		"http://example.com/help",
		"https://user:pass@example.com/help",
		"https://localhost/help",
		"https://127.0.0.1/help",
		"https://files.local/help",
		"javascript:alert(1)",
		"https://example.com/a b",
	} {
		if _, good := validSourceURL(raw); good {
			t.Fatalf("accepted %s", raw)
		}
	}
	if quote, ok := storedQuote("Short"); ok {
		t.Fatalf("short quote %q", quote)
	}
	if _, ok := storedQuote("A sourced fact."); !ok {
		t.Fatal("rejected a real quote")
	}
}

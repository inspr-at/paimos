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
	got, _ := paceFrom(now, true, released, []int{10, 20})
	if got.Releases30d == nil || *got.Releases30d != 2 {
		t.Fatalf("releases in 30 days %+v", got.Releases30d)
	}
	if got.MedianReleaseGapDays == nil || *got.MedianReleaseGapDays != 25 {
		t.Fatalf("median gap %+v", got.MedianReleaseGapDays)
	}
	if got.WishToLiveMedianDays == nil || *got.WishToLiveMedianDays != 15 {
		t.Fatalf("wish median %+v", got.WishToLiveMedianDays)
	}
	empty, _ := paceFrom(now, false, nil, nil)
	if !empty.empty() {
		t.Fatalf("unlinked pace %+v", empty)
	}
	zero, zeroSample := paceFrom(now, true, nil, nil)
	if zero.Releases30d == nil || *zero.Releases30d != 0 || zero.MedianReleaseGapDays != nil {
		t.Fatalf("linked with no releases %+v", zero)
	}
	if !zeroSample.forPublic(zero).empty() {
		t.Fatal("public page reported zero releases")
	}
}

func TestPublicPaceOmitsGroupsBelowFive(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	series := func(n, startDay int) []time.Time {
		out := make([]time.Time, n)
		for i := 0; i < n; i++ {
			out[i] = now.Add(-time.Duration(startDay+i) * 24 * time.Hour)
		}
		return out
	}
	wishes := func(n int) []int {
		out := make([]int, n)
		for i := 0; i < n; i++ {
			out[i] = 17 + i
		}
		return out
	}

	raw4, sample4 := paceFrom(now, true, series(4, 1), wishes(4))
	if raw4.Releases30d == nil || *raw4.Releases30d != 4 || raw4.MedianReleaseGapDays == nil || raw4.WishToLiveMedianDays == nil {
		t.Fatalf("admin still sees a group of four %+v", raw4)
	}
	if !sample4.forPublic(raw4).empty() {
		t.Fatalf("group of four leaked %+v", sample4.forPublic(raw4))
	}

	raw5, sample5 := paceFrom(now, true, series(5, 1), wishes(5))
	pub5 := sample5.forPublic(raw5)
	if pub5.Releases30d == nil || *pub5.Releases30d != 5 || pub5.MedianReleaseGapDays == nil || pub5.WishToLiveMedianDays == nil {
		t.Fatalf("group of five %+v sample %+v", pub5, sample5)
	}

	rawOld, sampleOld := paceFrom(now, true, series(5, 40), nil)
	pubOld := sampleOld.forPublic(rawOld)
	if pubOld.Releases30d != nil || pubOld.MedianReleaseGapDays == nil {
		t.Fatalf("five old releases %+v", pubOld)
	}
	if rawOld.Releases30d == nil || *rawOld.Releases30d != 0 {
		t.Fatalf("admin count of old releases %+v", rawOld.Releases30d)
	}
}

func TestCorrectionRejectsEncodedContact(t *testing.T) {
	good := correctionIntake{
		Competitor: "Northwind",
		Aspect:     "Owner assembly",
		Statement:  "The public page quotes the wrong line.",
		SourceURL:  "https://northwind.example/help",
	}
	if _, _, _, _, err := correctionText(good); err != nil {
		t.Fatal(err)
	}
	bad := []correctionIntake{
		{Competitor: "reader@example.com", Aspect: good.Aspect, Statement: good.Statement},
		{Competitor: good.Competitor, Aspect: "reader@example.com", Statement: good.Statement},
		{Competitor: good.Competitor, Aspect: "reader%40example.com", Statement: good.Statement},
		{Competitor: good.Competitor, Aspect: "reader&#64;example.com", Statement: good.Statement},
		{Competitor: good.Competitor, Aspect: "reader%2540example.com", Statement: good.Statement},
		{Competitor: good.Competitor, Aspect: good.Aspect, Statement: "Write to leak@example.com about this."},
		{Competitor: good.Competitor, Aspect: good.Aspect, Statement: "Mail the desk (at) example.com today."},
		{Competitor: good.Competitor, Aspect: good.Aspect, Statement: good.Statement, SourceURL: "https://northwind.example/reader%40example.com"},
		{Competitor: good.Competitor, Aspect: good.Aspect, Statement: good.Statement, SourceURL: "https://northwind.example/help?e=reader%2540example.com"},
		{Competitor: "reader\uFF20example.com", Aspect: good.Aspect, Statement: good.Statement},
	}
	for _, in := range bad {
		if _, _, _, _, err := correctionText(in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
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

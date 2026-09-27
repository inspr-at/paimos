// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"math"
	"testing"
	"time"
)

func TestPaceCapMatchesRegisteredModels(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	now := start.Add(time.Hour)
	got, ok := paceCap("frontload", start, end, now, 1000, "0.1000")
	if !ok || got != 850 {
		t.Fatalf("frontload cap %d ok %v", got, ok)
	}
	if math.Abs(paceFraction("frontload", 0.5, 0.1)-0.85) > 1e-12 {
		t.Fatal("frontload fraction")
	}
	got, ok = paceCap("unrestricted", start, end, start, 1000, "0.1000")
	if !ok || got != 1000 {
		t.Fatalf("unrestricted cap %d", got)
	}
	if _, ok := paceCap("mystery", start, end, now, 1000, "0.1"); ok {
		t.Fatal("unknown pace model invented a cap")
	}
	if _, ok := paceCap("steady", start, end, now, 1000, "nope"); ok {
		t.Fatal("invalid burst invented a cap")
	}
}

func TestParseRange(t *testing.T) {
	now := time.Date(2026, 9, 27, 15, 4, 0, 0, time.UTC)
	from, to, err := parseRange("", "", now)
	if err != nil || !from.Equal(time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("default %s %s %v", from, to, err)
	}
	from, to, err = parseRange("2026-09-01", "2026-09-08", now)
	if err != nil || !from.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("dates %s %s %v", from, to, err)
	}
	if _, _, err = parseRange("2026-09-08", "2026-09-01", now); err == nil {
		t.Fatal("inverted range accepted")
	}
	if _, _, err = parseRange("2026-01-01", "2027-02-01", now); err == nil {
		t.Fatal("long range accepted")
	}
	if _, _, err = parseRange("2026-09-01", "", now); err == nil {
		t.Fatal("half range accepted")
	}
}

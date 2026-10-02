// SPDX-License-Identifier: AGPL-3.0-only
package lanecontrol

import (
	"testing"
	"time"
)

func TestWindowInstances(t *testing.T) {
	w := window{Timezone: "Europe/Vienna", Days: []int{1, 2, 3, 4, 5, 6, 7}, Start: "22:00", End: "06:00"}
	for _, tc := range []struct {
		at, start, end string
		open           bool
	}{
		{"2026-10-02T21:00:00Z", "2026-10-02T20:00:00Z", "2026-10-03T04:00:00Z", true},
		{"2026-10-03T03:59:59Z", "2026-10-02T20:00:00Z", "2026-10-03T04:00:00Z", true},
		{"2026-10-03T04:00:00Z", "", "", false},
		{"2026-10-25T02:30:00Z", "2026-10-24T20:00:00Z", "2026-10-25T05:00:00Z", true},
		{"2026-03-29T02:30:00Z", "2026-03-28T21:00:00Z", "2026-03-29T04:00:00Z", true},
	} {
		t.Run(tc.at, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.at)
			a, b, e := activeWindow(w, now)
			if (e == nil) != tc.open {
				t.Fatalf("open=%v err=%v", tc.open, e)
			}
			if tc.open && (a.Format(time.RFC3339) != tc.start || b.Format(time.RFC3339) != tc.end) {
				t.Fatalf("%v..%v", a, b)
			}
		})
	}
	w.Start = "02:30"
	w.End = "06:00"
	for _, at := range []string{"2026-03-29T03:00:00Z", "2026-10-25T01:45:00Z"} {
		now, _ := time.Parse(time.RFC3339, at)
		if _, _, e := activeWindow(w, now); e == nil {
			t.Fatal("ambiguous/nonexistent endpoint admitted", at)
		}
	}
}
func TestWorkingAllocations(t *testing.T) {
	for _, raw := range []string{`{"cap":1.5}`, `{"cap":0}`, `{"cap":13}`, `{"cap":2,"area":{"backend":3}}`, `{"area":{"backend":1}}`, `{"cap":2,"view":"bad"}`} {
		if _, err := ParseWorkingPreference([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	p, err := ParseWorkingPreference([]byte(`{"cap":3,"view":"area","area":{"backend":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !fitsSlots(p, []slot{{"frontend", "codex"}}, slot{"backend", "claude"}) {
		t.Fatal("reserved backend slot unavailable")
	}
	if fitsSlots(p, []slot{{"frontend", "codex"}}, slot{"frontend", "claude"}) {
		t.Fatal("stole reserved backend slot")
	}
	p.View = "model"
	p.Model = map[string]int{"claude": 2}
	if fitsSlots(p, []slot{{"backend", "codex"}}, slot{"backend", "codex"}) {
		t.Fatal("stole model allocation")
	}
	if fitsSlots(WorkingPreference{}, nil, slot{}) {
		t.Fatal("unset target allowed a launch")
	}
}

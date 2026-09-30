// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"testing"
	"time"
)

func TestNextStartUsesOwnerClockAcrossDST(t *testing.T) {
	s := DefaultSchedule("Europe/Vienna")
	loc, _ := time.LoadLocation(s.Timezone)
	for _, day := range []time.Time{time.Date(2026, 3, 27, 23, 10, 0, 0, loc), time.Date(2026, 10, 23, 23, 10, 0, 0, loc)} {
		next := s.NextStart(day, false)
		if next == nil || next.In(loc).Weekday() != time.Monday || next.In(loc).Hour() != 8 || next.In(loc).Minute() != 0 {
			t.Fatalf("after %s: %v", day, next)
		}
		if !s.WorkingAt(*next) || s.WorkingAt(day) {
			t.Fatal("work bands disagree")
		}
	}
	s.Week = make([]Day, 7)
	if next := s.NextStart(time.Now(), false); next != nil {
		t.Fatalf("disabled week: %v", next)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/recurrences"
)

// Runs under the registered dispatch behaviour test, keeping shared tier files
// untouched. Every preview is derived from one clock, with no database values.
func elapsedRecurrenceCalendar(t *testing.T) {
	for _, tc := range []struct {
		name, rule, date, clock, zone, after, first, second string
	}{
		{"quarter hour refresh", "FREQ=MINUTELY;INTERVAL=15", "2026-10-23", "09:00", "UTC", "2026-10-23T09:07:00Z", "2026-10-23T09:15:00Z", "2026-10-23T09:30:00Z"},
		{"before anchor", "FREQ=MINUTELY;INTERVAL=15", "2026-10-23", "09:00", "UTC", "2026-10-22T09:07:00Z", "2026-10-23T09:00:00Z", "2026-10-23T09:15:00Z"},
		{"hourly boundary exclusive", "FREQ=HOURLY;INTERVAL=6", "2026-10-23", "09:00", "UTC", "2026-10-23T09:00:00Z", "2026-10-23T15:00:00Z", "2026-10-23T21:00:00Z"},
		{"spring elapsed hour", "FREQ=HOURLY", "2026-03-29", "01:30", "Europe/Vienna", "2026-03-29T00:30:00Z", "2026-03-29T01:30:00Z", "2026-03-29T02:30:00Z"},
		{"fold elapsed hour", "FREQ=HOURLY", "2026-10-25", "02:30", "Europe/Vienna", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z", "2026-10-25T02:30:00Z"},
		{"long downtime", "FREQ=MINUTELY;INTERVAL=15", "1900-01-01", "09:00", "UTC", "9998-12-31T23:44:00Z", "9998-12-31T23:45:00Z", "9999-01-01T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after, err := time.Parse(time.RFC3339, tc.after)
			must(t, err)
			trigger := recurrences.Trigger{Kind: "time", RRULE: tc.rule, StartDate: tc.date, TimeOfDay: tc.clock, Timezone: tc.zone}
			got, err := recurrences.Preview(trigger, after, 2)
			must(t, err)
			if len(got) != 2 || got[0].Format(time.RFC3339) != tc.first || got[1].Format(time.RFC3339) != tc.second {
				t.Fatalf("cadence=%v, want %s, %s", got, tc.first, tc.second)
			}
		})
	}
	for _, rule := range []string{"FREQ=MINUTELY;INTERVAL=0", "FREQ=MINUTELY;INTERVAL=1441", "FREQ=MINUTELY;INTERVAL=01", "FREQ=HOURLY;INTERVAL=169", "FREQ=HOURLY;BYDAY=MO", "FREQ=MINUTELY;COUNT=2"} {
		trigger := recurrences.Trigger{Kind: "time", RRULE: rule, StartDate: "2026-10-23", TimeOfDay: "09:00", Timezone: "UTC"}
		if _, err := recurrences.Preview(trigger, time.Unix(0, 0), 2); err == nil {
			t.Fatalf("unbounded cadence accepted: %s", rule)
		}
	}
	gap := recurrences.Trigger{Kind: "time", RRULE: "FREQ=HOURLY", StartDate: "2026-03-29", TimeOfDay: "02:30", Timezone: "Europe/Vienna"}
	if _, err := recurrences.Preview(gap, time.Unix(0, 0), 2); err == nil {
		t.Fatal("nonexistent elapsed anchor accepted")
	}
}

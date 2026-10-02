// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"strings"
	"testing"
	"time"
)

func timestamp(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
func TestCalendarEdges(t *testing.T) {
	for _, tc := range []struct {
		name, rule, clock, zone, start, after string
		want                                  []string
	}{
		{"daily spring gap", "FREQ=DAILY", "02:30", "Europe/Vienna", "2026-03-27", "2026-03-28T01:30:00Z", []string{"2026-03-30T00:30:00Z", "2026-03-31T00:30:00Z"}},
		{"fold first instant only", "FREQ=DAILY", "02:30", "Europe/Vienna", "2026-10-23", "2026-10-24T00:30:00Z", []string{"2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"}},
		{"after first fold", "FREQ=DAILY", "02:30", "Europe/Vienna", "2026-10-23", "2026-10-25T00:45:00Z", []string{"2026-10-26T01:30:00Z"}},
		{"month ends", "FREQ=MONTHLY;BYMONTHDAY=31", "09:00", "UTC", "2026-01-01", "2026-01-31T09:00:00Z", []string{"2026-03-31T09:00:00Z", "2026-05-31T09:00:00Z"}},
		{"negative month end leap year", "FREQ=MONTHLY;BYMONTHDAY=-1", "09:00", "UTC", "2028-01-01", "2028-01-31T09:00:00Z", []string{"2028-02-29T09:00:00Z", "2028-03-31T09:00:00Z"}},
		{"weekday timezone", "FREQ=WEEKLY;BYDAY=MO,FR", "09:00", "America/New_York", "2026-03-01", "2026-03-06T14:00:00Z", []string{"2026-03-09T13:00:00Z", "2026-03-13T13:00:00Z"}},
		{"half hour offset", "FREQ=DAILY", "09:00", "Asia/Kolkata", "2026-01-01", "2026-01-01T00:00:00Z", []string{"2026-01-01T03:30:00Z"}},
		{"half hour DST gap", "FREQ=DAILY", "02:15", "Australia/Lord_Howe", "2026-10-01", "2026-10-03T00:00:00Z", []string{"2026-10-04T15:15:00Z"}},
		{"skipped civil day", "FREQ=DAILY", "09:00", "Pacific/Apia", "2011-12-28", "2011-12-29T19:00:00Z", []string{"2011-12-30T19:00:00Z"}},
		{"weekly default anchor", "FREQ=WEEKLY", "09:00", "UTC", "2026-10-05", "2026-10-02T12:00:00Z", []string{"2026-10-05T09:00:00Z", "2026-10-12T09:00:00Z"}},
		{"month default anchor", "FREQ=MONTHLY", "09:00", "UTC", "2026-01-31", "2026-01-31T09:00:00Z", []string{"2026-03-31T09:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trigger := Trigger{Kind: "time", RRULE: tc.rule, TimeOfDay: tc.clock, Timezone: tc.zone, StartDate: tc.start}
			got, err := Preview(trigger, timestamp(t, tc.after), len(tc.want))
			if err != nil {
				t.Fatal(err)
			}
			for i, at := range got {
				if at.Format(time.RFC3339) != tc.want[i] {
					t.Fatalf("%d: %s want %s", i, at, tc.want[i])
				}
			}
		})
	}
}
func TestRRULEValidationAndPreviewBounds(t *testing.T) {
	good := Trigger{Kind: "time", RRULE: "FREQ=DAILY", TimeOfDay: "09:00", Timezone: "UTC", StartDate: "2026-10-01"}
	for _, rule := range []string{"FREQ=YEARLY", "FREQ=DAILY;COUNT=3", "FREQ=DAILY;BYDAY=MO", "FREQ=WEEKLY;BYDAY=1MO", "FREQ=WEEKLY;BYDAY=MO,MO", "FREQ=MONTHLY;BYMONTHDAY=0", "FREQ=MONTHLY;BYMONTHDAY=32", "FREQ=MONTHLY;BYMONTHDAY=-32", "FREQ=MONTHLY;BYDAY=MO", "FREQ=WEEKLY;BYMONTHDAY=3", "FREQ=DAILY;FREQ=WEEKLY", "FREQ=DAILY;", "FREQ=DAILY;INTERVAL=2"} {
		bad := good
		bad.RRULE = rule
		if _, err := parseSchedule(bad); err == nil {
			t.Fatalf("accepted %s", rule)
		}
	}
	for _, bad := range []Trigger{{Kind: "time", RRULE: "FREQ=DAILY", TimeOfDay: "9:00", Timezone: "UTC", StartDate: "2026-10-01"}, {Kind: "time", RRULE: "FREQ=DAILY", TimeOfDay: "09:00", Timezone: "Local", StartDate: "2026-10-01"}, {Kind: "time", RRULE: "FREQ=DAILY", TimeOfDay: "09:00", Timezone: "bad/zone", StartDate: "2026-10-01"}, {Kind: "time", RRULE: "FREQ=DAILY", TimeOfDay: "09:00", Timezone: "UTC", StartDate: "2026-02-30"}} {
		if _, err := parseSchedule(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	for _, n := range []int{-1, 0, 101} {
		if _, err := Preview(good, time.Now(), n); err == nil {
			t.Fatalf("count %d", n)
		}
	}
	out, err := Preview(Trigger{Kind: "event", Event: "release.published"}, time.Now(), 5)
	if err != nil || len(out) != 0 {
		t.Fatalf("event preview %v %v", out, err)
	}
	s, err := parseSchedule(good)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.latest(timestamp(t, "2036-10-02T15:00:00Z"))
	if err != nil || latest.Format(time.RFC3339) != "2036-10-02T09:00:00Z" {
		t.Fatalf("long downtime %s %v", latest, err)
	}
}
func TestTemplateVariablesAndBounds(t *testing.T) {
	for _, s := range []string{"{{unknown}}", "{{ occurrence }}", "{{date", "oops}}"} {
		if validateVariables(s) == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	got := render("#{{occurrence}} {{date}} {{release_name}} {{release_version}}", 3, timestamp(t, "2026-10-02T23:30:00Z"), Trigger{Kind: "time", Timezone: "Europe/Vienna"}, "Sonde", "261002081219.0.0")
	if got != "#3 2026-10-03 Sonde 261002081219.0.0" {
		t.Fatal(got)
	}
	in := Input{ProjectID: "10000000-0000-4000-8000-000000000001", ParentID: "10000000-0000-4000-8000-000000000002", Template: Template{Title: "Sweep {{occurrence}}"}, Trigger: Trigger{Kind: "time", RRULE: "FREQ=DAILY", Timezone: "UTC", TimeOfDay: "09:00"}}
	if err := in.normalize(timestamp(t, "2026-10-02T12:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if in.Trigger.StartDate != "2026-10-02" || in.OverlapPolicy != "skip" || in.CatchUpPolicy != "one" {
		t.Fatalf("defaults %+v", in)
	}
	in.QueueEach = true
	if in.normalize(time.Now()) == nil {
		t.Fatal("queue without readiness")
	}
	in.QueueEach = false
	in.Template.Title = strings.Repeat("a", 513)
	if in.normalize(time.Now()) == nil {
		t.Fatal("unbounded title")
	}
}

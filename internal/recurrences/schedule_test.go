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
		{"two months", "FREQ=MONTHLY;INTERVAL=2;BYMONTHDAY=1", "09:00", "Europe/Vienna", "2026-10-02", "2026-10-02T12:00:00Z", []string{"2026-12-01T08:00:00Z", "2027-02-01T08:00:00Z"}},
		{"quarterly last day", "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1", "09:00", "UTC", "2026-10-02", "2026-10-31T09:00:00Z", []string{"2027-01-31T09:00:00Z", "2027-04-30T09:00:00Z"}},
		{"six months anchor retained", "FREQ=MONTHLY;INTERVAL=6;BYMONTHDAY=1", "09:00", "UTC", "2026-10-02", "2027-04-01T09:00:00Z", []string{"2027-10-01T09:00:00Z", "2028-04-01T09:00:00Z"}},
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

func TestMonthlyIntervalsAnchoredToStartDate(t *testing.T) {
	for _, tc := range []struct {
		name, rule, start, zone, clock, after string
		want                                  []string
	}{
		{"one month", "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=-1", "2026-01-15", "UTC", "09:00", "2026-01-31T09:00:00Z", []string{"2026-02-28T09:00:00Z", "2026-03-31T09:00:00Z"}},
		{"two months skip short month", "FREQ=MONTHLY;INTERVAL=2;BYMONTHDAY=31", "2026-12-01", "UTC", "09:00", "2026-12-31T09:00:00Z", []string{"2027-08-31T09:00:00Z", "2027-10-31T09:00:00Z"}},
		{"quarterly last day", "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1", "2026-11-15", "UTC", "09:00", "2026-11-30T09:00:00Z", []string{"2027-02-28T09:00:00Z", "2027-05-31T09:00:00Z", "2027-08-31T09:00:00Z"}},
		{"six months skip short month", "FREQ=MONTHLY;INTERVAL=6", "2026-08-31", "UTC", "09:00", "2026-08-31T09:00:00Z", []string{"2027-08-31T09:00:00Z", "2028-08-31T09:00:00Z"}},
		{"six months leap day", "FREQ=MONTHLY;INTERVAL=6;BYMONTHDAY=29", "2024-02-29", "UTC", "09:00", "2024-08-29T09:00:00Z", []string{"2025-08-29T09:00:00Z", "2026-08-29T09:00:00Z"}},
		{"quarterly gap skips whole slot", "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=29", "2025-12-01", "Europe/Vienna", "02:30", "2025-12-29T01:30:00Z", []string{"2026-06-29T00:30:00Z", "2026-09-29T00:30:00Z"}},
		{"quarterly fold uses first instant", "FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=25", "2026-07-01", "Europe/Vienna", "02:30", "2026-07-25T00:30:00Z", []string{"2026-10-25T00:30:00Z", "2027-01-25T01:30:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trigger := Trigger{Kind: "time", RRULE: tc.rule, StartDate: tc.start, Timezone: tc.zone, TimeOfDay: tc.clock}
			got, err := Preview(trigger, timestamp(t, tc.after), len(tc.want))
			if err != nil {
				t.Fatal(err)
			}
			s, err := parseSchedule(trigger)
			if err != nil {
				t.Fatal(err)
			}
			for i, at := range got {
				if at.Format(time.RFC3339) != tc.want[i] {
					t.Fatalf("%d: %s want %s", i, at, tc.want[i])
				}
				latest, err := s.latest(at.Add(time.Minute))
				if err != nil || !latest.Equal(at) {
					t.Fatalf("latest %s want %s: %v", latest, at, err)
				}
			}
		})
	}
}

func TestMonthlyIntervalValidation(t *testing.T) {
	for _, rule := range []string{"FREQ=DAILY;INTERVAL=1", "FREQ=WEEKLY;INTERVAL=1", "FREQ=WEEKLY;BYDAY=MO;INTERVAL=2", "FREQ=MONTHLY;INTERVAL=0", "FREQ=MONTHLY;INTERVAL=4", "FREQ=MONTHLY;INTERVAL=12", "FREQ=MONTHLY;INTERVAL=-1", "FREQ=MONTHLY;INTERVAL=01", "FREQ=MONTHLY;INTERVAL=1;INTERVAL=2"} {
		if _, err := parseSchedule(Trigger{Kind: "time", RRULE: rule, StartDate: "2026-01-01", Timezone: "UTC", TimeOfDay: "09:00"}); err == nil {
			t.Fatalf("accepted %s", rule)
		}
	}
}

func TestSixMonthSpringGapWalksPastOneYear(t *testing.T) {
	trigger := Trigger{Kind: "time", RRULE: "FREQ=MONTHLY;INTERVAL=6;BYMONTHDAY=31", StartDate: "2023-03-01", Timezone: "Europe/Vienna", TimeOfDay: "02:30"}
	s, err := parseSchedule(trigger)
	if err != nil {
		t.Fatal(err)
	}
	// September has no 31st, and March 31 2024 at 02:30 does not exist.
	// The next real occurrence is two years after the prior one.
	before := timestamp(t, "2023-03-31T00:30:00Z")
	after := timestamp(t, "2025-03-31T00:30:00Z")
	if next, err := s.next(before); err != nil || !next.Equal(after) {
		t.Errorf("next %s, want %s: %v", next, after, err)
	}
	if latest, err := s.latest(after.Add(-time.Minute)); err != nil || !latest.Equal(before) {
		t.Errorf("latest %s, want %s: %v", latest, before, err)
	}
	if preview, err := Preview(trigger, before, 1); err != nil || len(preview) != 1 || !preview[0].Equal(after) {
		t.Errorf("preview %v, want [%s]: %v", preview, after, err)
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
	if got := render("{{date}}", 1, timestamp(t, "2026-10-02T23:30:00Z"), Trigger{Kind: "event", EventTimezone: "Europe/Vienna"}, "", ""); got != "2026-10-03" {
		t.Fatalf("event date %s", got)
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

func TestEventMorningFollowsLocalDateAcrossDST(t *testing.T) {
	trigger := Trigger{Kind: "event", EventStart: "morning", EventTimezone: "Europe/Vienna"}
	for _, tc := range []struct{ published, want string }{
		{"2026-10-24T21:30:00Z", "2026-10-25T05:00:00Z"},
		{"2026-10-24T23:30:00Z", "2026-10-26T05:00:00Z"},
		{"2026-03-28T22:30:00Z", "2026-03-29T04:00:00Z"},
	} {
		got := eventTime(trigger, timestamp(t, tc.published))
		if got.Format(time.RFC3339) != tc.want {
			t.Fatalf("published %s: got %s, want %s", tc.published, got, tc.want)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// Risk: a fresh or legacy intent invents check failures; a future enabled start
// spins forever; launch-off or live generations receive a misleading timeout.
func TestProjectLeadWaitProjection(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	base := Lead{State: "waiting_for_room", Reason: "awaiting_generation", Revision: 1, updatedAt: now}
	for _, tc := range []struct {
		name string
		lead Lead
		want string
	}{
		{"launch off fresh", base, "automatic_launch_disabled"},
		{"launch off old", func() Lead { l := base; l.updatedAt = now.Add(-24 * time.Hour); return l }(), "automatic_launch_disabled"},
		{"legacy fresh reason", Lead{State: "waiting_for_room", Reason: "start_checks_unavailable", Revision: 1}, "automatic_launch_disabled"},
		{"real legacy failure", Lead{State: "waiting_for_room", Reason: "start_checks_unavailable", Revision: 2}, "start_checks_unavailable"},
		{"named failed check", Lead{State: "waiting_for_room", Reason: "host_unavailable", Revision: 2}, "host_unavailable"},
		{"enabled before bound", Lead{State: base.State, Reason: base.Reason, AutomaticLaunchEnabled: true, updatedAt: now.Add(-leadPickupWait + time.Nanosecond)}, "awaiting_generation"},
		{"enabled at bound", Lead{State: base.State, Reason: base.Reason, AutomaticLaunchEnabled: true, updatedAt: now.Add(-leadPickupWait)}, "runtime_pickup_timeout"},
		{"enabled future timestamp", Lead{State: base.State, Reason: base.Reason, AutomaticLaunchEnabled: true, updatedAt: now.Add(time.Minute)}, "awaiting_generation"},
		{"enabled missing timestamp", Lead{State: base.State, Reason: base.Reason, AutomaticLaunchEnabled: true}, "awaiting_generation"},
		{"already active", Lead{State: base.State, Reason: base.Reason, ProcessActive: true, AutomaticLaunchEnabled: true, updatedAt: now.Add(-24 * time.Hour)}, "awaiting_generation"},
		{"paused", Lead{State: "paused", Reason: "handover_pending"}, "handover_pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := projectLeadWait(tc.lead, now)
			if got.Reason != tc.want || got.State != tc.lead.State || got.Revision != tc.lead.Revision || got.Generation != tc.lead.Generation || got.SessionID != tc.lead.SessionID || got.AutomaticLaunchEnabled != tc.lead.AutomaticLaunchEnabled {
				t.Fatalf("projection=%+v, want reason %s with unchanged identity/policy", got, tc.want)
			}
		})
	}
}

func TestLeadAdmissionFailureRedactsAndNamesCheck(t *testing.T) {
	for _, gate := range []string{"dial", "harness", "account", "host", "private account identifier"} {
		err := fmt.Errorf("wrapped: %w", &LeadAdmissionError{Gate: gate, Err: errors.New("private diagnostic")})
		want := gate + "_unavailable"
		if gate == "private account identifier" {
			want = "admission_unavailable"
		}
		if got := leadAdmissionFailure(err); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if got := leadAdmissionFailure(errors.New("private diagnostic")); got != "admission_unavailable" {
		t.Fatal(got)
	}
}

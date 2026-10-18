// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
)

func TestProjectionStatesOwnersAndDeadlines(t *testing.T) {
	// Risk: stale head checks/review results must not carry green across episodes.
	now := time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC)
	head := "0123456789abcdef0123456789abcdef01234567"
	pr := int64(9)
	o := Observation{ID: "item", Built: true, Head: head, Settings: defaults(), At: now}
	cases := []struct {
		s       State
		who     string
		minutes int
		mutate  func(*Observation)
	}{
		{Built, "coordinator", 0, func(*Observation) {}},
		{Reviewed, "coordinator", 30, func(o *Observation) { o.ReviewedHead = head }},
		{Pushed, "ci", 60, func(o *Observation) { o.PR = &pr; o.Open = true }},
		{CIGreen, "coordinator", 20, func(o *Observation) {
			for _, n := range *o.Settings.RequiredChecks {
				o.Checks = append(o.Checks, Check{Name: n, Status: "completed", Conclusion: "success"})
			}
		}},
		{InQueue, "queue", 60, func(o *Observation) { o.Queued = true }},
		{QueueFailed, "builder", 120, func(o *Observation) { o.QueueFailure = true; o.Queued = false }},
		{Held, "person", 0, func(o *Observation) { reason := "Release freeze"; o.HoldReason = &reason }},
		{Merged, "", 0, func(o *Observation) { o.Merged = true }},
	}
	var previous *Item
	for _, tc := range cases {
		t.Run(string(tc.s), func(t *testing.T) {
			tc.mutate(&o)
			o.At = o.At.Add(time.Minute)
			i := project(o, previous)
			if i.State != tc.s || i.Owner != tc.who || !i.Since.Equal(o.At) {
				t.Fatalf("bad projection: %+v", i)
			}
			if tc.minutes == 0 {
				if i.Deadline != nil {
					t.Fatal("unexpected deadline")
				}
			} else if i.Deadline == nil || !i.Deadline.Equal(o.At.Add(time.Duration(tc.minutes)*time.Minute)) {
				t.Fatalf("bad deadline: %v", i.Deadline)
			}
			previous = &i
		})
	}
	// Fresh head observations arrive with checks for that head only.
	o.Merged = false
	o.QueueFailure = false
	o.HoldReason = nil
	o.Head = "abcdef0123456789abcdef0123456789abcdef0123"
	o.Checks = nil
	o.At = o.At.Add(time.Minute)
	if i := project(o, previous); i.State != Reviewed {
		t.Fatalf("old review must require push: %s", i.State)
	}
	o.ReviewedHead = ""
	o.Checks = []Check{{Name: "go", Status: "completed", Conclusion: "success"}}
	if allSuccess(o) {
		t.Fatal("missing required contexts granted green")
	}
}
func TestProjectionReplayAndInheritedSettings(t *testing.T) {
	settings := effective(defaults(), Settings{Deadlines: map[State]int{Reviewed: 2}})
	o := Observation{ID: "row", Head: "head", ReviewedHead: "head", Settings: settings, At: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)}
	first := project(o, nil)
	o.At = o.At.Add(time.Minute)
	second := project(o, &first)
	if !first.Since.Equal(second.Since) || !first.Deadline.Equal(*second.Deadline) {
		t.Fatal("duplicate facts extended deadline")
	}
	got := project(o, func() *Item { i := project(first.Observation, nil); return &i }())
	if !reflect.DeepEqual(second, got) {
		t.Fatal("normalized replay changed projection")
	}
	if settings.Deadlines[Pushed] != 60 || settings.Deadlines[Reviewed] != 2 {
		t.Fatal("override discarded tenant defaults")
	}
}
func TestDeliveryManageExplicitAgentGrant(t *testing.T) {
	read, ok := authz.Lookup("delivery.read")
	if !ok || !read.AgentGrantable {
		t.Fatal("delivery.read unavailable to agents")
	}
	manage, ok := authz.Lookup("delivery.manage")
	if !ok || !manage.AgentGrantable {
		t.Fatal("explicit agent grant unavailable")
	}
	// The exclusion file is consumed by the real built-in agent ceiling.
	// Integration tests below check the effective permission of an admin agent.
}
func TestTicketHintsOnlyCanonicalPrefixes(t *testing.T) {
	for _, tc := range []struct{ title, branch, key string }{{"AEON-848: delivery", "work/aeon-848-delivery", "AEON-848"}, {"Mentions AEON-848 later", "feature/fix", ""}, {"AEON-848x: no", "work/aeon-848x-no", ""}} {
		if keyFromTitle(tc.title) != tc.key || keyFromBranch(tc.branch) != tc.key {
			t.Fatalf("wrong ticket hint: %+v", tc)
		}
	}
}

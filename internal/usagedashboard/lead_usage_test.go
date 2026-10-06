// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import "testing"

func TestLeadUsageStableAttemptsAndReplay(t *testing.T) {
	a := newLeadAccumulator()
	run, session := "run-1", "session-1"
	tokens, active, wait := "30", "100", "5"
	e := leadEvidence{id: "episode-1/segment-1", session: session, run: &run, tokens: &tokens, active: &active, waiting: &wait}
	for range 3 {
		if err := a.add(e); err != nil {
			t.Fatal(err)
		}
	}
	e.id = "episode-2/segment-2"
	tokens = "7"
	if err := a.add(e); err != nil {
		t.Fatal(err)
	}
	if a.totals.Attempts != 1 || a.totals.Contributions != 2 || *a.totals.Tokens.Measured != "37" {
		t.Fatalf("replay/segments: %+v", a.totals)
	}
	retry := "run-2"
	e.id = "episode-2/retry"
	e.run = &retry
	e.tokens = nil
	e.active = nil
	if err := a.add(e); err != nil {
		t.Fatal(err)
	}
	if a.totals.Attempts != 2 || a.totals.Tokens.Unknown != 1 || *a.totals.Tokens.Measured != "37" || a.totals.Active.Unknown != 1 {
		t.Fatalf("retry/unknown: %+v", a.totals)
	}
}
func TestLeadUsageCounterRejectsInvalidEvidence(t *testing.T) {
	for _, raw := range []string{"-1", "1.5", "bogus"} {
		var m LeadUsageMeasure
		if err := m.add(&raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	raw := "99999999999999999999999999"
	var m LeadUsageMeasure
	if err := m.add(&raw); err != nil {
		t.Fatal(err)
	}
	if err := m.add(&raw); err != nil {
		t.Fatal(err)
	}
	if *m.Measured != "199999999999999999999999998" || m.Known != 2 {
		t.Fatalf("large counter %+v", m)
	}
}

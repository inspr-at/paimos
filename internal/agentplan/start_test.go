// SPDX-License-Identifier: AGPL-3.0-only
package agentplan

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// Risk: exhaustion, missing readings or a stale snapshot authorize a new start
// or silently choose a successor despite the person's wait setting.
func TestDailyStartDecisionsFailClosedAndRespectLadderOrWait(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	end := now.Add(12 * time.Hour)
	reset := end.Add(5 * 24 * time.Hour)
	fresh := DailyAccount{AccountID: "first", UsedPct: Number(49), LimitUsedPct: Number(50), Freshness: "fresh", ReadAt: &now, ResetsAt: &reset}
	for _, tc := range []struct {
		name, setting, reason string
		ladder                bool
		edit                  func(*Snapshot)
	}{
		{"under pace", "ladder", "", false, nil},
		{"at limit ladder", "ladder", "daily_limit", true, func(s *Snapshot) { s.DailyState["codex"].Accounts[0].UsedPct = Number(50) }},
		{"at limit wait", "wait", "daily_limit", false, func(s *Snapshot) { s.DailyState["codex"].Accounts[0].UsedPct = Number(51) }},
		{"stale reading", "ladder", "daily_limit_unknown", false, func(s *Snapshot) {
			at := now.Add(-DailyFreshness - time.Nanosecond)
			s.DailyState["codex"].Accounts[0].ReadAt = &at
		}},
		{"future reading", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { at := now.Add(time.Nanosecond); s.DailyState["codex"].Accounts[0].ReadAt = &at }},
		{"unreadable", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { s.DailyState = nil }},
		{"malformed setting", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { d := s.Daily["codex"]; d.AtLimit = "skip"; s.Daily["codex"] = d }},
		{"expired local day", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { s.DailyUntil = now }},
		{"expired window", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { s.DailyState["codex"].Accounts[0].ResetsAt = &now }},
		{"missing limit", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { s.DailyState["codex"].Accounts[0].LimitUsedPct = nil }},
		{"nonfinite limit", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { s.DailyState["codex"].Accounts[0].LimitUsedPct = Number(math.NaN()) }},
		{"private", "ladder", "daily_limit_unknown", false, func(s *Snapshot) { s.DailyState["codex"].Accounts[0].DetailsRedacted = true }},
		{"mixed unknown exhausted", "ladder", "daily_limit_unknown", false, func(s *Snapshot) {
			s.DailyState["codex"].Accounts[0].UsedPct = Number(50)
			state := s.DailyState["codex"]
			state.Accounts = append(state.Accounts, DailyAccount{AccountID: "unknown"})
			s.DailyState["codex"] = state
		}},
		{"second door", "wait", "", false, func(s *Snapshot) {
			first := fresh
			first.UsedPct = Number(50)
			state := s.DailyState["codex"]
			state.Accounts = append([]DailyAccount{first}, state.Accounts...)
			s.DailyState["codex"] = state
		}},
		{"API door", "ladder", "", false, func(s *Snapshot) {
			s.DailyState["codex"] = DailyState{State: "no_limit", Accounts: []DailyAccount{{AccountID: "api"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := DefaultDaily()
			d.AtLimit = tc.setting
			s := Snapshot{Plan: Plan{Total: 5, Daily: map[string]DailySettings{"codex": d}}, DailyUntil: end, DailyState: map[string]DailyState{"codex": {State: "on_pace", Accounts: []DailyAccount{fresh}}}}
			if tc.edit != nil {
				tc.edit(&s)
			}
			got := DailyStart(s, "codex", now)
			if got.Reason != tc.reason || got.FollowLadder != tc.ladder {
				t.Fatalf("decision=%+v want reason=%s ladder=%v", got, tc.reason, tc.ladder)
			}
			ok, reason := CanStart(s.Plan, nil, "codex", got)
			if ok != (tc.reason == "") || reason != tc.reason {
				t.Fatalf("start=%v reason=%s", ok, reason)
			}
			if tc.reason == "daily_limit" && (got.Until == nil || !got.Until.Equal(end)) {
				t.Fatal("wait lost local midnight")
			}
		})
	}
	if ok, reason := CanStart(Plan{Total: 5, Daily: map[string]DailySettings{"codex": DefaultDaily()}}, nil, "codex"); ok || reason != "daily_limit_unknown" {
		t.Fatal("settings alone became authority")
	}
	// Guard clients receive the wire projection; a no-limit API account must
	// survive round-trip without relying on a Go-only discriminator.
	s := Snapshot{Plan: Default(), DailyUntil: end, DailyState: map[string]DailyState{"cursor": {State: "on_pace", Accounts: []DailyAccount{{AccountID: "subscription"}, {AccountID: "api", NoDailyLimit: true}}}}}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var wire Snapshot
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if got := DailyStart(wire, "cursor", now); got.Reason != "" {
		t.Fatal(got)
	}
}

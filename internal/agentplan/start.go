// SPDX-License-Identifier: AGPL-3.0-only
package agentplan

import "time"

const DailyFreshness = 2 * time.Minute

// DailyDecision is a refusal to start the requested harness, never a launch
// grant for a successor. Models must qualify that successor independently.
type DailyDecision struct {
	Reason       string
	FollowLadder bool
	Until        *time.Time
}

// DailyAccountReason uses the same precise reading and ceiling as the dial.
// Unknown, redacted, future and expired observations never imply headroom.
func DailyAccountReason(a DailyAccount, now time.Time) string {
	if now.IsZero() || a.DetailsRedacted {
		return "daily_limit_unknown"
	}
	if a.NoDailyLimit {
		return ""
	}
	if a.Freshness != "fresh" || a.ReadAt == nil || a.ReadAt.After(now) || now.Sub(*a.ReadAt) > DailyFreshness || a.ResetsAt == nil || !now.Before(*a.ResetsAt) || a.UsedPct == nil || a.LimitUsedPct == nil || !percent(*a.UsedPct) || !percent(*a.LimitUsedPct) {
		return "daily_limit_unknown"
	}
	if *a.UsedPct >= *a.LimitUsedPct {
		return "daily_limit"
	}
	return ""
}

// DailyStart checks all doors. One fresh door below its limit is sufficient;
// unknown doors cannot turn exhaustion into an instruction to switch models.
func DailyStart(s Snapshot, harness string, now time.Time) DailyDecision {
	unknown := DailyDecision{Reason: "daily_limit_unknown"}
	if s.Plan.Validate() != nil || HarnessLabel(harness) == "" || now.IsZero() || !s.DailyUntil.After(now) {
		return unknown
	}
	state, ok := s.DailyState[harness]
	if !ok || len(state.Accounts) == 0 || len(state.Accounts) > 1024 {
		return unknown
	}
	switch state.State {
	case "on_pace", "over_pace", "at_limit", "no_limit":
	default:
		return unknown
	}
	missing := false
	for _, a := range state.Accounts {
		// API-billed doors carry no numbers in the wire projection. Its
		// no_limit discriminant is only emitted for visible API accounts.
		if state.State == "no_limit" && !a.DetailsRedacted && a.UsedPct == nil && a.LimitUsedPct == nil {
			a.NoDailyLimit = true
		}
		switch DailyAccountReason(a, now) {
		case "":
			return DailyDecision{}
		case "daily_limit_unknown":
			missing = true
		}
	}
	if missing {
		return unknown
	}
	until := s.DailyUntil
	return DailyDecision{Reason: "daily_limit", FollowLadder: s.DailySetting(harness).AtLimit == "ladder", Until: &until}
}

// SPDX-License-Identifier: AGPL-3.0-only

package goals

import (
	"math"
	"slices"
)

// StateFacts contains facts for distinct eligible work leaves only. Reference
// nodes and hidden outcomes never enter this input.
type StateFacts struct {
	SessionIDs  []string
	HandedOver  bool
	TotalLeaves int
	DoneLeaves  int
	StateSet    State
	OpenPR      bool
	Review      bool
	Paused      bool
	Queued      bool
	Blocked     bool
}

type EffectiveState struct {
	State      State    `json:"state"`
	Reason     string   `json:"reason,omitempty"`
	SessionIDs []string `json:"session_ids"`
}

// DeriveState applies D4 in order: a live worker beats explicit queued, done,
// and handed-over; a non-empty all-done leaf set beats stale explicit moving.
func DeriveState(facts StateFacts) EffectiveState {
	sessions := slices.Clone(facts.SessionIDs)
	slices.Sort(sessions)
	sessions = slices.DeleteFunc(slices.Compact(sessions), func(id string) bool { return id == "" })
	result := EffectiveState{SessionIDs: sessions}
	if result.SessionIDs == nil {
		result.SessionIDs = []string{}
	}
	switch {
	case len(sessions) > 0:
		result.State = Moving
	case facts.HandedOver || facts.TotalLeaves > 0 && facts.DoneLeaves == facts.TotalLeaves:
		result.State = Done
	case facts.StateSet.valid():
		result.State = facts.StateSet
	case facts.OpenPR || facts.Review:
		result.State = Moving
	case facts.Paused:
		result.State = Paused
	default:
		result.State = Queued
		switch {
		case facts.Queued:
			result.Reason = "start_queue"
		case facts.Blocked:
			result.Reason = "blocked"
		default:
			result.Reason = "waiting"
		}
	}
	return result
}

type Coverage struct {
	EstimatedLeaves int `json:"estimated_leaves"`
	TotalLeaves     int `json:"total_leaves"`
}

type LeafEstimate struct {
	ID    string
	Hours float64 // zero, non-finite and out-of-range values are unestimated
}

// ProgressOutcome is one visible outcome and its eligible work leaves. A leaf
// may occur in several outcomes; duplicate roots within one outcome count once.
type ProgressOutcome struct {
	State  State
	Leaves []LeafEstimate
}

type OutcomeWeight struct {
	Coverage Coverage `json:"coverage"`
	Hours    float64  `json:"weight"`
}

type Rollup struct {
	Pct                     int             `json:"pct"`
	Basis                   string          `json:"basis"`
	Total                   int             `json:"total"`
	Done                    int             `json:"done"`
	ItemsWithoutEstimate    int             `json:"items_without_estimate"`
	ItemsPartiallyEstimated int             `json:"items_partially_estimated"`
	Outcomes                []OutcomeWeight `json:"outcomes"`
}

// ComputeRollup implements concept §5. Partially estimated outcomes get only
// known shared hours, never a mean for their unknown leaves. Only wholly
// unestimated outcomes receive the mean weight (or 1 when no hours exist).
// The result preserves the input order. SQL readers must enforce the work-scope
// budget before constructing these visibility-filtered inputs.
func ComputeRollup(outcomes []ProgressOutcome) Rollup {
	result := Rollup{Basis: "count", Total: len(outcomes), Outcomes: make([]OutcomeWeight, len(outcomes))}
	shares := map[string]int{}
	leaves := make([][]LeafEstimate, len(outcomes))
	for i, outcome := range outcomes {
		seen := map[string]bool{}
		for _, leaf := range outcome.Leaves {
			if seen[leaf.ID] {
				continue
			}
			seen[leaf.ID] = true
			shares[leaf.ID]++
			leaves[i] = append(leaves[i], leaf)
		}
	}
	knownWeight, estimatedOutcomes := 0.0, 0
	for i, outcome := range outcomes {
		weight := &result.Outcomes[i]
		weight.Coverage.TotalLeaves = len(leaves[i])
		for _, leaf := range leaves[i] {
			if leaf.Hours > 0 && leaf.Hours <= 200 && !math.IsNaN(leaf.Hours) {
				weight.Coverage.EstimatedLeaves++
				weight.Hours += leaf.Hours / float64(shares[leaf.ID])
			}
		}
		if outcome.State == Done {
			result.Done++
		}
		if weight.Coverage.EstimatedLeaves == 0 {
			result.ItemsWithoutEstimate++
		} else {
			estimatedOutcomes++
			knownWeight += weight.Hours
			if weight.Coverage.EstimatedLeaves < weight.Coverage.TotalLeaves {
				result.ItemsPartiallyEstimated++
			}
		}
	}
	mean := 1.0
	if estimatedOutcomes > 0 {
		mean = knownWeight / float64(estimatedOutcomes)
		result.Basis = "estimate"
		if result.ItemsWithoutEstimate > 0 || result.ItemsPartiallyEstimated > 0 {
			result.Basis = "mixed"
		}
	}
	totalWeight, doneWeight := 0.0, 0.0
	for i := range result.Outcomes {
		weight := &result.Outcomes[i]
		if weight.Coverage.EstimatedLeaves == 0 {
			weight.Hours = mean
		}
		totalWeight += weight.Hours
		if outcomes[i].State == Done {
			doneWeight += weight.Hours
		}
	}
	if totalWeight > 0 {
		result.Pct = int(math.Round(100 * doneWeight / totalWeight))
	}
	return result
}

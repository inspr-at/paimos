// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package deliverytrust

import (
	"fmt"
	"time"
)

type landingAnalysis struct {
	contributors   []Contributor
	diagnostics    []StageDiagnostic
	minimumSeconds int64
	maximumSeconds int64
	pointSeconds   *int64
	confidence     float64
	label          ConfidenceLabel
	rangeOnly      bool
	failure        SuppressionCode
	flags          []Flag
	transitions    []time.Time
}

func analyzeLanding(
	input Input,
	currentIndex int,
	estimates []estimateAnalysis,
) (landingAnalysis, []HistoryResult, error) {
	result := landingAnalysis{label: ConfidenceUnknown}
	histories := make([]HistoryResult, len(input.Stages))
	allPoints := true
	hasContributor := false
	confidenceSet := false
	disagreement := false

	for i := currentIndex; i < len(input.Stages); i++ {
		if !input.Policy[i].Required || input.Stages[i].Completion.Eligible {
			continue
		}
		stage := input.Stages[i]
		var executionStarted *time.Time
		if i == currentIndex {
			executionStarted = stage.ExecutionStartedAt
		}
		history, err := analyzeHistory(stage.History, HistoryOptions{
			ProjectIdentity:  input.ProjectIdentity,
			Stage:            stage.Stage,
			PolicyVersion:    input.PolicyVersion,
			CalculatedAt:     input.CalculatedAt,
			ExecutionStarted: executionStarted,
		})
		if err != nil {
			return landingAnalysis{}, nil, err
		}
		histories[i] = history
		result.transitions = append(result.transitions, history.nextTransitions...)

		var owner *Contributor
		if i == currentIndex {
			owner = estimates[i].rangeContributor
			result.transitions = append(result.transitions, estimates[i].nextTransitions...)
		}
		contributor, mergedDisagreement := mergeContributor(stage.Stage, i == currentIndex, owner, history)
		diagnostic := StageDiagnostic{
			Stage: stage.Stage, CurrentStage: i == currentIndex,
			Covered:             contributor != nil,
			RawSampleCount:      history.RawSampleCount,
			InlierSampleCount:   history.InlierSampleCount,
			RejectedSampleCount: history.RejectedSampleCount,
			Flags:               append([]Flag{}, history.Flags...),
		}
		if contributor == nil {
			for _, flag := range history.Flags {
				result.flags = appendUniqueFlag(result.flags, flag)
			}
			cause := history.Failure
			if history.RawSampleCount == 0 {
				cause = SuppressMissingContributor
			}
			if i == currentIndex && estimates[i].expired {
				cause = SuppressEstimateExpired
			}
			if cause == "" {
				cause = SuppressMissingContributor
			}
			diagnostic.Failure = cause
			result.diagnostics = append(result.diagnostics, diagnostic)
			result.failure = preferredSuppression(result.failure, cause)
			continue
		}
		for _, flag := range contributor.Flags {
			diagnostic.Flags = appendUniqueFlag(diagnostic.Flags, flag)
		}
		result.diagnostics = append(result.diagnostics, diagnostic)

		hasContributor = true
		disagreement = disagreement || mergedDisagreement
		result.contributors = append(result.contributors, *contributor)
		for _, flag := range contributor.Flags {
			result.flags = appendUniqueFlag(result.flags, flag)
		}
		if total, ok := checkedAdd(result.minimumSeconds, contributor.MinimumRemainingSeconds); ok {
			result.minimumSeconds = total
		} else {
			return landingAnalysis{}, nil, fmt.Errorf("%w: optimistic bound overflow", ErrInvalidInput)
		}
		if total, ok := checkedAdd(result.maximumSeconds, contributor.MaximumRemainingSeconds); ok {
			result.maximumSeconds = total
		} else {
			return landingAnalysis{}, nil, fmt.Errorf("%w: pessimistic bound overflow", ErrInvalidInput)
		}
		if contributor.PointRemainingSeconds == nil {
			allPoints = false
		} else if result.pointSeconds == nil {
			value := *contributor.PointRemainingSeconds
			result.pointSeconds = &value
		} else if total, ok := checkedAdd(*result.pointSeconds, *contributor.PointRemainingSeconds); ok {
			*result.pointSeconds = total
		} else {
			return landingAnalysis{}, nil, fmt.Errorf("%w: point bound overflow", ErrInvalidInput)
		}

		if !confidenceSet {
			result.confidence = contributor.Confidence
			result.label = contributor.ConfidenceLabel
			confidenceSet = true
		} else {
			result.confidence, result.label = weakerConfidence(
				result.confidence, result.label,
				contributor.Confidence, contributor.ConfidenceLabel,
			)
		}
	}

	if !hasContributor || !allPoints {
		result.pointSeconds = nil
	}
	result.rangeOnly = result.label == ConfidenceLow || result.label == ConfidenceUnknown || result.pointSeconds == nil || disagreement
	if result.rangeOnly {
		result.pointSeconds = nil
	}
	return result, histories, nil
}

func mergeContributor(
	stage StageKey,
	current bool,
	owner *Contributor,
	history HistoryResult,
) (*Contributor, bool) {
	if owner == nil && !history.Eligible {
		return nil, false
	}
	if owner == nil {
		return historyContributor(stage, current, history), false
	}

	merged := *owner
	merged.Stage = stage
	merged.CurrentStage = current
	if !history.Eligible {
		for _, flag := range history.Flags {
			merged.Flags = appendUniqueFlag(merged.Flags, flag)
		}
		return &merged, false
	}

	merged.Kind = ContributorAgentHistory
	merged.RawSampleCount = history.RawSampleCount
	merged.InlierSampleCount = history.InlierSampleCount
	merged.RejectedSampleCount = history.RejectedSampleCount
	components := history.Components
	merged.ComponentMedians = &components
	for _, flag := range history.Flags {
		merged.Flags = appendUniqueFlag(merged.Flags, flag)
	}
	ownerMinimum := owner.MinimumRemainingSeconds
	ownerMaximum := owner.MaximumRemainingSeconds
	if history.MinimumSeconds < merged.MinimumRemainingSeconds {
		merged.MinimumRemainingSeconds = history.MinimumSeconds
	}
	if history.MaximumSeconds > merged.MaximumRemainingSeconds {
		merged.MaximumRemainingSeconds = history.MaximumSeconds
	}

	disjoint := ownerMaximum < history.MinimumSeconds || history.MaximumSeconds < ownerMinimum
	if disjoint {
		merged.PointRemainingSeconds = nil
		merged.Confidence, merged.ConfidenceLabel = weakerConfidence(
			owner.Confidence, owner.ConfidenceLabel,
			history.Confidence, history.ConfidenceLabel,
		)
		merged.Confidence, merged.ConfidenceLabel = downgradeConfidence(merged.Confidence, merged.ConfidenceLabel)
		merged.Flags = appendUniqueFlag(merged.Flags, FlagAgentHistoryDisagreement)
		return &merged, true
	}

	// Overlap retains the owner's confidence and point. A history point is
	// adopted only if it lies inside the owner's original range; no midpoint is
	// ever synthesized.
	if owner.PointRemainingSeconds == nil &&
		history.PointSeconds >= ownerMinimum && history.PointSeconds <= ownerMaximum {
		point := history.PointSeconds
		merged.PointRemainingSeconds = &point
	}
	return &merged, false
}

func historyContributor(stage StageKey, current bool, history HistoryResult) *Contributor {
	point := history.PointSeconds
	components := history.Components
	return &Contributor{
		Stage:                   stage,
		Kind:                    ContributorHistory,
		CurrentStage:            current,
		Confidence:              history.Confidence,
		ConfidenceLabel:         history.ConfidenceLabel,
		MinimumRemainingSeconds: history.MinimumSeconds,
		MaximumRemainingSeconds: history.MaximumSeconds,
		PointRemainingSeconds:   &point,
		RawSampleCount:          history.RawSampleCount,
		InlierSampleCount:       history.InlierSampleCount,
		RejectedSampleCount:     history.RejectedSampleCount,
		ComponentMedians:        &components,
		Flags:                   append([]Flag(nil), history.Flags...),
	}
}

func preferredSuppression(current, candidate SuppressionCode) SuppressionCode {
	if current == "" || suppressionRank(candidate) < suppressionRank(current) {
		return candidate
	}
	return current
}

func suppressionRank(code SuppressionCode) int {
	switch code {
	case SuppressTerminalComplete:
		return 0
	case SuppressCancelled:
		return 1
	case SuppressTerminalFailed:
		return 2
	case SuppressWaitingOnHuman:
		return 3
	case SuppressBlocked:
		return 4
	case SuppressStale:
		return 5
	case SuppressUnknownReporter:
		return 6
	case SuppressNoSignal:
		return 7
	case SuppressEstimateExpired:
		return 8
	case SuppressOutlierHeavy:
		return 9
	case SuppressInsufficientBasis:
		return 10
	case SuppressMissingContributor:
		return 11
	default:
		return 12
	}
}

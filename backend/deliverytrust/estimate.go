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
	"math"
	"sort"
	"time"
)

type estimateAnalysis struct {
	latest              *EstimateFact
	latestProgressFact  *EstimateFact
	latestAttribution   *SourceAttribution
	maxProgress         *float64
	maxProgressFact     *EstimateFact
	progressAttribution *SourceAttribution
	backslide           bool
	rangeContributor    *Contributor
	expired             bool
	invalidLatest       bool
	nextTransitions     []time.Time
}

func analyzeEstimates(stage StageInput, calculatedAt time.Time) (estimateAnalysis, error) {
	var analysis estimateAnalysis
	current := make([]EstimateFact, 0, len(stage.Estimates))
	authorityMetadata := map[authorityEpochKey]reporterMetadata{
		authorityKey(stage.Scope): {
			Reporter: stage.Reporter, ReporterID: stage.Scope.ReporterID, RunLinkID: stage.Scope.RunLinkID,
		},
	}
	for _, fact := range stage.Estimates {
		if err := validateEstimateFact(fact); err != nil {
			return estimateAnalysis{}, err
		}
		key := authorityKey(fact.Scope)
		metadata := reporterMetadata{
			Reporter: fact.Reporter, ReporterID: fact.Scope.ReporterID, RunLinkID: fact.Scope.RunLinkID,
		}
		if existing, ok := authorityMetadata[key]; ok && existing != metadata {
			return estimateAnalysis{}, fmt.Errorf("%w: reporter metadata changed without a new authority epoch", ErrInvalidInput)
		}
		authorityMetadata[key] = metadata
		if fact.ServerReceivedAt.After(calculatedAt) {
			return estimateAnalysis{}, fmt.Errorf("%w: estimate arrives after calculation", ErrInvalidInput)
		}
		if (stage.Reporter == ReporterAgentRun || stage.Reporter == ReporterExternal) &&
			fact.Reporter == stage.Reporter && scopeMatches(fact.Scope, stage.Scope) {
			current = append(current, fact)
		}
	}
	if len(current) == 0 {
		return analysis, nil
	}
	sort.Slice(current, func(i, j int) bool {
		if current[i].Revision != current[j].Revision {
			return current[i].Revision < current[j].Revision
		}
		if current[i].Sequence != current[j].Sequence {
			return current[i].Sequence < current[j].Sequence
		}
		return current[i].Identity < current[j].Identity
	})
	latest := current[len(current)-1]
	analysis.latest = &latest
	invalidLatestProgress := latest.ProgressPercent != nil && !validProgressPercent(*latest.ProgressPercent)
	if baseEstimateEligible(latest) {
		analysis.latestAttribution = attribution(stage.Reporter, latest)
	} else {
		analysis.invalidLatest = true
	}
	if invalidLatestProgress {
		analysis.invalidLatest = true
		analysis.latestAttribution = nil
	}

	var latestProgress *EstimateFact
	for i := range current {
		fact := current[i]
		if !progressFactEligible(fact) {
			continue
		}
		latestProgress = &fact
		if analysis.maxProgress == nil || *fact.ProgressPercent > *analysis.maxProgress ||
			(*fact.ProgressPercent == *analysis.maxProgress && newerFact(fact, *analysis.maxProgressFact)) {
			value := *fact.ProgressPercent
			analysis.maxProgress = &value
			copy := fact
			analysis.maxProgressFact = &copy
		}
	}
	if latestProgress != nil && analysis.maxProgress != nil && *latestProgress.ProgressPercent < *analysis.maxProgress {
		analysis.backslide = true
	}
	if latestProgress != nil {
		copy := *latestProgress
		analysis.latestProgressFact = &copy
	}
	if analysis.maxProgressFact != nil {
		analysis.progressAttribution = attribution(stage.Reporter, *analysis.maxProgressFact)
	}

	if invalidLatestProgress || !baseEstimateEligible(latest) || latest.ETA == nil {
		return analysis, nil
	}
	relative, optimistic, pessimistic, point, valid, expired := anchoredRange(latest, calculatedAt)
	if !valid {
		analysis.invalidLatest = true
		return analysis, nil
	}
	analysis.expired = expired
	if optimistic.After(calculatedAt) {
		analysis.nextTransitions = append(analysis.nextTransitions, optimistic)
	}
	if point != nil && point.After(calculatedAt) {
		analysis.nextTransitions = append(analysis.nextTransitions, *point)
	}
	if pessimistic.After(calculatedAt) {
		analysis.nextTransitions = append(analysis.nextTransitions, pessimistic)
	}
	if expired {
		return analysis, nil
	}
	contributor := Contributor{
		Stage: stage.Stage, Kind: ContributorAgent, CurrentStage: true,
		Source:     analysis.latestAttribution,
		Confidence: latest.Confidence, ConfidenceLabel: analysis.latestAttribution.Label,
		MinimumRemainingSeconds: relative.MinimumSeconds,
		MaximumRemainingSeconds: relative.MaximumSeconds,
		PointRemainingSeconds:   relative.PointSeconds,
	}
	if point == nil {
		contributor.PointRemainingSeconds = nil
	}
	analysis.rangeContributor = &contributor
	return analysis, nil
}

func validateEstimateUniqueness(stages []StageInput) error {
	seenIdentities := make(map[string]bool)
	seenOrderKeys := make(map[estimateOrderKey]bool)
	for _, stage := range stages {
		for _, fact := range stage.Estimates {
			if seenIdentities[fact.Identity] {
				return fmt.Errorf("%w: duplicate estimate identity", ErrInvalidInput)
			}
			seenIdentities[fact.Identity] = true
			key := estimateOrderKey{
				Reporter: fact.Reporter, Scope: fact.Scope,
				Revision: fact.Revision, Sequence: fact.Sequence,
			}
			if seenOrderKeys[key] {
				return fmt.Errorf("%w: duplicate estimate order key", ErrInvalidInput)
			}
			seenOrderKeys[key] = true
		}
	}
	return nil
}

type estimateOrderKey struct {
	Reporter ReporterKind
	Scope    Scope
	Revision uint64
	Sequence uint64
}

type authorityEpochKey struct {
	AttemptID   string
	PlanID      string
	ExecutionID string
	AuthorityID string
}

type reporterMetadata struct {
	Reporter   ReporterKind
	ReporterID string
	RunLinkID  string
}

func authorityKey(scope Scope) authorityEpochKey {
	return authorityEpochKey{
		AttemptID: scope.AttemptID, PlanID: scope.PlanID,
		ExecutionID: scope.ExecutionID, AuthorityID: scope.AuthorityID,
	}
}

func validateEstimateFact(fact EstimateFact) error {
	if !validOpaque(fact.Identity) || fact.Revision == 0 || fact.Sequence == 0 || fact.ServerReceivedAt.IsZero() {
		return fmt.Errorf("%w: malformed estimate identity", ErrInvalidInput)
	}
	if fact.ProgressPercent == nil && fact.ETA == nil {
		return fmt.Errorf("%w: estimate fact carries no value", ErrInvalidInput)
	}
	if err := validateScope(fact.Scope, fact.Reporter, true); err != nil {
		return err
	}
	if err := validateSafeBasis(fact.Basis); err != nil {
		return err
	}
	return nil
}

func baseEstimateEligible(fact EstimateFact) bool {
	sourceEligible := false
	switch fact.Reporter {
	case ReporterAgentRun:
		sourceEligible = fact.Source == SourceAgent || fact.Source == SourceAdapter ||
			fact.Source == SourceProvider || fact.Source == SourceTool
	case ReporterExternal:
		sourceEligible = fact.Source == SourceExternal
	}
	if !sourceEligible {
		return false
	}
	if fact.Basis == "" {
		return false
	}
	_, ok := classifySourceConfidence(fact.Confidence)
	return ok
}

func progressFactEligible(fact EstimateFact) bool {
	if !baseEstimateEligible(fact) || fact.ProgressPercent == nil {
		return false
	}
	return validProgressPercent(*fact.ProgressPercent)
}

func validProgressPercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func newerFact(a, b EstimateFact) bool {
	if a.Revision != b.Revision {
		return a.Revision > b.Revision
	}
	if a.Sequence != b.Sequence {
		return a.Sequence > b.Sequence
	}
	return a.Identity > b.Identity
}

func attribution(reporter ReporterKind, fact EstimateFact) *SourceAttribution {
	label, ok := classifySourceConfidence(fact.Confidence)
	if !ok {
		return nil
	}
	return &SourceAttribution{
		Identity: fact.Identity, ReporterKind: reporter, Source: fact.Source, Revision: fact.Revision,
		Sequence: fact.Sequence, Confidence: fact.Confidence, Label: label, Basis: fact.Basis,
	}
}

func anchoredRange(fact EstimateFact, calculatedAt time.Time) (EstimateRange, time.Time, time.Time, *time.Time, bool, bool) {
	if fact.ETA == nil {
		return EstimateRange{}, time.Time{}, time.Time{}, nil, false, false
	}
	rangeValue := *fact.ETA
	if rangeValue.MinimumSeconds < 0 || rangeValue.MaximumSeconds < 0 ||
		rangeValue.MinimumSeconds > MaxETASeconds || rangeValue.MaximumSeconds > MaxETASeconds ||
		rangeValue.MinimumSeconds > rangeValue.MaximumSeconds {
		return EstimateRange{}, time.Time{}, time.Time{}, nil, false, false
	}
	if rangeValue.PointSeconds != nil && (*rangeValue.PointSeconds < rangeValue.MinimumSeconds ||
		*rangeValue.PointSeconds > rangeValue.MaximumSeconds) {
		return EstimateRange{}, time.Time{}, time.Time{}, nil, false, false
	}
	optimistic, ok := addSeconds(fact.ServerReceivedAt, rangeValue.MinimumSeconds)
	if !ok {
		return EstimateRange{}, time.Time{}, time.Time{}, nil, false, false
	}
	pessimistic, ok := addSeconds(fact.ServerReceivedAt, rangeValue.MaximumSeconds)
	if !ok {
		return EstimateRange{}, time.Time{}, time.Time{}, nil, false, false
	}
	if !pessimistic.After(calculatedAt) {
		return EstimateRange{}, optimistic, pessimistic, nil, true, true
	}
	remaining := EstimateRange{
		MinimumSeconds: saturatingSubtractToZero(rangeValue.MinimumSeconds, nonnegativeSecondsBetween(fact.ServerReceivedAt, calculatedAt)),
		MaximumSeconds: saturatingSubtractToZero(rangeValue.MaximumSeconds, nonnegativeSecondsBetween(fact.ServerReceivedAt, calculatedAt)),
	}
	var pointAt *time.Time
	if rangeValue.PointSeconds != nil {
		point, pointOK := addSeconds(fact.ServerReceivedAt, *rangeValue.PointSeconds)
		if !pointOK {
			return EstimateRange{}, time.Time{}, time.Time{}, nil, false, false
		}
		if point.After(calculatedAt) {
			seconds := saturatingSubtractToZero(*rangeValue.PointSeconds, nonnegativeSecondsBetween(fact.ServerReceivedAt, calculatedAt))
			remaining.PointSeconds = &seconds
			pointAt = &point
		}
	}
	return remaining, optimistic, pessimistic, pointAt, true, false
}

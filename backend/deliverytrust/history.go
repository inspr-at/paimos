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
	"math/big"
	"sort"
	"time"
)

type HistoryOptions struct {
	ProjectIdentity  string
	Stage            StageKey
	PolicyVersion    int
	CalculatedAt     time.Time
	ExecutionStarted *time.Time
}

type HistoryResult struct {
	Eligible            bool
	Failure             SuppressionCode
	MinimumSeconds      int64
	MaximumSeconds      int64
	PointSeconds        int64
	Confidence          float64
	ConfidenceLabel     ConfidenceLabel
	RawSampleCount      int
	InlierSampleCount   int
	RejectedSampleCount int
	P10Seconds          int64
	MedianSeconds       int64
	P90Seconds          int64
	Components          ComponentMedians
	Flags               []Flag

	immutableSamples []DurationSample
	nextTransitions  []time.Time
}

type historyPoint struct {
	value  int64
	sample DurationSample
}

func AnalyzeHistory(samples []DurationSample, options HistoryOptions) (HistoryResult, error) {
	return analyzeHistory(samples, options)
}

func analyzeHistory(samples []DurationSample, options HistoryOptions) (HistoryResult, error) {
	if !validOpaque(options.ProjectIdentity) || !validStage(options.Stage) ||
		options.PolicyVersion != EstimatorPolicyVersion || options.CalculatedAt.IsZero() ||
		(options.ExecutionStarted != nil && (options.ExecutionStarted.IsZero() || options.ExecutionStarted.After(options.CalculatedAt))) {
		return HistoryResult{}, fmt.Errorf("%w: invalid history options", ErrInvalidInput)
	}
	matching := make([]DurationSample, 0, len(samples))
	seenIdentity := make(map[string]bool, len(samples))
	seenExecution := make(map[uint64]bool, len(samples))
	for _, sample := range samples {
		if sample.ProjectIdentity != options.ProjectIdentity || sample.Stage != options.Stage || sample.PolicyVersion != options.PolicyVersion {
			continue
		}
		if err := validateDurationSample(sample); err != nil {
			return HistoryResult{}, err
		}
		if sample.CompletedAt.After(options.CalculatedAt) {
			return HistoryResult{}, fmt.Errorf("%w: duration sample completes after calculation", ErrInvalidInput)
		}
		if seenIdentity[sample.Identity] || seenExecution[sample.StageExecutionID] {
			return HistoryResult{}, fmt.Errorf("%w: duplicate duration sample", ErrInvalidInput)
		}
		seenIdentity[sample.Identity] = true
		seenExecution[sample.StageExecutionID] = true
		matching = append(matching, sample)
	}
	sort.Slice(matching, func(i, j int) bool {
		if !matching[i].CompletedAt.Equal(matching[j].CompletedAt) {
			return matching[i].CompletedAt.After(matching[j].CompletedAt)
		}
		if matching[i].StageExecutionID != matching[j].StageExecutionID {
			return matching[i].StageExecutionID > matching[j].StageExecutionID
		}
		return matching[i].Identity > matching[j].Identity
	})
	if len(matching) > MaxHistorySamples {
		matching = matching[:MaxHistorySamples]
	}
	result := HistoryResult{
		RawSampleCount:   len(matching),
		ConfidenceLabel:  ConfidenceUnknown,
		immutableSamples: append([]DurationSample(nil), matching...),
	}
	if len(matching) < 5 {
		result.Failure = SuppressInsufficientBasis
		result.Flags = []Flag{FlagHistoryInsufficientBasis}
		return result, nil
	}

	elapsed := int64(0)
	if options.ExecutionStarted != nil {
		elapsed = nonnegativeSecondsBetween(*options.ExecutionStarted, options.CalculatedAt)
	}
	points := make([]historyPoint, 0, len(matching))
	for _, sample := range matching {
		value := sample.FullLeadSeconds
		if options.ExecutionStarted != nil {
			value = saturatingSubtractToZero(value, elapsed)
			if transition, ok := addSeconds(*options.ExecutionStarted, sample.FullLeadSeconds); ok && transition.After(options.CalculatedAt) {
				result.nextTransitions = append(result.nextTransitions, transition)
			}
		}
		points = append(points, historyPoint{value: value, sample: sample})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].value != points[j].value {
			return points[i].value < points[j].value
		}
		if points[i].sample.StageExecutionID != points[j].sample.StageExecutionID {
			return points[i].sample.StageExecutionID < points[j].sample.StageExecutionID
		}
		return points[i].sample.Identity < points[j].sample.Identity
	})
	values := historyValues(points)
	q1 := nearestRank(values, 1, 4)
	q3 := nearestRank(values, 3, 4)
	inliers := make([]historyPoint, 0, len(points))
	for _, point := range points {
		if insideTukeyFence(point.value, q1, q3) {
			inliers = append(inliers, point)
		}
	}
	result.InlierSampleCount = len(inliers)
	result.RejectedSampleCount = len(points) - len(inliers)
	required := maxInt(5, ceilThreeFifths(len(points)))
	if len(inliers) < required {
		result.Failure = SuppressOutlierHeavy
		result.Flags = []Flag{FlagHistoryOutlierHeavy}
		return result, nil
	}

	inlierValues := historyValues(inliers)
	result.P10Seconds = nearestRank(inlierValues, 1, 10)
	result.MedianSeconds = median(inlierValues)
	result.P90Seconds = nearestRank(inlierValues, 9, 10)
	result.MinimumSeconds = result.P10Seconds
	result.PointSeconds = result.MedianSeconds
	result.MaximumSeconds = result.P90Seconds
	result.Confidence, result.ConfidenceLabel = historyConfidence(len(inliers))
	spread := result.P90Seconds - result.P10Seconds
	qualityDowngrade := result.RejectedSampleCount*5 >= result.RawSampleCount || spread > result.MedianSeconds
	if qualityDowngrade {
		result.Confidence, result.ConfidenceLabel = downgradeConfidence(result.Confidence, result.ConfidenceLabel)
		result.Flags = append(result.Flags, FlagHistoryQualityDowngraded)
	}
	result.Components = componentMedians(inliers)
	result.Eligible = true
	return result, nil
}

func validateDurationSample(sample DurationSample) error {
	if !validOpaque(sample.Identity) || sample.StageExecutionID == 0 || !validOpaque(sample.ProjectIdentity) ||
		!validStage(sample.Stage) || sample.PolicyVersion <= 0 || sample.CompletedAt.IsZero() ||
		sample.FullLeadSeconds < 0 || sample.ActiveSeconds < 0 || sample.BlockedSeconds < 0 || sample.HumanWaitSeconds < 0 {
		return fmt.Errorf("%w: malformed duration sample", ErrInvalidInput)
	}
	total, ok := checkedAdd(sample.ActiveSeconds, sample.BlockedSeconds)
	if !ok {
		return fmt.Errorf("%w: duration component overflow", ErrInvalidInput)
	}
	total, ok = checkedAdd(total, sample.HumanWaitSeconds)
	if !ok || total != sample.FullLeadSeconds {
		return fmt.Errorf("%w: duration components do not equal full lead", ErrInvalidInput)
	}
	return nil
}

func historyValues(points []historyPoint) []int64 {
	values := make([]int64, len(points))
	for i := range points {
		values[i] = points[i].value
	}
	return values
}

func nearestRank(sortedValues []int64, numerator, denominator int) int64 {
	rank := (numerator*len(sortedValues) + denominator - 1) / denominator
	if rank < 1 {
		rank = 1
	}
	if rank > len(sortedValues) {
		rank = len(sortedValues)
	}
	return sortedValues[rank-1]
}

func median(sortedValues []int64) int64 {
	mid := len(sortedValues) / 2
	if len(sortedValues)%2 == 1 {
		return sortedValues[mid]
	}
	a, b := sortedValues[mid-1], sortedValues[mid]
	return a + (b-a)/2
}

func insideTukeyFence(value, q1, q3 int64) bool {
	// Compare 2*x against 2*q1-3*IQR and 2*q3+3*IQR. big.Int keeps
	// deliberately hostile int64 fixtures deterministic and overflow-free.
	iqr := new(big.Int).Sub(big.NewInt(q3), big.NewInt(q1))
	threeIQR := new(big.Int).Mul(iqr, big.NewInt(3))
	lower := new(big.Int).Sub(new(big.Int).Mul(big.NewInt(q1), big.NewInt(2)), threeIQR)
	upper := new(big.Int).Add(new(big.Int).Mul(big.NewInt(q3), big.NewInt(2)), threeIQR)
	twice := new(big.Int).Mul(big.NewInt(value), big.NewInt(2))
	return twice.Cmp(lower) >= 0 && twice.Cmp(upper) <= 0
}

func componentMedians(points []historyPoint) ComponentMedians {
	full := make([]int64, len(points))
	active := make([]int64, len(points))
	blocked := make([]int64, len(points))
	human := make([]int64, len(points))
	for i, point := range points {
		full[i] = point.sample.FullLeadSeconds
		active[i] = point.sample.ActiveSeconds
		blocked[i] = point.sample.BlockedSeconds
		human[i] = point.sample.HumanWaitSeconds
	}
	for _, values := range [][]int64{full, active, blocked, human} {
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	}
	return ComponentMedians{
		FullLeadSeconds: median(full), ActiveSeconds: median(active),
		BlockedSeconds: median(blocked), HumanWaitSeconds: median(human),
	}
}

func ceilThreeFifths(value int) int { return (3*value + 4) / 5 }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

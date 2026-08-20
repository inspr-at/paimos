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
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestHistoryConfidenceBoundaries(t *testing.T) {
	tests := []struct {
		count          int
		wantEligible   bool
		wantFailure    SuppressionCode
		wantConfidence float64
		wantLabel      ConfidenceLabel
	}{
		{0, false, SuppressInsufficientBasis, 0, ConfidenceUnknown},
		{1, false, SuppressInsufficientBasis, 0, ConfidenceUnknown},
		{4, false, SuppressInsufficientBasis, 0, ConfidenceUnknown},
		{5, true, "", 0.25, ConfidenceLow},
		{9, true, "", 0.25, ConfidenceLow},
		{10, true, "", 0.65, ConfidenceMedium},
		{29, true, "", 0.65, ConfidenceMedium},
		{30, true, "", 0.90, ConfidenceHigh},
		{100, true, "", 0.90, ConfidenceHigh},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("n=%d", test.count), func(t *testing.T) {
			result := mustAnalyzeHistory(t, historySamples(StageQA, test.count, 100), StageQA, nil)
			if result.Eligible != test.wantEligible || result.Failure != test.wantFailure ||
				result.Confidence != test.wantConfidence || result.ConfidenceLabel != test.wantLabel {
				t.Fatalf("n=%d result=%+v", test.count, result)
			}
		})
	}
}

func TestHistoryLatest100AndPermutationAreExact(t *testing.T) {
	samples := historySamples(StageQA, 101, 100)
	// The oldest sample is outside latest-100 and must not affect the result.
	samples[100].FullLeadSeconds = 9_999
	samples[100].ActiveSeconds = 9_999
	first := mustAnalyzeHistory(t, samples, StageQA, nil)
	if first.RawSampleCount != 100 || first.MedianSeconds != 100 || first.P90Seconds != 100 {
		t.Fatalf("latest-100 selection incorrect: %+v", first)
	}

	permuted := append([]DurationSample(nil), samples...)
	rand.New(rand.NewPCG(11, 29)).Shuffle(len(permuted), func(i, j int) {
		permuted[i], permuted[j] = permuted[j], permuted[i]
	})
	second := mustAnalyzeHistory(t, permuted, StageQA, nil)
	if !reflect.DeepEqual(publicHistory(first), publicHistory(second)) ||
		!reflect.DeepEqual(first.immutableSamples, second.immutableSamples) {
		t.Fatalf("permutation changed history\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestHistoryNearestRankMedianAndResidual(t *testing.T) {
	values := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	samples := samplesWithValues(StageQA, values)
	result := mustAnalyzeHistory(t, samples, StageQA, nil)
	if result.P10Seconds != 10 || result.MedianSeconds != 55 || result.P90Seconds != 90 {
		t.Fatalf("rank statistics = p10:%d median:%d p90:%d", result.P10Seconds, result.MedianSeconds, result.P90Seconds)
	}

	started := fixtureNow.Add(-35 * time.Second)
	residual := mustAnalyzeHistory(t, samples, StageQA, &started)
	// Values <=35 clamp to zero before statistics: 0,0,0,5,...65.
	if residual.P10Seconds != 0 || residual.MedianSeconds != 20 || residual.P90Seconds != 55 {
		t.Fatalf("residual statistics = p10:%d median:%d p90:%d", residual.P10Seconds, residual.MedianSeconds, residual.P90Seconds)
	}
	if min := minimumFuture(fixtureNow, residual.nextTransitions...); min == nil || !min.Equal(fixtureNow.Add(5*time.Second)) {
		t.Fatalf("residual next transition=%v, want +5s", min)
	}
}

func TestHistoryOutlierAndQualityBoundaries(t *testing.T) {
	t.Run("exact 20 percent downgrades once", func(t *testing.T) {
		values := append(repeatValue(24, 100), repeatValue(6, 1_000)...)
		result := mustAnalyzeHistory(t, samplesWithValues(StageQA, values), StageQA, nil)
		if !result.Eligible || result.RejectedSampleCount != 6 || result.Confidence != 0.25 || result.ConfidenceLabel != ConfidenceLow {
			t.Fatalf("exact-20 result=%+v", result)
		}
		if countFlag(result.Flags, FlagHistoryQualityDowngraded) != 1 {
			t.Fatalf("quality downgrade flags=%v", result.Flags)
		}
	})

	t.Run("exact 60 percent remains eligible", func(t *testing.T) {
		values := append(repeatValue(2, 0), repeatValue(6, 100)...)
		values = append(values, repeatValue(2, 1_000)...)
		result := mustAnalyzeHistory(t, samplesWithValues(StageQA, values), StageQA, nil)
		if !result.Eligible || result.InlierSampleCount != 6 {
			t.Fatalf("exact-60 result=%+v", result)
		}
	})

	t.Run("below 60 percent is outlier heavy", func(t *testing.T) {
		values := append(repeatValue(4, 0), repeatValue(11, 100)...)
		values = append(values, repeatValue(5, 1_000)...)
		result := mustAnalyzeHistory(t, samplesWithValues(StageQA, values), StageQA, nil)
		if result.Eligible || result.Failure != SuppressOutlierHeavy || result.InlierSampleCount != 11 {
			t.Fatalf("outlier-heavy result=%+v", result)
		}
	})

	t.Run("dispersion downgrades once", func(t *testing.T) {
		result := mustAnalyzeHistory(t, samplesWithValues(StageQA, []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}), StageQA, nil)
		if !result.Eligible || result.MedianSeconds != 4 || result.P90Seconds-result.P10Seconds <= result.MedianSeconds ||
			result.Confidence != 0.25 || countFlag(result.Flags, FlagHistoryQualityDowngraded) != 1 {
			t.Fatalf("dispersion result=%+v", result)
		}
	})

	t.Run("median zero with spread downgrades", func(t *testing.T) {
		result := mustAnalyzeHistory(t, samplesWithValues(StageQA, []int64{0, 0, 0, 0, 0, 0, 1, 1, 1, 1}), StageQA, nil)
		if result.MedianSeconds != 0 || result.P90Seconds-result.P10Seconds == 0 ||
			!slices.Contains(result.Flags, FlagHistoryQualityDowngraded) {
			t.Fatalf("median-zero result=%+v", result)
		}
	})

	t.Run("all equal IQR zero remains eligible", func(t *testing.T) {
		result := mustAnalyzeHistory(t, historySamples(StageQA, 10, 0), StageQA, nil)
		if !result.Eligible || result.InlierSampleCount != 10 || result.P10Seconds != 0 || result.P90Seconds != 0 || len(result.Flags) != 0 {
			t.Fatalf("all-equal result=%+v", result)
		}
	})
}

func TestHistoryProjectPolicyIsolationAndUniqueness(t *testing.T) {
	samples := historySamples(StageQA, 5, 100)
	otherProject := historySamples(StageQA, 20, 9_999)
	for i := range otherProject {
		otherProject[i].Identity = "other-project-" + otherProject[i].Identity
		otherProject[i].StageExecutionID += 100_000
		otherProject[i].ProjectIdentity = "project-hidden"
	}
	otherPolicy := historySamples(StageQA, 20, 8_888)
	for i := range otherPolicy {
		otherPolicy[i].Identity = "other-policy-" + otherPolicy[i].Identity
		otherPolicy[i].StageExecutionID += 200_000
		otherPolicy[i].PolicyVersion = 2
	}
	samples = append(samples, otherProject...)
	samples = append(samples, otherPolicy...)
	result := mustAnalyzeHistory(t, samples, StageQA, nil)
	if result.RawSampleCount != 5 || result.MedianSeconds != 100 {
		t.Fatalf("cross-project/policy samples leaked: %+v", result)
	}

	duplicates := historySamples(StageQA, 5, 100)
	duplicates[1].StageExecutionID = duplicates[0].StageExecutionID
	if _, err := AnalyzeHistory(duplicates, historyOptions(StageQA, nil)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate execution error=%v", err)
	}
}

func TestHistoryRejectsUnsupportedPolicyAndFutureExecutionStart(t *testing.T) {
	options := historyOptions(StageQA, nil)
	options.PolicyVersion++
	if _, err := AnalyzeHistory(historySamples(StageQA, 5, 100), options); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsupported history policy error=%v", err)
	}
	future := fixtureNow.Add(time.Second)
	options = historyOptions(StageQA, &future)
	if _, err := AnalyzeHistory(historySamples(StageQA, 5, 100), options); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("future execution start error=%v", err)
	}
}

func TestHistoryOverflowSafeMath(t *testing.T) {
	if got := median([]int64{math.MaxInt64 - 1, math.MaxInt64}); got != math.MaxInt64-1 {
		t.Fatalf("overflow-safe median=%d", got)
	}
	if !insideTukeyFence(math.MaxInt64, math.MaxInt64-1, math.MaxInt64) {
		t.Fatal("overflow-safe Tukey fence rejected maximum int64")
	}
	if insideTukeyFence(math.MaxInt64, 0, 0) {
		t.Fatal("zero-IQR fence accepted maximum outlier")
	}

	samples := historySamples(StageQA, 5, math.MaxInt64)
	result := mustAnalyzeHistory(t, samples, StageQA, nil)
	if result.MedianSeconds != math.MaxInt64 || result.Components.FullLeadSeconds != math.MaxInt64 {
		t.Fatalf("max-int history corrupted: %+v", result)
	}
}

func TestAgentHistoryMergeRules(t *testing.T) {
	point := int64(100)
	owner := &Contributor{
		Stage: StageImplementation, Kind: ContributorAgent, CurrentStage: true,
		Confidence: 0.9, ConfidenceLabel: ConfidenceHigh,
		MinimumRemainingSeconds: 80, MaximumRemainingSeconds: 120, PointRemainingSeconds: &point,
	}
	history := HistoryResult{
		Eligible: true, MinimumSeconds: 90, MaximumSeconds: 140, PointSeconds: 110,
		Confidence: 0.65, ConfidenceLabel: ConfidenceMedium,
		RawSampleCount: 10, InlierSampleCount: 10,
	}
	merged, disagreement := mergeContributor(StageImplementation, true, owner, history)
	if disagreement || merged.MinimumRemainingSeconds != 80 || merged.MaximumRemainingSeconds != 140 ||
		merged.PointRemainingSeconds == nil || *merged.PointRemainingSeconds != 100 ||
		merged.Confidence != 0.9 || merged.ConfidenceLabel != ConfidenceHigh {
		t.Fatalf("overlap merge=%+v disagreement=%v", merged, disagreement)
	}

	withoutPoint := *owner
	withoutPoint.PointRemainingSeconds = nil
	merged, disagreement = mergeContributor(StageImplementation, true, &withoutPoint, history)
	if disagreement || merged.PointRemainingSeconds == nil || *merged.PointRemainingSeconds != 110 {
		t.Fatalf("in-range history median not adopted: %+v", merged)
	}

	history.PointSeconds = 130 // ranges overlap, but point lies outside owner range.
	merged, disagreement = mergeContributor(StageImplementation, true, &withoutPoint, history)
	if disagreement || merged.PointRemainingSeconds != nil {
		t.Fatalf("out-of-owner-range history point synthesized: %+v", merged)
	}

	history.MinimumSeconds, history.MaximumSeconds, history.PointSeconds = 200, 300, 250
	merged, disagreement = mergeContributor(StageImplementation, true, owner, history)
	if !disagreement || merged.MinimumRemainingSeconds != 80 || merged.MaximumRemainingSeconds != 300 ||
		merged.PointRemainingSeconds != nil || merged.Confidence != 0.25 || merged.ConfidenceLabel != ConfidenceLow ||
		countFlag(merged.Flags, FlagAgentHistoryDisagreement) != 1 {
		t.Fatalf("disjoint merge=%+v disagreement=%v", merged, disagreement)
	}

	sparse := HistoryResult{Failure: SuppressInsufficientBasis, Flags: []Flag{FlagHistoryInsufficientBasis}}
	merged, disagreement = mergeContributor(StageImplementation, true, owner, sparse)
	if disagreement || merged.Kind != ContributorAgent || !slices.Contains(merged.Flags, FlagHistoryInsufficientBasis) {
		t.Fatalf("optional bad history suppressed owner: %+v", merged)
	}

	historyOnly, disagreement := mergeContributor(StageImplementation, true, nil, history)
	if disagreement || historyOnly.Kind != ContributorHistory {
		t.Fatalf("history-only merge=%+v", historyOnly)
	}
}

func TestWeightedProgressMonotonicProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(803, 1))
	for trial := 0; trial < 1_000; trial++ {
		input := fixtureInput(1)
		lower := random.Float64() * 100
		higher := lower + random.Float64()*(100-lower)
		input.Stages[1].Estimates[0].ProgressPercent = &lower
		first := mustEvaluate(t, input)
		input.Stages[1].Estimates[0].ProgressPercent = &higher
		second := mustEvaluate(t, input)
		if *second.ProgressPercent < *first.ProgressPercent {
			t.Fatalf("progress decreased for %f -> %f: %d -> %d", lower, higher, *first.ProgressPercent, *second.ProgressPercent)
		}
		if *second.ProgressPercent > 99 {
			t.Fatalf("incomplete progress escaped cap: %d", *second.ProgressPercent)
		}
	}
}

func mustAnalyzeHistory(t *testing.T, samples []DurationSample, stage StageKey, started *time.Time) HistoryResult {
	t.Helper()
	result, err := AnalyzeHistory(samples, historyOptions(stage, started))
	if err != nil {
		t.Fatalf("AnalyzeHistory() error: %v", err)
	}
	return result
}

func historyOptions(stage StageKey, started *time.Time) HistoryOptions {
	return HistoryOptions{
		ProjectIdentity: "project-1", Stage: stage,
		PolicyVersion: EstimatorPolicyVersion,
		CalculatedAt:  fixtureNow, ExecutionStarted: started,
	}
}

func samplesWithValues(stage StageKey, values []int64) []DurationSample {
	samples := historySamples(stage, len(values), 0)
	for i, value := range values {
		samples[i].FullLeadSeconds = value
		samples[i].ActiveSeconds = value
	}
	return samples
}

func repeatValue(count int, value int64) []int64 {
	values := make([]int64, count)
	for i := range values {
		values[i] = value
	}
	return values
}

func countFlag(flags []Flag, wanted Flag) int {
	count := 0
	for _, flag := range flags {
		if flag == wanted {
			count++
		}
	}
	return count
}

type publicHistoryResult struct {
	Eligible                bool
	Failure                 SuppressionCode
	Minimum, Maximum, Point int64
	Confidence              float64
	Label                   ConfidenceLabel
	Raw, Inlier, Rejected   int
	P10, Median, P90        int64
	Components              ComponentMedians
	Flags                   []Flag
}

func publicHistory(result HistoryResult) publicHistoryResult {
	return publicHistoryResult{
		Eligible: result.Eligible, Failure: result.Failure,
		Minimum: result.MinimumSeconds, Maximum: result.MaximumSeconds, Point: result.PointSeconds,
		Confidence: result.Confidence, Label: result.ConfidenceLabel,
		Raw: result.RawSampleCount, Inlier: result.InlierSampleCount, Rejected: result.RejectedSampleCount,
		P10: result.P10Seconds, Median: result.MedianSeconds, P90: result.P90Seconds,
		Components: result.Components, Flags: result.Flags,
	}
}

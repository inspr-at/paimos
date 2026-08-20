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
	"reflect"
	"testing"
	"time"
)

func FuzzHistoryPermutation(f *testing.F) {
	f.Add([]byte{1, 7, 3, 9, 2, 4, 8, 6, 5, 0})
	f.Add([]byte{0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 5 {
			t.Skip()
		}
		if len(data) > MaxHistorySamples {
			data = data[:MaxHistorySamples]
		}
		values := make([]int64, len(data))
		for i := range data {
			values[i] = int64(data[i])
		}
		ordered := samplesWithValues(StageQA, values)
		permuted := append([]DurationSample(nil), ordered...)
		for i := range permuted {
			j := int(data[i]) % len(permuted)
			permuted[i], permuted[j] = permuted[j], permuted[i]
		}
		first, err := AnalyzeHistory(ordered, historyOptions(StageQA, nil))
		if err != nil {
			t.Fatal(err)
		}
		second, err := AnalyzeHistory(permuted, historyOptions(StageQA, nil))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(publicHistory(first), publicHistory(second)) ||
			!reflect.DeepEqual(first.immutableSamples, second.immutableSamples) {
			t.Fatalf("permutation changed result: first=%+v second=%+v", first, second)
		}
	})
}

func FuzzMissingPointNeverCreatesMidpoint(f *testing.F) {
	f.Add(uint16(10), uint16(20))
	f.Add(uint16(0), uint16(0))
	f.Fuzz(func(t *testing.T, a, b uint16) {
		minimum, maximum := int64(a), int64(b)
		if minimum > maximum {
			minimum, maximum = maximum, minimum
		}
		owner := &Contributor{
			Stage: StageImplementation, Kind: ContributorAgent, CurrentStage: true,
			Confidence: 0.9, ConfidenceLabel: ConfidenceHigh,
			MinimumRemainingSeconds: minimum, MaximumRemainingSeconds: maximum,
		}
		merged, disagreement := mergeContributor(
			StageImplementation, true, owner,
			HistoryResult{Failure: SuppressInsufficientBasis},
		)
		if disagreement || merged.PointRemainingSeconds != nil {
			t.Fatalf("midpoint synthesized for [%d,%d]: %+v", minimum, maximum, merged)
		}
	})
}

func FuzzTrustRevisionIgnoresCalculationClock(f *testing.F) {
	f.Add(uint16(0))
	f.Add(uint16(600))
	f.Add(uint16(1_800))
	f.Fuzz(func(t *testing.T, raw uint16) {
		seconds := int(raw % 1_801)
		firstInput := fixtureInput(1)
		secondInput := fixtureInput(1)
		secondInput.CalculatedAt = secondInput.CalculatedAt.Add(time.Duration(seconds) * time.Second)
		first, err := Evaluate(firstInput)
		if err != nil {
			t.Fatal(err)
		}
		second, err := Evaluate(secondInput)
		if err != nil {
			t.Fatal(err)
		}
		if first.TrustRevision != second.TrustRevision {
			t.Fatalf("calculation clock changed trust revision: %s != %s", first.TrustRevision, second.TrustRevision)
		}
	})
}

func FuzzSameScopeProgressMonotonic(f *testing.F) {
	f.Add(uint16(10), uint16(90))
	f.Add(uint16(100), uint16(0))
	f.Fuzz(func(t *testing.T, a, b uint16) {
		lower, higher := float64(a%101), float64(b%101)
		if lower > higher {
			lower, higher = higher, lower
		}
		input := fixtureInput(1)
		input.Stages[1].Estimates[0].ProgressPercent = &lower
		first, err := Evaluate(input)
		if err != nil {
			t.Fatal(err)
		}
		input.Stages[1].Estimates[0].ProgressPercent = &higher
		second, err := Evaluate(input)
		if err != nil {
			t.Fatal(err)
		}
		if *second.ProgressPercent < *first.ProgressPercent || *second.ProgressPercent > 99 {
			t.Fatalf("progress invariant failed: %.0f/%.0f -> %d/%d", lower, higher, *first.ProgressPercent, *second.ProgressPercent)
		}
	})
}

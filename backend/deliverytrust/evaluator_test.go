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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var fixtureNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func TestEvaluateWeightedProgressAndEndToEndLanding(t *testing.T) {
	input := fixtureInput(1)
	output := mustEvaluate(t, input)

	assertIntPointer(t, output.ProgressPercent, 32)
	if output.Suppression != "" {
		t.Fatalf("unexpected suppression: %s", output.Suppression)
	}
	if output.CurrentStage == nil || *output.CurrentStage != StageImplementation {
		t.Fatalf("current stage = %v, want implementation", output.CurrentStage)
	}
	assertInt64Pointer(t, output.RemainingMinimumSeconds, 3_900)
	assertInt64Pointer(t, output.RemainingMaximumSeconds, 4_100)
	assertInt64Pointer(t, output.RemainingSeconds, 4_000)
	if output.RangeOnly || output.LandingAt == nil || output.ConfidenceLabel != ConfidenceMedium {
		t.Fatalf("expected medium point landing, got range_only=%v landing=%v confidence=%s", output.RangeOnly, output.LandingAt, output.ConfidenceLabel)
	}
	if got, want := len(output.Contributors), 4; got != want {
		t.Fatalf("contributors = %d, want %d", got, want)
	}
	if got, want := len(output.StageDiagnostics), 4; got != want {
		t.Fatalf("stage diagnostics = %d, want %d", got, want)
	}
	for i, stage := range canonicalStages[1:] {
		diagnostic := output.StageDiagnostics[i]
		if diagnostic.Stage != stage || diagnostic.CurrentStage != (i == 0) || !diagnostic.Covered || diagnostic.Failure != "" {
			t.Fatalf("diagnostic[%d]=%+v", i, diagnostic)
		}
	}
	if output.Contributors[0].Kind != ContributorAgentHistory {
		t.Fatalf("current contributor = %s, want merged", output.Contributors[0].Kind)
	}
	if output.NextTrustTransitionAt == nil || !output.NextTrustTransitionAt.Equal(fixtureNow.Add(900*time.Second)) {
		t.Fatalf("next transition = %v, want %v", output.NextTrustTransitionAt, fixtureNow.Add(900*time.Second))
	}
	if !regexp.MustCompile(`^tr1_[0-9a-f]{64}$`).MatchString(output.TrustRevision) {
		t.Fatalf("invalid trust revision %q", output.TrustRevision)
	}
	if output.OwnerSource == nil || output.OwnerSource.Identity != "estimate-implementation-1" ||
		output.ProgressSource == nil || output.ProgressSource.Identity != "estimate-implementation-1" {
		t.Fatalf("source attribution missing: owner=%+v progress=%+v", output.OwnerSource, output.ProgressSource)
	}
}

func TestEvaluateNAWeightRenormalization(t *testing.T) {
	input := fixtureInput(1)
	makeStageNA(&input, 2)
	output := mustEvaluate(t, input)

	// (10 completed + 45*50%) / (10+45+15+10) = floor(40.625).
	assertIntPointer(t, output.ProgressPercent, 40)
	assertInt64Pointer(t, output.RemainingMinimumSeconds, 2_900)
	assertInt64Pointer(t, output.RemainingMaximumSeconds, 3_100)
	if len(output.Contributors) != 3 {
		t.Fatalf("contributors = %d, want 3", len(output.Contributors))
	}
	if len(output.StageDiagnostics) != 3 {
		t.Fatalf("diagnostics = %d, want 3 required remaining stages", len(output.StageDiagnostics))
	}
	for _, diagnostic := range output.StageDiagnostics {
		if diagnostic.Stage == StageQA {
			t.Fatalf("N/A stage leaked into diagnostics: %+v", diagnostic)
		}
	}
}

func TestEveryStageAndNACombinationIsDeterministic(t *testing.T) {
	for current := 0; current < len(canonicalStages); current++ {
		for naMask := 0; naMask < 1<<len(canonicalStages); naMask++ {
			name := fmt.Sprintf("current=%s/na=%05b", canonicalStages[current], naMask)
			t.Run(name, func(t *testing.T) {
				input := fixtureInput(current)
				requiredWeight := 0
				weightedHundredths := 0
				for i := range canonicalStages {
					if naMask&(1<<i) != 0 {
						makeStageNA(&input, i)
						continue
					}
					requiredWeight += input.Policy[i].Weight
					if i < current {
						weightedHundredths += input.Policy[i].Weight * 100
					} else if i == current {
						weightedHundredths += input.Policy[i].Weight * 50
					}
				}
				if requiredWeight == 0 {
					if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
						t.Fatalf("all-N/A error=%v", err)
					}
					return
				}

				output := mustEvaluate(t, input)
				completed := true
				for i := range canonicalStages {
					if input.Policy[i].Required && !input.Stages[i].Completion.Eligible {
						completed = false
						break
					}
				}
				expected := weightedHundredths / requiredWeight
				if completed {
					expected = 100
				} else if expected > 99 {
					expected = 99
				}
				assertIntPointer(t, output.ProgressPercent, expected)
				if output.Completed != completed {
					t.Fatalf("completed=%v, want %v", output.Completed, completed)
				}
				expectedDiagnostics := make([]StageKey, 0, len(canonicalStages))
				if !completed {
					for i := range input.Stages {
						if input.Policy[i].Required && !input.Stages[i].Completion.Eligible {
							expectedDiagnostics = append(expectedDiagnostics, input.Stages[i].Stage)
						}
					}
				}
				if len(output.StageDiagnostics) != len(expectedDiagnostics) {
					t.Fatalf("diagnostics=%+v, want stages=%v", output.StageDiagnostics, expectedDiagnostics)
				}
				for i, stage := range expectedDiagnostics {
					if output.StageDiagnostics[i].Stage != stage {
						t.Fatalf("diagnostic[%d]=%+v, want stage=%s", i, output.StageDiagnostics[i], stage)
					}
				}
			})
		}
	}
}

func TestCompletionEvidenceGatesEveryStage(t *testing.T) {
	for current := range canonicalStages {
		t.Run(string(canonicalStages[current]), func(t *testing.T) {
			input := fixtureInput(current)
			hundred := 100.0
			input.Stages[current].Estimates[0].ProgressPercent = &hundred
			output := mustEvaluate(t, input)
			if output.Completed || *output.ProgressPercent > 99 {
				t.Fatalf("reported milestone completed %s: %+v", canonicalStages[current], output)
			}

			input.Stages[current].Completion.Status = CompletionSucceeded
			if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("sealed success without eligible evidence error=%v", err)
			}

			input.Stages[current].Completion.Eligible = true
			if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("evidence-free eligible completion error=%v", err)
			}
		})
	}
}

func TestSchemaV1RejectsUnsupportedVersionAndCustomWeights(t *testing.T) {
	input := fixtureInput(1)
	input.PolicyVersion = EstimatorPolicyVersion + 1
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsupported policy version error=%v", err)
	}

	for index := range canonicalStages {
		t.Run(string(canonicalStages[index]), func(t *testing.T) {
			input := fixtureInput(1)
			input.Policy[index].Weight++
			if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("custom v1 weight error=%v", err)
			}
			makeStageNA(&input, index)
			input.Policy[index].Weight++
			if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("custom N/A v1 weight error=%v", err)
			}
		})
	}
}

func TestNAStageMustBeSterile(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StageInput)
	}{
		{"execution", func(stage *StageInput) {
			started := fixtureNow.Add(-time.Minute)
			stage.ExecutionStartedAt = &started
			stage.Reporter = ReporterAgentRun
			stage.Scope = startedScope(2)
		}},
		{"completion", func(stage *StageInput) { stage.Completion = successfulCompletion(2) }},
		{"semantic signal", func(stage *StageInput) { stage.Signals.SemanticIdentity = "na-activity" }},
		{"clock signal", func(stage *StageInput) { stage.Signals.Stale = true }},
		{"transition", func(stage *StageInput) { stage.Signals.TransitionAt = []time.Time{fixtureNow.Add(time.Minute)} }},
		{"estimate", func(stage *StageInput) {
			progress := 20.0
			stage.Estimates = []EstimateFact{{
				Identity: "na-estimate", Reporter: ReporterAgentRun, Scope: startedScope(2),
				Revision: 1, Sequence: 1, Source: SourceAgent, ServerReceivedAt: fixtureNow,
				Confidence: 0.9, Basis: "must not exist", ProgressPercent: &progress,
			}}
		}},
		{"history", func(stage *StageInput) { stage.History = historySamples(StageQA, 5, 100) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixtureInput(1)
			makeStageNA(&input, 2)
			clean := mustEvaluate(t, input)
			test.mutate(&input.Stages[2])
			if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("non-sterile N/A error=%v; clean revision=%s", err, clean.TrustRevision)
			}
		})
	}
}

func TestPerformedDeploymentVerificationTruthIsIndependentFromNA(t *testing.T) {
	t.Run("performed deployment awaits required verification", func(t *testing.T) {
		output := mustEvaluate(t, fixtureInput(4))
		if !output.Deployed || output.Verified || !output.Unverified || !output.DeployedUnverified || output.Completed {
			t.Fatalf("performed deployment truth=%+v", output)
		}
	})

	t.Run("verification N/A is neither verified nor unverified", func(t *testing.T) {
		input := fixtureInput(4)
		makeStageNA(&input, 4)
		output := mustEvaluate(t, input)
		if !output.Completed || !output.Deployed || output.Verified || output.Unverified || output.DeployedUnverified {
			t.Fatalf("verification-N/A truth=%+v", output)
		}
	})

	t.Run("deployment N/A never fabricates performed deployment", func(t *testing.T) {
		input := fixtureInput(4)
		makeStageNA(&input, 3)
		output := mustEvaluate(t, input)
		if output.Deployed || output.Verified || !output.Unverified || output.DeployedUnverified || output.Completed {
			t.Fatalf("deployment-N/A pending-verification truth=%+v", output)
		}
		input.Stages[4].Completion = successfulCompletion(4)
		output = mustEvaluate(t, input)
		if !output.Completed || output.Deployed || !output.Verified || output.Unverified || output.DeployedUnverified {
			t.Fatalf("deployment-N/A verified truth=%+v", output)
		}
	})

	t.Run("deployment and verification N/A fabricate neither", func(t *testing.T) {
		input := fixtureInput(3)
		makeStageNA(&input, 3)
		makeStageNA(&input, 4)
		output := mustEvaluate(t, input)
		if !output.Completed || output.Deployed || output.Verified || output.Unverified || output.DeployedUnverified {
			t.Fatalf("deployment+verification-N/A truth=%+v", output)
		}
	})
}

func TestReportedHundredNeverCompletesAndIncompleteCapsAt99(t *testing.T) {
	input := fixtureInput(4)
	hundred := 100.0
	input.Stages[4].Estimates[0].ProgressPercent = &hundred
	output := mustEvaluate(t, input)
	assertIntPointer(t, output.ProgressPercent, 99)
	if output.Completed || output.Suppression == SuppressTerminalComplete {
		t.Fatalf("reported 100 completed delivery: %+v", output)
	}

	input.Stages[4].Completion = successfulCompletion(4)
	output = mustEvaluate(t, input)
	assertIntPointer(t, output.ProgressPercent, 100)
	if !output.Completed || !output.Verified || output.Suppression != SuppressTerminalComplete {
		t.Fatalf("terminal delivery truth incorrect: %+v", output)
	}
}

func TestSameResetMaximumProgressAndNewestETASemantics(t *testing.T) {
	input := fixtureInput(1)
	eighty := 80.0
	input.Stages[1].Estimates[0].ProgressPercent = &eighty
	twenty := 20.0
	input.Stages[1].Estimates = append(input.Stages[1].Estimates, EstimateFact{
		Identity:         "estimate-implementation-2",
		Reporter:         ReporterAgentRun,
		Scope:            input.Stages[1].Scope,
		Revision:         2,
		Sequence:         2,
		Source:           SourceAgent,
		ServerReceivedAt: fixtureNow,
		Confidence:       0.9,
		Basis:            "new progress milestone",
		ProgressPercent:  &twenty,
		// Deliberately progress-only: this must supersede revision 1's ETA.
	})
	output := mustEvaluate(t, input)

	assertIntPointer(t, output.ProgressPercent, 46)
	if !slices.Contains(output.Flags, FlagSourceBackslideIgnored) {
		t.Fatalf("missing backslide flag: %v", output.Flags)
	}
	if output.ProgressSource == nil || output.ProgressSource.Identity != "estimate-implementation-1" {
		t.Fatalf("max-progress attribution = %+v", output.ProgressSource)
	}
	if output.OwnerSource == nil || output.OwnerSource.Identity != "estimate-implementation-2" {
		t.Fatalf("latest owner attribution = %+v", output.OwnerSource)
	}
	if output.Contributors[0].Kind != ContributorHistory {
		t.Fatalf("older ETA reused; contributor=%s", output.Contributors[0].Kind)
	}
}

func TestEstimatePermutationCannotChangeSelectionOrRevision(t *testing.T) {
	input := fixtureInput(1)
	eighty := 80.0
	input.Stages[1].Estimates[0].ProgressPercent = &eighty
	twenty := 20.0
	input.Stages[1].Estimates = append(input.Stages[1].Estimates, EstimateFact{
		Identity: "estimate-implementation-2", Reporter: ReporterAgentRun,
		Scope: input.Stages[1].Scope, Revision: 2, Sequence: 2,
		Source: SourceAgent, ServerReceivedAt: fixtureNow,
		Confidence: 0.9, Basis: "newer lower milestone", ProgressPercent: &twenty,
	})
	first := mustEvaluate(t, input)
	slices.Reverse(input.Stages[1].Estimates)
	second := mustEvaluate(t, input)
	if first.TrustRevision != second.TrustRevision || *first.ProgressPercent != *second.ProgressPercent ||
		first.OwnerSource.Identity != second.OwnerSource.Identity || !slices.Equal(first.Flags, second.Flags) {
		t.Fatalf("estimate permutation changed result:\nfirst=%+v\nsecond=%+v", first, second)
	}
}

func TestTrustRevisionIncludesDistinctLatestProgressDeterminant(t *testing.T) {
	build := func(latestProgress float64) Input {
		input := fixtureInput(1)
		eighty := 80.0
		input.Stages[1].Estimates[0].ProgressPercent = &eighty
		input.Stages[1].Estimates = append(input.Stages[1].Estimates,
			EstimateFact{
				Identity: "estimate-progress-2", Reporter: ReporterAgentRun,
				Scope: input.Stages[1].Scope, Revision: 2, Sequence: 2,
				Source: SourceAgent, ServerReceivedAt: fixtureNow,
				Confidence: 0.9, Basis: "lower progress", ProgressPercent: &latestProgress,
			},
			EstimateFact{
				Identity: "estimate-eta-3", Reporter: ReporterAgentRun,
				Scope: input.Stages[1].Scope, Revision: 3, Sequence: 3,
				Source: SourceAgent, ServerReceivedAt: fixtureNow,
				Confidence: 0.9, Basis: "ETA only", ETA: etaRange(900, 1_100, 1_000),
			},
		)
		return input
	}

	firstInput := build(20)
	first := mustEvaluate(t, firstInput)
	second := mustEvaluate(t, build(30))
	assertIntPointer(t, first.ProgressPercent, 46)
	assertIntPointer(t, second.ProgressPercent, 46)
	if first.OwnerSource == nil || first.OwnerSource.Identity != "estimate-eta-3" ||
		first.ProgressSource == nil || first.ProgressSource.Identity != "estimate-implementation-1" ||
		!slices.Contains(first.Flags, FlagSourceBackslideIgnored) {
		t.Fatalf("80→20→ETA-only determinants incorrect: %+v", first)
	}
	if first.TrustRevision == second.TrustRevision {
		t.Fatal("latest eligible progress fact was omitted from trust revision")
	}
	slices.Reverse(firstInput.Stages[1].Estimates)
	permuted := mustEvaluate(t, firstInput)
	if first.TrustRevision != permuted.TrustRevision {
		t.Fatalf("estimate permutation changed three-determinant hash: %s != %s", first.TrustRevision, permuted.TrustRevision)
	}
}

func TestTypedScopeResetMayLowerProgress(t *testing.T) {
	input := fixtureInput(1)
	eighty := 80.0
	input.Stages[1].Estimates[0].ProgressPercent = &eighty
	oldScope := input.Stages[1].Scope
	input.Stages[1].Scope.ResetID = "reset-implementation-2"
	twenty := 20.0
	input.Stages[1].Estimates = append(input.Stages[1].Estimates, EstimateFact{
		Identity:         "estimate-after-reset",
		Reporter:         ReporterAgentRun,
		Scope:            input.Stages[1].Scope,
		Revision:         2,
		Sequence:         2,
		Source:           SourceAgent,
		ServerReceivedAt: fixtureNow,
		Confidence:       0.9,
		Basis:            "typed reset milestone",
		ProgressPercent:  &twenty,
		ETA:              etaRange(900, 1_100, 1_000),
	})
	if input.Stages[1].Estimates[0].Scope != oldScope {
		t.Fatal("fixture unexpectedly mutated the stale fact scope")
	}
	output := mustEvaluate(t, input)
	assertIntPointer(t, output.ProgressPercent, 19)
	if slices.Contains(output.Flags, FlagSourceBackslideIgnored) {
		t.Fatalf("backslide incorrectly crossed reset scope: %v", output.Flags)
	}
}

func TestEveryAuthorityScopeIdentityFencesPriorProgress(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*Input)
	}{
		{"attempt", func(input *Input) {
			for i := range input.Stages {
				input.Stages[i].Scope.AttemptID = "attempt-2"
			}
		}},
		{"plan", func(input *Input) {
			for i := range input.Stages {
				input.Stages[i].Scope.PlanID = "plan-2"
			}
		}},
		{"execution", func(input *Input) { input.Stages[1].Scope.ExecutionID = "execution-implementation-2" }},
		{"authority", func(input *Input) { input.Stages[1].Scope.AuthorityID = "authority-implementation-2" }},
		{"typed reset", func(input *Input) { input.Stages[1].Scope.ResetID = "reset-implementation-2" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			input := fixtureInput(1)
			eighty := 80.0
			input.Stages[1].Estimates[0].ProgressPercent = &eighty
			mutation.mutate(&input)
			twenty := 20.0
			input.Stages[1].Estimates = append(input.Stages[1].Estimates, EstimateFact{
				Identity: "estimate-new-scope", Reporter: ReporterAgentRun,
				Scope: input.Stages[1].Scope, Revision: 2, Sequence: 2,
				Source: SourceAgent, ServerReceivedAt: fixtureNow,
				Confidence: 0.9, Basis: "new authority scope", ProgressPercent: &twenty,
				ETA: etaRange(900, 1_100, 1_000),
			})
			output := mustEvaluate(t, input)
			assertIntPointer(t, output.ProgressPercent, 19)
			if output.ProgressSource == nil || output.ProgressSource.Identity != "estimate-new-scope" ||
				slices.Contains(output.Flags, FlagSourceBackslideIgnored) {
				t.Fatalf("stale scope contributed: %+v", output)
			}
		})
	}
}

func TestReporterMetadataChangeRequiresNewAuthorityEpoch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StageInput)
	}{
		{"reporter kind", func(stage *StageInput) {
			stage.Reporter = ReporterExternal
			stage.Scope.ReporterID = "external-reporter"
			stage.Scope.RunLinkID = ""
		}},
		{"reporter identity", func(stage *StageInput) { stage.Scope.ReporterID = "reporter-implementation-2" }},
		{"run link", func(stage *StageInput) { stage.Scope.RunLinkID = "run-link-implementation-2" }},
		{"typed reset cannot disguise reporter identity change", func(stage *StageInput) {
			stage.Scope.ResetID = "reset-implementation-2"
			stage.Scope.ReporterID = "reporter-implementation-2"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixtureInput(1)
			test.mutate(&input.Stages[1])
			if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("metadata-only handoff error=%v", err)
			}
		})
	}
}

func TestLatestInvalidExpiredAndProgressOnlyFallBackOnlyToHistory(t *testing.T) {
	tests := []struct {
		name       string
		latest     func(Input) EstimateFact
		wantFlag   Flag
		wantSource bool
	}{
		{
			name: "progress only",
			latest: func(input Input) EstimateFact {
				progress := 60.0
				return EstimateFact{
					Identity: "latest-progress-only", Reporter: ReporterAgentRun, Scope: input.Stages[1].Scope,
					Revision: 2, Sequence: 2, Source: SourceAgent, ServerReceivedAt: fixtureNow,
					Confidence: 0.9, Basis: "progress only", ProgressPercent: &progress,
				}
			},
			wantSource: true,
		},
		{
			name: "explicit reporter history source rejected",
			latest: func(input Input) EstimateFact {
				progress := 60.0
				return EstimateFact{
					Identity: "latest-fake-history", Reporter: ReporterAgentRun, Scope: input.Stages[1].Scope,
					Revision: 2, Sequence: 2, Source: SourceHistory, ServerReceivedAt: fixtureNow,
					Confidence: 0.9, Basis: "not computed history", ProgressPercent: &progress,
					ETA: etaRange(1, 2, 1),
				}
			},
			wantFlag: FlagOwnerEstimateInvalid,
		},
		{
			name: "invalid confidence",
			latest: func(input Input) EstimateFact {
				fact := input.Stages[1].Estimates[0]
				fact.Identity, fact.Revision, fact.Sequence, fact.Confidence = "latest-invalid", 2, 2, 0
				return fact
			},
			wantFlag: FlagOwnerEstimateInvalid,
		},
		{
			name: "invalid progress payload",
			latest: func(input Input) EstimateFact {
				fact := input.Stages[1].Estimates[0]
				invalid := math.NaN()
				fact.Identity, fact.Revision, fact.Sequence, fact.ProgressPercent = "latest-progress-invalid", 2, 2, &invalid
				return fact
			},
			wantFlag: FlagOwnerEstimateInvalid,
		},
		{
			name: "invalid bounds",
			latest: func(input Input) EstimateFact {
				fact := input.Stages[1].Estimates[0]
				fact.Identity, fact.Revision, fact.Sequence = "latest-bounds", 2, 2
				fact.ETA = etaRange(2, 1, 1)
				return fact
			},
			wantFlag:   FlagOwnerEstimateInvalid,
			wantSource: true,
		},
		{
			name: "expired pessimistic bound",
			latest: func(input Input) EstimateFact {
				fact := input.Stages[1].Estimates[0]
				fact.Identity, fact.Revision, fact.Sequence = "latest-expired", 2, 2
				fact.ServerReceivedAt = fixtureNow.Add(-200 * time.Second)
				fact.ETA = etaRange(50, 100, 75)
				return fact
			},
			wantFlag:   FlagOwnerEstimateExpired,
			wantSource: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixtureInput(1)
			input.Stages[1].Estimates = append(input.Stages[1].Estimates, test.latest(input))
			output := mustEvaluate(t, input)
			if output.Suppression != "" || output.Contributors[0].Kind != ContributorHistory {
				t.Fatalf("latest fact reused or history fallback failed: suppression=%s contributor=%s", output.Suppression, output.Contributors[0].Kind)
			}
			if test.wantFlag != "" && !slices.Contains(output.Flags, test.wantFlag) {
				t.Fatalf("flags %v do not contain %s", output.Flags, test.wantFlag)
			}
			if test.wantSource != (output.OwnerSource != nil) {
				t.Fatalf("owner source present=%v, want %v", output.OwnerSource != nil, test.wantSource)
			}
		})
	}
}

func TestCurrentOwnerCoversSparseCurrentHistoryButNotFutureHistory(t *testing.T) {
	input := fixtureInput(1)
	input.Stages[1].History = nil
	output := mustEvaluate(t, input)
	if output.Suppression != "" || output.Contributors[0].Kind != ContributorAgent {
		t.Fatalf("valid owner did not cover current stage: suppression=%s contributor=%s", output.Suppression, output.Contributors[0].Kind)
	}
	if !slices.Contains(output.Contributors[0].Flags, FlagHistoryInsufficientBasis) {
		t.Fatalf("optional history diagnostics missing: %v", output.Contributors[0].Flags)
	}
	diagnostic := requireStageDiagnostic(t, output, StageImplementation)
	if !diagnostic.CurrentStage || !diagnostic.Covered || diagnostic.Failure != "" ||
		diagnostic.RawSampleCount != 0 || diagnostic.InlierSampleCount != 0 || diagnostic.RejectedSampleCount != 0 ||
		!slices.Contains(diagnostic.Flags, FlagHistoryInsufficientBasis) {
		t.Fatalf("covered sparse-current diagnostic=%+v", diagnostic)
	}

	input = fixtureInput(1)
	input.Stages[1].History = historySamples(StageImplementation, 4, 1_000)
	output = mustEvaluate(t, input)
	diagnostic = requireStageDiagnostic(t, output, StageImplementation)
	if output.Suppression != "" || !diagnostic.Covered || diagnostic.Failure != "" ||
		diagnostic.RawSampleCount != 4 || diagnostic.InlierSampleCount != 0 || diagnostic.RejectedSampleCount != 0 ||
		!slices.Contains(diagnostic.Flags, FlagHistoryInsufficientBasis) {
		t.Fatalf("covered four-row current diagnostic=%+v output_suppression=%s", diagnostic, output.Suppression)
	}

	input = fixtureInput(1)
	input.Stages[2].History = nil
	output = mustEvaluate(t, input)
	if output.Suppression != SuppressMissingContributor || output.OptimisticLandingAt != nil {
		t.Fatalf("future missing history not suppressed: %+v", output)
	}
	diagnostic = requireStageDiagnostic(t, output, StageQA)
	if diagnostic.CurrentStage || diagnostic.Covered || diagnostic.Failure != SuppressMissingContributor ||
		diagnostic.RawSampleCount != 0 || diagnostic.InlierSampleCount != 0 || diagnostic.RejectedSampleCount != 0 {
		t.Fatalf("uncovered missing-future diagnostic=%+v", diagnostic)
	}

	input = fixtureInput(1)
	input.Stages[2].History = historySamples(StageQA, 1, 1_000)
	output = mustEvaluate(t, input)
	if output.Suppression != SuppressInsufficientBasis || output.OptimisticLandingAt != nil {
		t.Fatalf("future low-basis history not suppressed: %+v", output)
	}
	diagnostic = requireStageDiagnostic(t, output, StageQA)
	if diagnostic.Covered || diagnostic.Failure != SuppressInsufficientBasis ||
		diagnostic.RawSampleCount != 1 || diagnostic.InlierSampleCount != 0 || diagnostic.RejectedSampleCount != 0 ||
		!slices.Contains(diagnostic.Flags, FlagHistoryInsufficientBasis) {
		t.Fatalf("uncovered sparse-future diagnostic=%+v", diagnostic)
	}
}

func TestMissingCurrentContributorIsDistinctFromLowBasis(t *testing.T) {
	input := fixtureInput(1)
	input.Stages[1].Estimates = nil
	input.Stages[1].History = nil
	output := mustEvaluate(t, input)
	if output.Suppression != SuppressMissingContributor {
		t.Fatalf("zero-row current contributor suppression=%s", output.Suppression)
	}
	diagnostic := requireStageDiagnostic(t, output, StageImplementation)
	if diagnostic.Covered || diagnostic.Failure != SuppressMissingContributor || diagnostic.RawSampleCount != 0 {
		t.Fatalf("zero-row current diagnostic=%+v", diagnostic)
	}

	input.Stages[1].History = historySamples(StageImplementation, 4, 1_000)
	output = mustEvaluate(t, input)
	if output.Suppression != SuppressInsufficientBasis {
		t.Fatalf("four-row current contributor suppression=%s", output.Suppression)
	}
	diagnostic = requireStageDiagnostic(t, output, StageImplementation)
	if diagnostic.Covered || diagnostic.Failure != SuppressInsufficientBasis ||
		diagnostic.RawSampleCount != 4 || diagnostic.InlierSampleCount != 0 {
		t.Fatalf("four-row current diagnostic=%+v", diagnostic)
	}
}

func TestExpiredOwnerOutranksSparseHistory(t *testing.T) {
	input := fixtureInput(1)
	fact := &input.Stages[1].Estimates[0]
	fact.ServerReceivedAt = fixtureNow.Add(-200 * time.Second)
	fact.ETA = etaRange(50, 100, 75)
	input.Stages[1].History = nil
	output := mustEvaluate(t, input)
	if output.Suppression != SuppressEstimateExpired {
		t.Fatalf("suppression=%s, want estimate_expired", output.Suppression)
	}
}

func TestOutlierHeavyFutureStageSuppressesWholeLanding(t *testing.T) {
	input := fixtureInput(1)
	values := append(repeatValue(4, 0), repeatValue(11, 100)...)
	values = append(values, repeatValue(5, 1_000)...)
	input.Stages[2].History = samplesWithValues(StageQA, values)
	output := mustEvaluate(t, input)
	if output.Suppression != SuppressOutlierHeavy || output.OptimisticLandingAt != nil ||
		!slices.Contains(output.Flags, FlagHistoryOutlierHeavy) {
		t.Fatalf("outlier-heavy future stage output=%+v", output)
	}
	diagnostic := requireStageDiagnostic(t, output, StageQA)
	if diagnostic.Covered || diagnostic.Failure != SuppressOutlierHeavy ||
		diagnostic.RawSampleCount != 20 || diagnostic.InlierSampleCount != 11 || diagnostic.RejectedSampleCount != 9 ||
		!slices.Contains(diagnostic.Flags, FlagHistoryOutlierHeavy) {
		t.Fatalf("outlier-heavy future diagnostic=%+v", diagnostic)
	}
}

func TestOptionalOutlierHeavyCurrentHistoryRemainsCoveredDiagnostic(t *testing.T) {
	input := fixtureInput(1)
	// Current-stage residuals subtract the fixture's exact 100 elapsed seconds;
	// these full-lead values therefore preserve the 0/100/1,000 outlier shape.
	values := append(repeatValue(4, 100), repeatValue(11, 200)...)
	values = append(values, repeatValue(5, 1_100)...)
	input.Stages[1].History = samplesWithValues(StageImplementation, values)
	output := mustEvaluate(t, input)
	if output.Suppression != "" || len(output.Contributors) == 0 || output.Contributors[0].Kind != ContributorAgent {
		t.Fatalf("optional bad current history suppressed valid owner: %+v", output)
	}
	diagnostic := requireStageDiagnostic(t, output, StageImplementation)
	if !diagnostic.CurrentStage || !diagnostic.Covered || diagnostic.Failure != "" ||
		diagnostic.RawSampleCount != 20 || diagnostic.InlierSampleCount != 11 || diagnostic.RejectedSampleCount != 9 ||
		!slices.Contains(diagnostic.Flags, FlagHistoryOutlierHeavy) {
		t.Fatalf("covered outlier-heavy current diagnostic=%+v", diagnostic)
	}
	for _, contributor := range output.Contributors {
		if contributor.Stage == StageImplementation && contributor.Kind == ContributorHistory {
			t.Fatalf("ineligible history emitted as contributor: %+v", contributor)
		}
	}
}

func TestAuthorityHandoffRejectsReusedRunTelemetry(t *testing.T) {
	input := fixtureInput(1)
	stage := &input.Stages[1]
	staleAgent := stage.Estimates[0]
	stage.Reporter = ReporterExternal
	stage.Scope.AuthorityID = "authority-implementation-2"
	stage.Scope.ReporterID = "external-reporter"
	stage.Scope.RunLinkID = ""
	externalProgress := 30.0
	stage.Estimates = []EstimateFact{staleAgent, {
		Identity: "external-estimate", Reporter: ReporterExternal, Scope: stage.Scope,
		Revision: 1, Sequence: 1, Source: SourceExternal, ServerReceivedAt: fixtureNow,
		Confidence: 0.8, Basis: "external authority estimate", ProgressPercent: &externalProgress,
		ETA: etaRange(900, 1_100, 1_000),
	}}
	output := mustEvaluate(t, input)
	assertIntPointer(t, output.ProgressPercent, 23)
	if output.ProgressSource == nil || output.ProgressSource.Identity != "external-estimate" {
		t.Fatalf("reused run telemetry contributed: %+v", output.ProgressSource)
	}
	if output.OwnerSource == nil || output.OwnerSource.ReporterKind != ReporterExternal ||
		output.Contributors[0].Source == nil || output.Contributors[0].Source.ReporterKind != ReporterExternal {
		t.Fatalf("exact current external ETA lost eligibility: owner=%+v contributor=%+v", output.OwnerSource, output.Contributors[0])
	}
}

func TestReporterSourceAttributionMustMatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StageInput)
	}{
		{"agent cannot claim external source", func(stage *StageInput) {
			stage.Estimates[0].Source = SourceExternal
		}},
		{"external cannot claim agent source", func(stage *StageInput) {
			stage.Reporter = ReporterExternal
			stage.Scope.ReporterID = "external-reporter"
			stage.Scope.RunLinkID = ""
			stage.Estimates[0].Reporter = ReporterExternal
			stage.Estimates[0].Scope = stage.Scope
			stage.Estimates[0].Source = SourceAgent
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixtureInput(1)
			test.mutate(&input.Stages[1])
			output := mustEvaluate(t, input)
			if output.OwnerSource != nil || output.ProgressSource != nil || output.Contributors[0].Kind != ContributorHistory ||
				!slices.Contains(output.Flags, FlagOwnerEstimateInvalid) {
				t.Fatalf("mismatched attribution trusted: %+v", output)
			}
		})
	}
}

func TestEstimateFactRequiresAValueButAllowsETAOnly(t *testing.T) {
	input := fixtureInput(1)
	input.Stages[1].Estimates[0].ProgressPercent = nil
	output := mustEvaluate(t, input)
	if output.OwnerSource == nil || output.Contributors[0].Kind != ContributorAgentHistory {
		t.Fatalf("ETA-only estimate rejected: %+v", output)
	}

	input.Stages[1].Estimates[0].ETA = nil
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("basis-only estimate error=%v", err)
	}
}

func TestDuplicateEstimateIdentityAndOrderKeysAreRejected(t *testing.T) {
	t.Run("exact duplicate", func(t *testing.T) {
		input := fixtureInput(1)
		input.Stages[1].Estimates = append(input.Stages[1].Estimates, input.Stages[1].Estimates[0])
		if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("exact duplicate error=%v", err)
		}
	})

	t.Run("identity duplicate across authority scopes", func(t *testing.T) {
		input := fixtureInput(1)
		old := input.Stages[1].Estimates[0]
		input.Stages[1].Scope.AuthorityID = "authority-implementation-2"
		conflict := old
		conflict.Scope = input.Stages[1].Scope
		conflict.Revision, conflict.Sequence = 2, 2
		input.Stages[1].Estimates = append(input.Stages[1].Estimates, conflict)
		if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cross-scope duplicate identity error=%v", err)
		}
	})

	t.Run("conflicting duplicate order key", func(t *testing.T) {
		input := fixtureInput(1)
		conflict := input.Stages[1].Estimates[0]
		conflict.Identity = "conflicting-same-order"
		progress := 99.0
		conflict.ProgressPercent = &progress
		input.Stages[1].Estimates = append(input.Stages[1].Estimates, conflict)
		if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("conflicting order key error=%v", err)
		}
		slices.Reverse(input.Stages[1].Estimates)
		if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("permuted conflicting order key error=%v", err)
		}
	})

	t.Run("same order in a new authority is valid and deterministic", func(t *testing.T) {
		input := fixtureInput(1)
		input.Stages[1].Scope.AuthorityID = "authority-implementation-2"
		progress := 20.0
		input.Stages[1].Estimates = append(input.Stages[1].Estimates, EstimateFact{
			Identity: "new-authority-same-order", Reporter: ReporterAgentRun,
			Scope: input.Stages[1].Scope, Revision: 1, Sequence: 1,
			Source: SourceAgent, ServerReceivedAt: fixtureNow,
			Confidence: 0.9, Basis: "new authority", ProgressPercent: &progress,
			ETA: etaRange(900, 1_100, 1_000),
		})
		first := mustEvaluate(t, input)
		slices.Reverse(input.Stages[1].Estimates)
		second := mustEvaluate(t, input)
		if first.TrustRevision != second.TrustRevision || *first.ProgressPercent != *second.ProgressPercent {
			t.Fatalf("cross-authority order/permutation changed output: first=%+v second=%+v", first, second)
		}
	})
}

func TestIneligibleOwnerIsUnknownNotZeroOrOptimistic(t *testing.T) {
	input := fixtureInput(1)
	stage := &input.Stages[1]
	stage.Reporter = ReporterUser
	stage.Scope.ReporterID = "user-owner"
	stage.Scope.RunLinkID = ""
	stage.Estimates = nil
	output := mustEvaluate(t, input)
	if output.Suppression != SuppressUnknownReporter {
		t.Fatalf("suppression=%s, want unknown_reporter", output.Suppression)
	}
	assertIntPointer(t, output.ProgressPercent, 10)
	if output.OwnerSource != nil || output.ProgressSource != nil || output.LandingAt != nil {
		t.Fatalf("ineligible owner produced trusted output: %+v", output)
	}

	legacy := Input{
		DeliveryIdentity: "delivery-legacy", ProjectIdentity: "project-1",
		CalculatedAt: fixtureNow, Instrumented: false,
	}
	output = mustEvaluate(t, legacy)
	if output.ProgressKnown || output.ProgressPercent != nil || output.Suppression != SuppressUnknownReporter {
		t.Fatalf("legacy delivery was assigned optimistic defaults: %+v", output)
	}
}

func TestSuppressionStatesAndExactPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StageInput)
		want   SuppressionCode
	}{
		{"cancelled", func(stage *StageInput) { stage.Completion.Status = CompletionCancelled }, SuppressCancelled},
		{"failed", func(stage *StageInput) { stage.Completion.Status = CompletionFailed }, SuppressTerminalFailed},
		{"conflict", func(stage *StageInput) { stage.Completion.Status = CompletionConflict }, SuppressTerminalFailed},
		{"waiting", func(stage *StageInput) { stage.Signals.WaitingOnHuman = true }, SuppressWaitingOnHuman},
		{"blocked", func(stage *StageInput) { stage.Signals.Blocked = true }, SuppressBlocked},
		{"stale", func(stage *StageInput) { stage.Signals.Stale = true }, SuppressStale},
		{"unknown", func(stage *StageInput) { stage.Signals.UnknownReporter = true }, SuppressUnknownReporter},
		{"no signal", func(stage *StageInput) { stage.Signals.NoSignal = true }, SuppressNoSignal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixtureInput(1)
			test.mutate(&input.Stages[1])
			output := mustEvaluate(t, input)
			if output.Suppression != test.want || output.ProgressPercent == nil || output.OptimisticLandingAt != nil {
				t.Fatalf("suppressed output = %+v, want %s with factual progress", output, test.want)
			}
		})
	}

	precedence := []SuppressionCode{
		SuppressTerminalComplete, SuppressCancelled, SuppressTerminalFailed,
		SuppressWaitingOnHuman, SuppressBlocked, SuppressStale,
		SuppressUnknownReporter, SuppressNoSignal, SuppressEstimateExpired,
		SuppressOutlierHeavy, SuppressInsufficientBasis, SuppressMissingContributor,
	}
	for i, earlier := range precedence {
		for _, later := range precedence[i+1:] {
			if got := preferredSuppression(later, earlier); got != earlier {
				t.Fatalf("precedence(%s,%s)=%s", later, earlier, got)
			}
			if got := preferredSuppression(earlier, later); got != earlier {
				t.Fatalf("precedence(%s,%s)=%s", earlier, later, got)
			}
		}
	}
}

func TestLowConfidenceAndMissingPointAreRangeOnlyWithoutMidpoint(t *testing.T) {
	input := fixtureInput(1)
	input.Stages[1].History = nil
	input.Stages[1].Estimates[0].Confidence = 0.25
	output := mustEvaluate(t, input)
	if !output.RangeOnly || output.LandingAt != nil || output.RemainingSeconds != nil ||
		output.OptimisticLandingAt == nil || output.PessimisticLandingAt == nil {
		t.Fatalf("low-confidence output made an exact promise: %+v", output)
	}

	input = fixtureInput(1)
	input.Stages[1].History = nil
	input.Stages[1].Estimates[0].ETA.PointSeconds = nil
	output = mustEvaluate(t, input)
	if !output.RangeOnly || output.LandingAt != nil || output.RemainingSeconds != nil {
		t.Fatalf("missing point synthesized a midpoint: %+v", output)
	}

	input = fixtureInput(1)
	input.Stages[1].History = historySamples(StageImplementation, 5, 200)
	output = mustEvaluate(t, input)
	if output.Suppression != "" || !output.RangeOnly || output.LandingAt != nil ||
		output.Confidence == nil || *output.Confidence != 0 || output.ConfidenceLabel != ConfidenceUnknown ||
		len(output.Contributors) == 0 ||
		output.Contributors[0].Confidence != 0 || output.Contributors[0].ConfidenceLabel != ConfidenceUnknown ||
		!slices.Contains(output.Contributors[0].Flags, FlagAgentHistoryDisagreement) {
		t.Fatalf("low-history disjoint downgrade was strengthened by later contributors: %+v", output)
	}
}

func TestTrustRevisionIsImmutableClockIndependentAndPermutationInvariant(t *testing.T) {
	base := fixtureInput(1)
	base.Stages[0].Completion.EvidenceIdentities = []string{"evidence-extra", "evidence-specification"}
	first := mustEvaluate(t, base)

	clock := fixtureInput(1)
	clock.Stages[0].Completion.EvidenceIdentities = []string{"evidence-specification", "evidence-extra"}
	clock.CalculatedAt = clock.CalculatedAt.Add(10 * time.Minute)
	clock.Stages[1].Signals.TransitionAt = []time.Time{clock.CalculatedAt.Add(time.Minute)}
	for i := range clock.Stages {
		slices.Reverse(clock.Stages[i].History)
	}
	second := mustEvaluate(t, clock)
	if first.TrustRevision != second.TrustRevision {
		t.Fatalf("clock/permutation changed immutable revision: %s != %s", first.TrustRevision, second.TrustRevision)
	}
	if first.ServerTime.Equal(second.ServerTime) || first.RemainingMinimumSeconds == nil || second.RemainingMinimumSeconds == nil ||
		*first.RemainingMinimumSeconds == *second.RemainingMinimumSeconds {
		t.Fatal("clock-derived outputs did not independently evolve")
	}
	clock.Stages[1].Signals.Stale = true
	clock.Stages[1].Signals.NoSignal = true
	clockSuppressed := mustEvaluate(t, clock)
	if second.TrustRevision != clockSuppressed.TrustRevision || clockSuppressed.Suppression != SuppressStale {
		t.Fatalf("clock-only classification changed immutable trust: second=%+v suppressed=%+v", second, clockSuppressed)
	}

	mutations := []struct {
		name   string
		mutate func(*Input)
	}{
		{"reset", func(input *Input) {
			input.Stages[1].Scope.ResetID = "reset-implementation-next"
			input.Stages[1].Estimates[0].Scope = input.Stages[1].Scope
		}},
		{"source revision", func(input *Input) { input.Stages[1].Estimates[0].Revision++ }},
		{"history value", func(input *Input) {
			input.Stages[2].History[0].FullLeadSeconds++
			input.Stages[2].History[0].ActiveSeconds++
		}},
		{"plan policy", func(input *Input) { input.Policy[2].Identity = "policy-qa-revised" }},
		{"reporter identity", func(input *Input) {
			input.Stages[1].Scope.ReporterID = "reporter-replacement"
			input.Stages[1].Estimates[0].Scope = input.Stages[1].Scope
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			input := fixtureInput(1)
			input.Stages[0].Completion.EvidenceIdentities = []string{"evidence-extra", "evidence-specification"}
			mutation.mutate(&input)
			if got := mustEvaluate(t, input).TrustRevision; got == first.TrustRevision {
				t.Fatalf("immutable %s did not change revision", mutation.name)
			}
		})
	}
}

func TestOutputIsPrivacySafeAndRejectsSecretLikeBasis(t *testing.T) {
	input := fixtureInput(1)
	input.Stages[1].Scope.ReporterID = "private-reporter-key"
	input.Stages[1].Scope.RunLinkID = "private-run-link"
	input.Stages[1].Estimates[0].Scope = input.Stages[1].Scope
	input.Stages[1].History[0].Identity = "private-sample-identity"
	output := mustEvaluate(t, input)
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-reporter-key", "private-run-link", "private-sample-identity"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("privacy-sensitive identity leaked in output: %s", forbidden)
		}
	}

	input = fixtureInput(1)
	input.Stages[1].Estimates[0].Basis = "api_key=super-secret-value"
	if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("secret-like basis error=%v, want ErrInvalidInput", err)
	}
}

func TestClockSkewCannotEnterEvaluation(t *testing.T) {
	input := fixtureInput(1)
	output := mustEvaluate(t, input)
	if !output.ServerTime.Equal(input.CalculatedAt) || !output.CalculatedAt.Equal(input.CalculatedAt) {
		t.Fatalf("server time contract incorrect: server=%v calculated=%v", output.ServerTime, output.CalculatedAt)
	}
	want := input.CalculatedAt.Add(time.Duration(*output.RemainingSeconds) * time.Second)
	if output.LandingAt == nil || !output.LandingAt.Equal(want) {
		t.Fatalf("landing=%v, want server anchored %v", output.LandingAt, want)
	}
	// A browser can be hours ahead or behind; there is intentionally no viewer
	// clock in Input, so both must use server_time + remaining_seconds.
	for _, viewer := range []time.Time{fixtureNow.Add(-12 * time.Hour), fixtureNow.Add(17 * time.Hour)} {
		_ = viewer
		if got := output.ServerTime.Add(time.Duration(*output.RemainingSeconds) * time.Second); !got.Equal(*output.LandingAt) {
			t.Fatalf("viewer skew affected landing: %v", got)
		}
	}
}

func TestElapsedSecondsAndOwnerRangeFloorSubsecondsConservatively(t *testing.T) {
	tests := []struct {
		name  string
		start time.Time
		end   time.Time
		want  int64
	}{
		{"negative", fixtureNow, fixtureNow.Add(-time.Nanosecond), 0},
		{"zero", fixtureNow, fixtureNow, 0},
		{"two hundred milliseconds", fixtureNow.Add(-200 * time.Millisecond), fixtureNow, 0},
		{"just below one second", fixtureNow.Add(-time.Second + time.Nanosecond), fixtureNow, 0},
		{"exactly one second", fixtureNow.Add(-time.Second), fixtureNow, 1},
		{"one point two seconds", fixtureNow.Add(-time.Second - 200*time.Millisecond), fixtureNow, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nonnegativeSecondsBetween(test.start, test.end); got != test.want {
				t.Fatalf("seconds(%s,%s)=%d, want %d", test.start, test.end, got, test.want)
			}
		})
	}

	base := fixtureInput(1).Stages[1].Estimates[0]
	base.ETA = etaRange(1, 2, 2)

	base.ServerReceivedAt = fixtureNow.Add(-200 * time.Millisecond)
	rangeValue, _, _, pointAt, valid, expired := anchoredRange(base, fixtureNow)
	if !valid || expired || rangeValue.MinimumSeconds != 1 || rangeValue.MaximumSeconds != 2 ||
		rangeValue.PointSeconds == nil || *rangeValue.PointSeconds != 2 || pointAt == nil {
		t.Fatalf("subsecond owner range=%+v point_at=%v valid=%v expired=%v", rangeValue, pointAt, valid, expired)
	}

	base.ServerReceivedAt = fixtureNow.Add(-2*time.Second + time.Nanosecond)
	rangeValue, _, pessimistic, pointAt, valid, expired := anchoredRange(base, fixtureNow)
	if !valid || expired || !pessimistic.Equal(fixtureNow.Add(time.Nanosecond)) ||
		rangeValue.MinimumSeconds != 0 || rangeValue.MaximumSeconds != 1 ||
		rangeValue.PointSeconds == nil || *rangeValue.PointSeconds != 1 || pointAt == nil {
		t.Fatalf("pre-expiry owner range=%+v pessimistic=%v point_at=%v valid=%v expired=%v",
			rangeValue, pessimistic, pointAt, valid, expired)
	}

	base.ServerReceivedAt = fixtureNow.Add(-2 * time.Second)
	_, _, pessimistic, pointAt, valid, expired = anchoredRange(base, fixtureNow)
	if !valid || !expired || !pessimistic.Equal(fixtureNow) || pointAt != nil {
		t.Fatalf("exact-expiry pessimistic=%v point_at=%v valid=%v expired=%v", pessimistic, pointAt, valid, expired)
	}
}

func TestNextTransitionIncludesPointExpiryAndProvidedFreshness(t *testing.T) {
	input := fixtureInput(1)
	input.Stages[1].Estimates[0].ETA = etaRange(0, 1_000, 500)
	output := mustEvaluate(t, input)
	if output.NextTrustTransitionAt == nil || !output.NextTrustTransitionAt.Equal(fixtureNow.Add(500*time.Second)) {
		t.Fatalf("point transition=%v, want +500s", output.NextTrustTransitionAt)
	}
	input.Stages[1].Signals.TransitionAt = []time.Time{fixtureNow.Add(30 * time.Second)}
	output = mustEvaluate(t, input)
	if output.NextTrustTransitionAt == nil || !output.NextTrustTransitionAt.Equal(fixtureNow.Add(30*time.Second)) {
		t.Fatalf("freshness transition=%v, want +30s", output.NextTrustTransitionAt)
	}
}

func TestEvaluateRejectsMalformedContractsAndOverflow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
	}{
		{"stage order", func(input *Input) { input.Stages[1].Stage = StageQA }},
		{"cross plan", func(input *Input) { input.Stages[2].Scope.PlanID = "plan-other" }},
		{"future started", func(input *Input) {
			started := fixtureNow.Add(-time.Second)
			input.Stages[2].ExecutionStartedAt = &started
			input.Stages[2].Scope.ExecutionID = "future-execution"
			input.Stages[2].Scope.AuthorityID = "future-authority"
			input.Stages[2].Scope.ResetID = "future-reset"
			input.Stages[2].Scope.ReporterID = "future-reporter"
		}},
		{"completion without evidence", func(input *Input) {
			input.Stages[1].Completion = CompletionInput{Status: CompletionSucceeded, Eligible: true}
		}},
		{"component sum overflow", func(input *Input) {
			sample := &input.Stages[2].History[0]
			sample.FullLeadSeconds = math.MaxInt64
			sample.ActiveSeconds = math.MaxInt64
			sample.BlockedSeconds = 1
		}},
		{"landing sum overflow", func(input *Input) {
			for stageIndex := 2; stageIndex < 5; stageIndex++ {
				for sampleIndex := range input.Stages[stageIndex].History {
					input.Stages[stageIndex].History[sampleIndex].FullLeadSeconds = math.MaxInt64
					input.Stages[stageIndex].History[sampleIndex].ActiveSeconds = math.MaxInt64
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixtureInput(1)
			test.mutate(&input)
			if _, err := Evaluate(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestSourceConfidenceBoundaries(t *testing.T) {
	tests := []struct {
		value float64
		label ConfidenceLabel
		valid bool
	}{
		{0, ConfidenceUnknown, false},
		{math.SmallestNonzeroFloat64, ConfidenceLow, true},
		{math.Nextafter(0.5, 0), ConfidenceLow, true},
		{0.5, ConfidenceMedium, true},
		{math.Nextafter(0.8, 0), ConfidenceMedium, true},
		{0.8, ConfidenceHigh, true},
		{1, ConfidenceHigh, true},
		{math.Nextafter(1, 2), ConfidenceUnknown, false},
		{math.NaN(), ConfidenceUnknown, false},
		{math.Inf(1), ConfidenceUnknown, false},
	}
	for _, test := range tests {
		label, valid := classifySourceConfidence(test.value)
		if label != test.label || valid != test.valid {
			t.Fatalf("confidence(%v)=(%s,%v), want (%s,%v)", test.value, label, valid, test.label, test.valid)
		}
	}
}

func fixtureInput(currentIndex int) Input {
	input := Input{
		DeliveryIdentity: "delivery-1",
		ProjectIdentity:  "project-1",
		Instrumented:     true,
		CalculatedAt:     fixtureNow,
		PolicyVersion:    EstimatorPolicyVersion,
		Policy:           DefaultPolicy(),
		Stages:           make([]StageInput, len(canonicalStages)),
	}
	for i, stageKey := range canonicalStages {
		stage := StageInput{
			Stage:      stageKey,
			Scope:      Scope{AttemptID: "attempt-1", PlanID: "plan-1"},
			Reporter:   ReporterUnknown,
			Completion: CompletionInput{Status: CompletionPending},
		}
		if i < currentIndex {
			started := fixtureNow.Add(-time.Duration(i+2) * time.Hour)
			stage.ExecutionStartedAt = &started
			stage.Reporter = ReporterAgentRun
			stage.Scope = startedScope(i)
			stage.Completion = successfulCompletion(i)
		} else if i == currentIndex && currentIndex < len(canonicalStages) {
			started := fixtureNow.Add(-100 * time.Second)
			stage.ExecutionStartedAt = &started
			stage.Reporter = ReporterAgentRun
			stage.Scope = startedScope(i)
			stage.Signals.SemanticIdentity = fmt.Sprintf("activity-%s", stageKey)
			progress := 50.0
			stage.Estimates = []EstimateFact{{
				Identity:         fmt.Sprintf("estimate-%s-1", stageKey),
				Reporter:         ReporterAgentRun,
				Scope:            stage.Scope,
				Revision:         1,
				Sequence:         1,
				Source:           SourceAgent,
				ServerReceivedAt: fixtureNow,
				Confidence:       0.9,
				Basis:            "current owner estimate",
				ProgressPercent:  &progress,
				ETA:              etaRange(900, 1_100, 1_000),
			}}
		}
		if i >= currentIndex && currentIndex < len(canonicalStages) {
			stage.History = historySamples(stageKey, 10, 1_000)
		}
		input.Stages[i] = stage
	}
	return input
}

func startedScope(index int) Scope {
	stage := canonicalStages[index]
	return Scope{
		AttemptID: "attempt-1", PlanID: "plan-1",
		ExecutionID: fmt.Sprintf("execution-%s-1", stage),
		AuthorityID: fmt.Sprintf("authority-%s-1", stage),
		ResetID:     fmt.Sprintf("reset-%s-1", stage),
		ReporterID:  fmt.Sprintf("reporter-%s-1", stage),
		RunLinkID:   fmt.Sprintf("run-link-%s-1", stage),
	}
}

func successfulCompletion(index int) CompletionInput {
	stage := canonicalStages[index]
	return CompletionInput{
		Status:             CompletionSucceeded,
		Eligible:           true,
		SemanticIdentity:   fmt.Sprintf("semantic-%s", stage),
		EvidenceIdentities: []string{fmt.Sprintf("evidence-%s", stage)},
	}
}

func makeStageNA(input *Input, index int) {
	input.Policy[index].Required = false
	input.Policy[index].Identity = fmt.Sprintf("policy-na-%s", canonicalStages[index])
	input.Stages[index].Scope = Scope{AttemptID: "attempt-1", PlanID: "plan-1"}
	input.Stages[index].Reporter = ReporterUnknown
	input.Stages[index].ExecutionStartedAt = nil
	input.Stages[index].Completion = CompletionInput{Status: CompletionPending}
	input.Stages[index].Signals = StageSignals{}
	input.Stages[index].Estimates = nil
	input.Stages[index].History = nil
}

func historySamples(stage StageKey, count int, full int64) []DurationSample {
	stageIndex := slices.Index(canonicalStages[:], stage)
	samples := make([]DurationSample, count)
	for i := range samples {
		samples[i] = DurationSample{
			Identity:         fmt.Sprintf("sample-%s-%03d", stage, i),
			StageExecutionID: uint64((stageIndex+1)*1_000 + i + 1),
			ProjectIdentity:  "project-1",
			Stage:            stage,
			PolicyVersion:    EstimatorPolicyVersion,
			CompletedAt:      fixtureNow.Add(-time.Duration(i+1) * time.Hour),
			FullLeadSeconds:  full,
			ActiveSeconds:    full,
		}
	}
	return samples
}

func etaRange(minimum, maximum, point int64) *EstimateRange {
	return &EstimateRange{MinimumSeconds: minimum, MaximumSeconds: maximum, PointSeconds: &point}
}

func mustEvaluate(t *testing.T, input Input) Output {
	t.Helper()
	output, err := Evaluate(input)
	if err != nil {
		t.Fatalf("Evaluate() error: %v", err)
	}
	return output
}

func assertIntPointer(t *testing.T, actual *int, expected int) {
	t.Helper()
	if actual == nil || *actual != expected {
		t.Fatalf("value=%v, want %d", actual, expected)
	}
}

func assertInt64Pointer(t *testing.T, actual *int64, expected int64) {
	t.Helper()
	if actual == nil || *actual != expected {
		t.Fatalf("value=%v, want %d", actual, expected)
	}
}

func requireStageDiagnostic(t *testing.T, output Output, stage StageKey) StageDiagnostic {
	t.Helper()
	var matches []StageDiagnostic
	for _, diagnostic := range output.StageDiagnostics {
		if diagnostic.Stage == stage {
			matches = append(matches, diagnostic)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("diagnostic count for %s=%d, want exactly one; all=%+v", stage, len(matches), output.StageDiagnostics)
	}
	return matches[0]
}

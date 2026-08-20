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

type validatedDelivery struct {
	currentIndex int
	completed    bool
	deployed     bool
	verified     bool
}

// Evaluate deterministically evaluates one already-authorized, consistently
// read delivery snapshot. It performs no I/O and owns no persistence. Callers
// must load Input and its bounded histories in one caller-owned read
// transaction; this seam intentionally makes that integration requirement
// explicit without coupling the policy to SQL or HTTP.
func Evaluate(input Input) (Output, error) {
	input.CalculatedAt = input.CalculatedAt.UTC()
	if !input.Instrumented && input.PolicyVersion <= 0 {
		input.PolicyVersion = EstimatorPolicyVersion
	}
	state, err := validateInput(input)
	if err != nil {
		return Output{}, err
	}

	base := Output{
		SchemaVersion:          SchemaVersion,
		EstimatorPolicyVersion: input.PolicyVersion,
		DeliveryIdentity:       input.DeliveryIdentity,
		ServerTime:             input.CalculatedAt,
		CalculatedAt:           input.CalculatedAt,
		Instrumented:           input.Instrumented,
		ConfidenceLabel:        ConfidenceUnknown,
		RangeOnly:              true,
		Flags:                  []Flag{},
		Contributors:           []Contributor{},
	}
	if !input.Instrumented {
		base.Suppression = SuppressUnknownReporter
		base.TrustRevision = trustRevision(input, nil, nil)
		return base, nil
	}

	estimates := make([]estimateAnalysis, len(input.Stages))
	for i := range input.Stages {
		estimates[i], err = analyzeEstimates(input.Stages[i], input.CalculatedAt)
		if err != nil {
			return Output{}, err
		}
	}
	progress, err := weightedProgress(input.Policy, input.Stages, estimates, state.completed)
	if err != nil {
		return Output{}, err
	}
	base.Completed = state.completed
	base.Deployed = state.deployed
	base.Verified = state.verified
	base.Unverified = input.Policy[4].Required && !state.verified
	base.DeployedUnverified = state.deployed && input.Policy[4].Required && !state.verified
	base.ProgressKnown = true
	base.ProgressPercent = intPointer(progress.percent)
	base.ProgressSource = progress.source
	if progress.backslide {
		base.Flags = appendUniqueFlag(base.Flags, FlagSourceBackslideIgnored)
	}
	if base.DeployedUnverified {
		base.Flags = appendUniqueFlag(base.Flags, FlagDeployedUnverified)
	}

	if state.completed {
		base.Suppression = SuppressTerminalComplete
		base.RangeOnly = false
		base.TrustRevision = trustRevision(input, estimates, make([]HistoryResult, len(input.Stages)))
		return base, nil
	}

	current := input.Stages[state.currentIndex]
	currentStage := current.Stage
	base.CurrentStage = &currentStage
	base.Scope = publicScope(current.Scope)
	base.OwnerSource = estimates[state.currentIndex].latestAttribution
	if estimates[state.currentIndex].invalidLatest {
		base.Flags = appendUniqueFlag(base.Flags, FlagOwnerEstimateInvalid)
	}
	if estimates[state.currentIndex].expired {
		base.Flags = appendUniqueFlag(base.Flags, FlagOwnerEstimateExpired)
	}
	if current.Completion.Status == CompletionFailed || current.Completion.Status == CompletionConflict {
		base.FailedNeedsRetry = true
		base.Flags = appendUniqueFlag(base.Flags, FlagFailedNeedsRetry)
	}

	landing, histories, err := analyzeLanding(input, state.currentIndex, estimates)
	if err != nil {
		return Output{}, err
	}
	base.Contributors = landing.contributors
	for _, flag := range landing.flags {
		base.Flags = appendUniqueFlag(base.Flags, flag)
	}
	base.Suppression = chooseSuppression(current, landing.failure)
	if landing.label != ConfidenceUnknown && landing.failure == "" {
		confidence := landing.confidence
		base.Confidence = &confidence
		base.ConfidenceLabel = landing.label
	}

	transitions := append([]time.Time(nil), current.Signals.TransitionAt...)
	transitions = append(transitions, landing.transitions...)
	base.NextTrustTransitionAt = minimumFuture(input.CalculatedAt, transitions...)
	base.TrustRevision = trustRevision(input, estimates, histories)

	if base.Suppression != "" {
		return base, nil
	}
	minimumAt, ok := addSeconds(input.CalculatedAt, landing.minimumSeconds)
	if !ok {
		return Output{}, fmt.Errorf("%w: optimistic landing instant overflow", ErrInvalidInput)
	}
	maximumAt, ok := addSeconds(input.CalculatedAt, landing.maximumSeconds)
	if !ok {
		return Output{}, fmt.Errorf("%w: pessimistic landing instant overflow", ErrInvalidInput)
	}
	base.OptimisticLandingAt = &minimumAt
	base.PessimisticLandingAt = &maximumAt
	base.RemainingMinimumSeconds = int64Pointer(landing.minimumSeconds)
	base.RemainingMaximumSeconds = int64Pointer(landing.maximumSeconds)
	base.RangeOnly = landing.rangeOnly
	if landing.pointSeconds != nil {
		pointAt, valid := addSeconds(input.CalculatedAt, *landing.pointSeconds)
		if !valid {
			return Output{}, fmt.Errorf("%w: point landing instant overflow", ErrInvalidInput)
		}
		base.LandingAt = &pointAt
		base.RemainingSeconds = int64Pointer(*landing.pointSeconds)
	}
	return base, nil
}

func validateInput(input Input) (validatedDelivery, error) {
	if !validOpaque(input.DeliveryIdentity) || !validOpaque(input.ProjectIdentity) || input.CalculatedAt.IsZero() {
		return validatedDelivery{}, fmt.Errorf("%w: invalid delivery identity, project, or calculation time", ErrInvalidInput)
	}
	if input.PolicyVersion != EstimatorPolicyVersion {
		return validatedDelivery{}, fmt.Errorf("%w: unsupported estimator policy version", ErrInvalidInput)
	}
	if !input.Instrumented {
		if len(input.Policy) != 0 || len(input.Stages) != 0 {
			return validatedDelivery{}, fmt.Errorf("%w: uninstrumented delivery carries trusted stage data", ErrInvalidInput)
		}
		return validatedDelivery{currentIndex: -1}, nil
	}
	if len(input.Policy) != len(canonicalStages) || len(input.Stages) != len(canonicalStages) {
		return validatedDelivery{}, fmt.Errorf("%w: incomplete estimator policy or stage set", ErrInvalidInput)
	}

	requiredWeight := 0
	currentIndex := -1
	completed := true
	seenIncomplete := false
	var attemptID, planID string
	for i, expectedStage := range canonicalStages {
		policy := input.Policy[i]
		stage := input.Stages[i]
		if policy.Stage != expectedStage || stage.Stage != expectedStage || !validOpaque(policy.Identity) ||
			policy.Weight != DefaultWeight(expectedStage) {
			return validatedDelivery{}, fmt.Errorf("%w: invalid or non-canonical stage policy", ErrInvalidInput)
		}
		if policy.Required {
			requiredWeight += policy.Weight
		}
		if i == 0 {
			attemptID, planID = stage.Scope.AttemptID, stage.Scope.PlanID
		} else if stage.Scope.AttemptID != attemptID || stage.Scope.PlanID != planID {
			return validatedDelivery{}, fmt.Errorf("%w: stages cross attempt or plan scope", ErrInvalidInput)
		}
		started := stage.ExecutionStartedAt != nil
		if err := validateScope(stage.Scope, stage.Reporter, started); err != nil {
			return validatedDelivery{}, err
		}
		if started && (stage.ExecutionStartedAt.IsZero() || stage.ExecutionStartedAt.After(input.CalculatedAt)) {
			return validatedDelivery{}, fmt.Errorf("%w: invalid execution start", ErrInvalidInput)
		}
		if err := validateCompletion(stage.Completion); err != nil {
			return validatedDelivery{}, err
		}
		if stage.Signals.SemanticIdentity != "" && !validOpaque(stage.Signals.SemanticIdentity) {
			return validatedDelivery{}, fmt.Errorf("%w: invalid semantic signal identity", ErrInvalidInput)
		}
		if (stage.Signals.WaitingOnHuman || stage.Signals.Blocked) && stage.Signals.SemanticIdentity == "" {
			return validatedDelivery{}, fmt.Errorf("%w: semantic state lacks identity", ErrInvalidInput)
		}
		if !policy.Required && !sterileNAStage(stage) {
			return validatedDelivery{}, fmt.Errorf("%w: N/A stage carries delivery facts", ErrInvalidInput)
		}

		done := !policy.Required || stage.Completion.Eligible
		if policy.Required && !done {
			completed = false
			if currentIndex == -1 {
				currentIndex = i
				seenIncomplete = true
			}
		} else if policy.Required && seenIncomplete {
			return validatedDelivery{}, fmt.Errorf("%w: stage completes before its prerequisite", ErrInvalidInput)
		}
	}
	if requiredWeight <= 0 {
		return validatedDelivery{}, fmt.Errorf("%w: no required stage weight", ErrInvalidInput)
	}
	if !completed && currentIndex < 0 {
		return validatedDelivery{}, fmt.Errorf("%w: no current stage", ErrInvalidInput)
	}
	for i := currentIndex + 1; !completed && i < len(input.Stages); i++ {
		stage := input.Stages[i]
		if stage.ExecutionStartedAt != nil || len(stage.Estimates) != 0 || stage.Completion.Status != CompletionPending {
			return validatedDelivery{}, fmt.Errorf("%w: future stage already started", ErrInvalidInput)
		}
	}

	deployed := input.Stages[3].Completion.Eligible
	verified := input.Stages[4].Completion.Eligible
	return validatedDelivery{
		currentIndex: currentIndex,
		completed:    completed,
		deployed:     deployed,
		verified:     verified,
	}, nil
}

func validateCompletion(completion CompletionInput) error {
	switch completion.Status {
	case CompletionPending, CompletionSucceeded, CompletionFailed, CompletionCancelled, CompletionConflict:
	default:
		return fmt.Errorf("%w: invalid completion status", ErrInvalidInput)
	}
	if completion.Eligible && completion.Status != CompletionSucceeded {
		return fmt.Errorf("%w: non-success status marked complete", ErrInvalidInput)
	}
	if completion.Status == CompletionSucceeded && !completion.Eligible {
		return fmt.Errorf("%w: sealed success lacks eligible terminal evidence", ErrInvalidInput)
	}
	if completion.Eligible && (completion.SemanticIdentity == "" || len(completion.EvidenceIdentities) == 0) {
		return fmt.Errorf("%w: terminal completion lacks evidence", ErrInvalidInput)
	}
	if completion.SemanticIdentity != "" && !validOpaque(completion.SemanticIdentity) {
		return fmt.Errorf("%w: invalid terminal semantic identity", ErrInvalidInput)
	}
	seen := make(map[string]bool, len(completion.EvidenceIdentities))
	for _, identity := range completion.EvidenceIdentities {
		if !validOpaque(identity) || seen[identity] {
			return fmt.Errorf("%w: invalid or duplicate terminal evidence", ErrInvalidInput)
		}
		seen[identity] = true
	}
	return nil
}

func sterileNAStage(stage StageInput) bool {
	return stage.ExecutionStartedAt == nil &&
		stage.Reporter == ReporterUnknown &&
		stage.Scope.ExecutionID == "" && stage.Scope.AuthorityID == "" && stage.Scope.ResetID == "" &&
		stage.Scope.ReporterID == "" && stage.Scope.RunLinkID == "" &&
		stage.Completion.Status == CompletionPending && !stage.Completion.Eligible &&
		stage.Completion.SemanticIdentity == "" && len(stage.Completion.EvidenceIdentities) == 0 &&
		stage.Signals.SemanticIdentity == "" && !stage.Signals.WaitingOnHuman && !stage.Signals.Blocked &&
		!stage.Signals.Stale && !stage.Signals.UnknownReporter && !stage.Signals.NoSignal &&
		len(stage.Signals.TransitionAt) == 0 && len(stage.Estimates) == 0 && len(stage.History) == 0
}

func chooseSuppression(current StageInput, landingFailure SuppressionCode) SuppressionCode {
	var state SuppressionCode
	switch current.Completion.Status {
	case CompletionCancelled:
		state = SuppressCancelled
	case CompletionFailed, CompletionConflict:
		state = SuppressTerminalFailed
	}
	if current.Signals.WaitingOnHuman {
		state = preferredSuppression(state, SuppressWaitingOnHuman)
	}
	if current.Signals.Blocked {
		state = preferredSuppression(state, SuppressBlocked)
	}
	if current.Signals.Stale {
		state = preferredSuppression(state, SuppressStale)
	}
	if current.Signals.UnknownReporter ||
		(current.Reporter != ReporterAgentRun && current.Reporter != ReporterExternal) {
		state = preferredSuppression(state, SuppressUnknownReporter)
	}
	if current.Signals.NoSignal {
		state = preferredSuppression(state, SuppressNoSignal)
	}
	return preferredSuppression(state, landingFailure)
}

func intPointer(value int) *int { return &value }

func int64Pointer(value int64) *int64 { return &value }

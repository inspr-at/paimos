// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

func (r *cliReporter) processRetirement(ctx context.Context, runtimeGeneration, publicID string, session agentd.Session, remote harnessSessionResponse, agentName, workerLease string, claim models.HarnessRetirementClaim) error {
	if err := validateRetirementClaim(runtimeGeneration, publicID, session, remote, claim); err != nil {
		return err
	}
	if session.LastEventKind != agentd.EventTurnCompleted || session.ActivitySequence < claim.RequestedActivitySequence {
		return nil
	}

	// This conservative result is durable before crossing the server-ready or
	// local-stop boundaries. A crash can therefore never replay an unreceipted
	// stop. A definitive not-ready response clears it and leaves the claim live.
	completion := agentd.ReporterCompletion{ControlID: claim.ID, Kind: managedharness.RetirementKind, Outcome: "rejected", Reason: "outcome_unknown"}
	if err := r.checkpoint(ctx, session, r.pendingReporterState(session, publicID, completion)); err != nil {
		return err
	}

	readyArgs := []string{"--json", "harness", "retirement-ready", "--project", strconv.FormatInt(session.ProjectID, 10),
		"--session", publicID, "--retirement-id", claim.ID, "--agent", agentName, "--worker-lease-file", "-"}
	raw, err := r.run(ctx, r.paimosPath, readyArgs, r.environment, strings.NewReader(workerLease))
	if err != nil {
		if reporterErrorCode(raw) == "harness_session_retirement_not_ready" {
			if checkpointErr := r.checkpoint(ctx, session, r.baseReporterState(session, publicID)); checkpointErr != nil {
				return checkpointErr
			}
			return nil
		}
		return err
	}
	var ready models.HarnessRetirementClaim
	if json.Unmarshal(raw, &ready) != nil || !retirementReadyMatches(claim, ready) {
		return errors.New("paimos reporter returned mismatched retirement readiness evidence")
	}

	request := agentd.ControlRequest{Instance: r.instance, ProjectID: session.ProjectID, Identity: session.Identity, CorrelationID: claim.ID}
	receipt, stopErr := r.controller.Stop(ctx, session.ID, request)
	if stopErr == nil && validReporterReceipt(receipt, "stop", session, request) {
		completion.Outcome, completion.Reason = "applied", "applied"
	} else if stopErr != nil {
		completion.Reason = reporterRetirementStopReason(stopErr)
	}
	// An invalid or absent receipt after invoking Stop deliberately retains
	// outcome_unknown. Public stopped state is not substituted for this proof.
	if err := r.checkpoint(ctx, session, r.pendingReporterState(session, publicID, completion)); err != nil {
		return err
	}
	if err := r.completeRetirement(ctx, publicID, session, agentName, workerLease, completion); err != nil {
		return err
	}
	if err := r.checkpoint(ctx, session, r.baseReporterState(session, publicID)); err != nil {
		return err
	}
	if completion.Outcome != "applied" {
		return nil
	}
	return r.closeRetiredSession(ctx, publicID, session, workerLease)
}

func (r *cliReporter) closeRetiredSession(ctx context.Context, publicID string, session agentd.Session, workerLease string) error {
	if err := r.markStopped(ctx, publicID, session, workerLease); err != nil {
		return err
	}
	closedState := r.baseReporterState(session, publicID)
	closedState.RemoteClosed = true
	if err := r.checkpoint(ctx, session, closedState); err != nil {
		return err
	}
	if err := r.leases.Delete(session.ID); err != nil {
		return errors.New("private agentd worker lease could not be released")
	}
	closedState.Closed = true
	if err := r.checkpoint(ctx, session, closedState); err != nil {
		return err
	}
	known := r.sessions[session.ID]
	known.terminal = true
	r.sessions[session.ID] = known
	return nil
}

func validateRetirementClaim(runtimeGeneration, publicID string, session agentd.Session, remote harnessSessionResponse, claim models.HarnessRetirementClaim) error {
	validTime := func(value string) bool {
		_, err := time.Parse(time.RFC3339Nano, value)
		return err == nil
	}
	if uuid.Validate(claim.ID) != nil || claim.ProjectID != session.ProjectID || claim.HarnessSessionID != publicID ||
		claim.HarnessSessionRevision < 1 || claim.RequestedActivitySequence < 0 || uuid.Validate(claim.RuntimeID) != nil ||
		uuid.Validate(runtimeGeneration) != nil || claim.RuntimeGeneration != runtimeGeneration ||
		uuid.Validate(session.ID) != nil || claim.SessionGeneration != session.ID ||
		(claim.State != "claimed" && claim.State != "stopping") || !validTime(claim.RequestedAt) || !validTime(claim.ClaimedAt) ||
		(claim.State == "stopping" && !validTime(claim.StoppingAt)) ||
		remote.Revision < claim.HarnessSessionRevision || remote.ActivitySequence != session.ActivitySequence ||
		claim.RequestedActivitySequence > session.ActivitySequence {
		return errors.New("paimos reporter returned an invalid generation-fenced retirement")
	}
	return nil
}

func retirementReadyMatches(claim, ready models.HarnessRetirementClaim) bool {
	if ready.ID != claim.ID || ready.ProjectID != claim.ProjectID || ready.HarnessSessionID != claim.HarnessSessionID ||
		ready.HarnessSessionRevision != claim.HarnessSessionRevision || ready.RequestedActivitySequence != claim.RequestedActivitySequence ||
		ready.RuntimeID != claim.RuntimeID || ready.RuntimeGeneration != claim.RuntimeGeneration || ready.SessionGeneration != claim.SessionGeneration ||
		ready.RequestedAt != claim.RequestedAt || ready.ClaimedAt != claim.ClaimedAt || ready.State != "stopping" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, ready.StoppingAt)
	return err == nil && (claim.StoppingAt == "" || ready.StoppingAt == claim.StoppingAt)
}

func reporterRetirementStopReason(err error) string {
	switch {
	case errors.Is(err, agentd.ErrSessionNotFound), errors.Is(err, agentd.ErrSessionNotRunning):
		return "not_running"
	case errors.Is(err, agentd.ErrCapabilityMissing):
		return "unsupported"
	default:
		return "outcome_unknown"
	}
}

func (r *cliReporter) completeRetirement(ctx context.Context, publicID string, session agentd.Session, agentName, workerLease string, completion agentd.ReporterCompletion) error {
	args := []string{"--json", "harness", "complete-retirement", "--project", strconv.FormatInt(session.ProjectID, 10),
		"--session", publicID, "--retirement-id", completion.ControlID, "--agent", agentName, "--worker-lease-file", "-",
		"--outcome", completion.Outcome, "--reason", completion.Reason}
	raw, err := r.run(ctx, r.paimosPath, args, r.environment, strings.NewReader(workerLease))
	if err != nil {
		return err
	}
	var response models.HarnessRetirementOutcome
	if json.Unmarshal(raw, &response) != nil || !retirementCompletionMatches(publicID, session.ProjectID, completion, response) {
		return errors.New("paimos reporter returned mismatched retirement completion evidence")
	}
	return nil
}

func retirementCompletionMatches(publicID string, projectID int64, completion agentd.ReporterCompletion, response models.HarnessRetirementOutcome) bool {
	if response.ID != completion.ControlID || response.CorrelationID != completion.ControlID || response.ProjectID != projectID ||
		response.HarnessSessionID != publicID || response.Kind != managedharness.RetirementKind || response.RequestedRevision < 1 ||
		response.Reason != completion.Reason || response.OwnedStopReceipt != (completion.Outcome == "applied") {
		return false
	}
	switch {
	case completion.Outcome == "applied":
		return response.State == "stopping" && !response.StoppedGenerationProof || response.State == "completed" && response.StoppedGenerationProof
	case completion.Outcome == "rejected" && completion.Reason == "outcome_unknown":
		return response.State == "outcome_unknown" && !response.StoppedGenerationProof
	case completion.Outcome == "rejected":
		return response.State == "failed" && !response.StoppedGenerationProof
	default:
		return false
	}
}

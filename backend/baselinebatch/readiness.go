// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

// OwnedVerifier resolves the current owned readiness evidence for one reviewed
// worker selection. It never trusts a client ready:true, a registration
// advertisement, or an operator's intent: the only thing that can make a
// selection ready is a completed readiness intent whose report an owned daemon
// produced from real local probes and this authority accepted.
type OwnedVerifier interface {
	Verify(ctx context.Context, tx *sql.Tx, projectID int64, worker WorkerSelection, baselineDigest string, now time.Time) (ReadinessEvidence, error)
}

// LifecycleVerifier reads the durable observation recorded by
// lifecycleintents when the daemon completed the readiness probe. Absence,
// expiry, a non-ready status or a failed required check all block; there is no
// weaker match and no substitution.
type LifecycleVerifier struct{}

func (LifecycleVerifier) Verify(ctx context.Context, tx *sql.Tx, projectID int64, worker WorkerSelection, baselineDigest string, now time.Time) (ReadinessEvidence, error) {
	evidence := ReadinessEvidence{
		Status:             "unavailable",
		Basis:              BasisOwnedDaemonProbe,
		ContractVersion:    lifecycleintents.ReadinessContractVersion,
		ClientReadyIgnored: true,
		BaselineDigest:     baselineDigest,
		RuntimeID:          worker.RuntimeID,
		RuntimeGeneration:  worker.RuntimeGeneration,
		AccountLabel:       worker.AccountLabel,
		AccountKey:         worker.AccountKey,
		ProfileID:          worker.ProfileID,
		ProfileVersion:     worker.ProfileVersion,
		WorkspaceHandle:    worker.WorkspaceHandle,
		Checks:             []ReadinessCheckView{},
	}
	if worker.RuntimeID == "" || worker.RuntimeGeneration == "" || worker.AccountLabel == "" ||
		worker.ProfileID == "" || worker.ProfileVersion == "" || worker.WorkspaceHandle == "" {
		evidence.BlockingReason = "worker_selection_incomplete"
		return evidence, nil
	}
	if !lifecycleintents.ValidBaselineDigest(baselineDigest) {
		evidence.BlockingReason = "baseline_digest_invalid"
		return evidence, nil
	}
	live, err := runtimeIsLive(ctx, tx, projectID, worker, now)
	if err != nil {
		return evidence, err
	}
	if !live {
		evidence.BlockingReason = "runtime_offline"
		return evidence, nil
	}
	target := lifecycleintents.ReadinessTarget{
		RuntimeID:         worker.RuntimeID,
		RuntimeGeneration: worker.RuntimeGeneration,
		AccountLabel:      worker.AccountLabel,
		AccountKey:        worker.AccountKey,
		ProfileID:         worker.ProfileID,
		ProfileVersion:    worker.ProfileVersion,
		WorkspaceHandle:   worker.WorkspaceHandle,
		BaselineDigest:    baselineDigest,
	}
	if err := loadProbeState(ctx, tx, projectID, target, &evidence); err != nil {
		return evidence, err
	}
	observation, err := lifecycleintents.CurrentReadinessTx(ctx, tx, projectID, target, now)
	if errors.Is(err, sql.ErrNoRows) {
		evidence.BlockingReason = "readiness_observation_missing"
		evidence.NextAction = "run_owned_readiness_probe"
		return evidence, nil
	}
	if err != nil {
		return evidence, err
	}
	evidence.IntentID = observation.IntentID
	evidence.ObservedAt = observation.ObservedAt
	evidence.FreshUntil = observation.ExpiresAt
	evidence.WorkspaceIdentity = observation.WorkspaceIdentity
	evidence.HostKind = observation.HostKind
	evidence.NextAction = observation.NextAction
	for _, check := range observation.Checks {
		evidence.Checks = append(evidence.Checks, ReadinessCheckView{ID: check.ID, Status: check.Status, Reason: check.Reason})
	}
	if observation.Status != "ready" {
		evidence.Status = observation.Status
		evidence.BlockingReason = "readiness_" + observation.Status
		return evidence, nil
	}
	if missing := failedRequiredCheck(observation.Checks); missing != "" {
		evidence.BlockingReason = "required_check_" + missing
		return evidence, nil
	}
	evidence.Status = "ready"
	evidence.NamedAccountProof = observation.AccountKey == worker.AccountKey
	evidence.ModelProfileProof = observation.ProfileID == worker.ProfileID && observation.ProfileVersion == worker.ProfileVersion
	evidence.WorkspaceProof = observation.WorkspaceIdentity != ""
	if !evidence.NamedAccountProof || !evidence.ModelProfileProof || !evidence.WorkspaceProof {
		evidence.Status = "unavailable"
		evidence.BlockingReason = "observation_binding_mismatch"
	}
	return evidence, nil
}

// runtimeIsLive keeps an observation from outliving the daemon generation that
// produced it, even inside the stored freshness window.
func runtimeIsLive(ctx context.Context, tx *sql.Tx, projectID int64, worker WorkerSelection, now time.Time) (bool, error) {
	var expires string
	err := tx.QueryRowContext(ctx, `SELECT expires_at FROM lifecycle_runtimes WHERE id=? AND project_id=? AND generation=?`,
		worker.RuntimeID, projectID, worker.RuntimeGeneration).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	deadline, parseErr := time.Parse(time.RFC3339Nano, expires)
	if parseErr != nil {
		return false, nil
	}
	return deadline.After(now), nil
}

// loadProbeState reports the newest owned probe for this exact target so the
// human can see that a probe is in flight instead of guessing why an agent mode
// is still blocked. It never substitutes for an accepted observation.
func loadProbeState(ctx context.Context, tx *sql.Tx, projectID int64, target lifecycleintents.ReadinessTarget, evidence *ReadinessEvidence) error {
	err := tx.QueryRowContext(ctx, `SELECT id,state,reason FROM lifecycle_intents
		WHERE project_id=? AND runtime_id=?
		 AND json_extract(request_json,'$.operation')='readiness'
		 AND json_extract(request_json,'$.runtime_generation')=?
		 AND json_extract(request_json,'$.baseline_digest')=?
		 AND json_extract(request_json,'$.workspace_handle')=?
		 AND json_extract(request_json,'$.dispatch_profile_id')=?
		ORDER BY created_at DESC, id DESC LIMIT 1`,
		projectID, target.RuntimeID, target.RuntimeGeneration, target.BaselineDigest,
		target.WorkspaceHandle, target.ProfileID).
		Scan(&evidence.ProbeIntentID, &evidence.ProbeState, &evidence.ProbeReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func failedRequiredCheck(checks []lifecycleintents.ReadinessCheck) string {
	byID := map[string]string{}
	for _, check := range checks {
		byID[check.ID] = check.Status
	}
	for _, required := range lifecycleintents.RequiredReadinessChecks {
		if byID[required] != "pass" {
			return required
		}
	}
	return ""
}

// RequestReadiness asks the owned daemon to observe this host for the draft's
// reviewed selection. It submits a readiness intent through the same lifecycle
// authority as a start — the daemon claims it, runs the real probes and reports
// back — and returns the evidence as it stands right now. The browser never
// supplies, and never receives, anything the probe reads.
func (s *Service) RequestReadiness(ctx context.Context, actor Actor, projectID, draftID int64) (ReadinessEvidence, error) {
	if err := requireHuman(actor); err != nil {
		return ReadinessEvidence{}, err
	}
	if s.Lifecycle == nil {
		return ReadinessEvidence{}, fmt.Errorf("%w: lifecycle authority", ErrUnavailable)
	}
	principal, err := actor.principal()
	if err != nil {
		return ReadinessEvidence{}, err
	}
	lifecycleintents.LockMutations()
	defer lifecycleintents.UnlockMutations()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ReadinessEvidence{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return ReadinessEvidence{}, err
	}
	if err := s.requireStreamEnabled(ctx, tx, projectID); err != nil {
		return ReadinessEvidence{}, err
	}
	draft, err := loadDraftByID(ctx, tx, projectID, draftID)
	if err != nil {
		return ReadinessEvidence{}, err
	}
	if draft.Status == DraftClosed {
		return ReadinessEvidence{}, fmt.Errorf("%w: draft is closed", ErrConflict)
	}
	worker := draft.Worker
	if draft.ExecutionMode == ModeManual || draft.ExecutionMode == "" {
		return ReadinessEvidence{}, fmt.Errorf("%w: manual delivery does not need an owned readiness probe", ErrInvalid)
	}
	if err := s.validateAgentWorker(ctx, tx, projectID, worker); err != nil {
		return ReadinessEvidence{}, err
	}
	// One probe per exact target: repeating the request re-reads the same intent
	// instead of queueing a second observation of the same host.
	key := requestKey(projectID, "readiness", strings.Join([]string{
		worker.RuntimeID, worker.RuntimeGeneration, worker.AccountLabel, worker.AccountKey,
		worker.ProfileID, worker.ProfileVersion, worker.WorkspaceHandle, draft.Baseline.ContentDigest,
	}, "\x00"))
	if _, _, err := s.Lifecycle.SubmitTx(ctx, tx, principal, projectID, lifecycleintents.Request{
		RequestKey:             key,
		Operation:              "readiness",
		RuntimeID:              worker.RuntimeID,
		RuntimeGeneration:      worker.RuntimeGeneration,
		AccountLabel:           worker.AccountLabel,
		AccountKey:             worker.AccountKey,
		TTLSeconds:             lifecycleintents.ReadinessTTLSeconds,
		WorkspaceHandle:        worker.WorkspaceHandle,
		DispatchProfileID:      worker.ProfileID,
		DispatchProfileVersion: worker.ProfileVersion,
		BaselineDigest:         draft.Baseline.ContentDigest,
	}); err != nil {
		return ReadinessEvidence{}, lifecycleFailure("readiness", err)
	}
	evidence, err := s.Verifier.Verify(ctx, tx, projectID, worker, draft.Baseline.ContentDigest, s.now())
	if err != nil {
		return ReadinessEvidence{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReadinessEvidence{}, err
	}
	return evidence, nil
}

func requireAgentReadiness(evidence ReadinessEvidence) error {
	if evidence.Status == "ready" && evidence.NamedAccountProof && evidence.ModelProfileProof && evidence.WorkspaceProof {
		return nil
	}
	reason := evidence.BlockingReason
	if reason == "" {
		reason = evidence.Status
	}
	return fmt.Errorf("%w: %s", ErrBlocked, reason)
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/targetidentity"
)

const launchAdmissionTTL = 15 * time.Minute

type launchGrantRow struct {
	grantID, baselineRef, contentDigest, revisionSeal, targetRef, workflow, environment string
	humanSession, issuedAt, expiresAt, revokedAt, lifecycleIntentID                     string
	scopeJSON, workerJSON, delegatedJSON, reviewBinding, reviewSelectedJSON             string
	revision, projectID, draftID, draftRevision, reviewID, batchID                      int64
	deliveryID, attemptID, attemptNumber, planRevision, humanUserID                     int64
	maxLaunches                                                                         int
	grantDigest, scopeDigest, workerDigest                                              []byte
}

type launchScope struct {
	RequirementRefs []string `json:"requirement_refs"`
	ConstraintRefs  []string `json:"constraint_refs"`
}

type launchWorker struct {
	WorkerName        string `json:"worker_name,omitempty"`
	AccountLabel      string `json:"account_label,omitempty"`
	AccountKey        string `json:"account_key,omitempty"`
	ProfileID         string `json:"profile_id,omitempty"`
	ProfileVersion    string `json:"profile_version,omitempty"`
	WorkspaceHandle   string `json:"workspace_handle,omitempty"`
	RuntimeID         string `json:"runtime_id,omitempty"`
	RuntimeGeneration string `json:"runtime_generation,omitempty"`
}

type launchSelection struct {
	TargetRef   string `json:"target_ref"`
	Workflow    string `json:"workflow"`
	Environment string `json:"environment"`
	ExpiresAt   string `json:"expires_at"`
	MaxLaunches int    `json:"max_launches"`
}

func validateLaunchCandidate(candidate LaunchCandidate, secret []byte, now time.Time) (LaunchCandidate, error) {
	if candidate.Schema != LaunchAdmissionSchema || candidate.Version != LaunchAdmissionVersion ||
		!targetidentity.IsDigest(candidate.TargetRef) || candidate.Workflow != "deploy-production" ||
		!symbolPattern.MatchString(candidate.Environment) || !digestString(candidate.ReviewedPlanDigest) ||
		!digestString(candidate.OperationBindingDigest) || candidate.ReviewedPlanDigest == candidate.OperationBindingDigest {
		return LaunchCandidate{}, ErrInvalid
	}
	if err := validateArtifactEvidenceV2(candidate.Artifact, secret); err != nil {
		return LaunchCandidate{}, err
	}
	for _, value := range []string{candidate.TargetRef, candidate.Workflow, candidate.Environment,
		candidate.ReviewedPlanDigest, candidate.OperationBindingDigest, candidate.ObservedAt} {
		if secretEcho(value, secret) {
			return LaunchCandidate{}, ErrInvalid
		}
	}
	observed, err := time.Parse(time.RFC3339Nano, candidate.ObservedAt)
	if err != nil || observed.After(now.UTC().Add(MaxReporterFutureSkew)) {
		return LaunchCandidate{}, ErrInvalid
	}
	candidate.ObservedAt = observed.UTC().Format(time.RFC3339Nano)
	return candidate, nil
}

func digestString(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && value == strings.ToLower(value) && func() bool {
		_, err := hex.DecodeString(value[7:])
		return err == nil
	}()
}

func (s *Service) AdmitLaunch(ctx context.Context, principal Principal, handoffID, idempotencyKey string,
	secret []byte, candidate LaunchCandidate,
) ([]byte, error) {
	if idempotencyKey == "" {
		return nil, ErrInvalid
	}
	var err error
	candidate, err = validateLaunchCandidate(candidate, secret, s.clock.Now())
	if err != nil {
		return nil, err
	}
	candidateDigest, err := canonicalDigest(candidate)
	if err != nil {
		return nil, err
	}
	idempotencyDigest := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	handoff, err := s.loadHandoffTx(ctx, tx, handoffID)
	if err != nil || s.authenticateExternalTx(ctx, tx, handoff, principal, secret) != nil {
		return nil, ErrNotFound
	}
	if prior, found, err := loadAdmissionByHandoff(ctx, tx, handoff.rowID); err != nil {
		return nil, err
	} else if found {
		if subtle.ConstantTimeCompare(prior.candidateDigest, candidateDigest[:]) != 1 ||
			subtle.ConstantTimeCompare(prior.idempotencyDigest, idempotencyDigest[:]) != 1 ||
			prior.credentialEpoch != handoff.credentialEpoch {
			return nil, ErrConflict
		}
		var admission LaunchAdmission
		if json.Unmarshal(prior.response, &admission) != nil || admission.CredentialEpoch != handoff.credentialEpoch {
			return nil, ErrConflict
		}
		expires, expiryErr := time.Parse(time.RFC3339Nano, admission.ExpiresAt)
		if expiryErr != nil || !expires.After(s.clock.Now().UTC()) {
			return nil, ErrConflict
		}
		replayCandidate := LaunchCandidate{TargetRef: admission.TargetRef, Workflow: admission.Workflow,
			Environment: admission.Environment, Artifact: admission.Artifact, ReviewedPlanDigest: admission.ReviewedPlanDigest,
			OperationBindingDigest: admission.OperationBindingDigest}
		_, expectedArtifact, err := s.validateLaunchAuthorityTx(ctx, tx, handoff, replayCandidate)
		if err != nil || matchBuiltOwnerArtifact(expectedArtifact, &PharosEvidence{
			Workflow: replayCandidate.Workflow, Environment: replayCandidate.Environment,
			Artifact: ArtifactEvidence{Version: replayCandidate.Artifact.Version, Digest: replayCandidate.Artifact.Digest,
				CommitDigest: replayCandidate.Artifact.CommitDigest},
		}, &replayCandidate.Artifact) != nil {
			return nil, ErrConflict
		}
		return append([]byte(nil), prior.response...), nil
	}
	grant, expectedArtifact, err := s.validateLaunchAuthorityTx(ctx, tx, handoff, candidate)
	if err != nil {
		return nil, err
	}
	if err := matchBuiltOwnerArtifact(expectedArtifact, &PharosEvidence{
		Workflow: candidate.Workflow, Environment: candidate.Environment,
		Artifact: ArtifactEvidence{Version: candidate.Artifact.Version, Digest: candidate.Artifact.Digest, CommitDigest: candidate.Artifact.CommitDigest},
	}, &candidate.Artifact); err != nil {
		return nil, ErrConflict
	}
	issued := s.clock.Now().UTC()
	expires := issued.Add(launchAdmissionTTL)
	if rootExpiry, _ := time.Parse(time.RFC3339Nano, grant.expiresAt); rootExpiry.Before(expires) {
		expires = rootExpiry
	}
	if handoffExpiry, _ := time.Parse(time.RFC3339Nano, handoff.expiresAt); handoffExpiry.Before(expires) {
		expires = handoffExpiry
	}
	if !expires.After(issued) {
		return nil, ErrConflict
	}
	admissionID := uuid.NewString()
	grantDigest := "sha256:" + hex.EncodeToString(grant.grantDigest)
	identity := struct {
		Domain                 string             `json:"domain"`
		GrantID                string             `json:"grant_id"`
		GrantRevision          int64              `json:"grant_revision"`
		GrantDigest            string             `json:"grant_digest"`
		AdmissionID            string             `json:"admission_id"`
		HandoffID              string             `json:"handoff_id"`
		CredentialEpoch        int64              `json:"credential_epoch"`
		TargetRef              string             `json:"target_ref"`
		Workflow               string             `json:"workflow"`
		Environment            string             `json:"environment"`
		Artifact               ArtifactEvidenceV2 `json:"artifact"`
		Stage                  string             `json:"stage"`
		Attempt                int64              `json:"attempt"`
		Plan                   int64              `json:"plan"`
		Execution              int64              `json:"execution"`
		Authority              int64              `json:"authority"`
		PlanDigest             string             `json:"plan_digest"`
		PredecessorDigest      string             `json:"predecessor_digest"`
		ContextDigest          string             `json:"context_digest"`
		ReviewedPlanDigest     string             `json:"reviewed_plan_digest"`
		OperationBindingDigest string             `json:"operation_binding_digest"`
		IssuedAt               string             `json:"issued_at"`
		ExpiresAt              string             `json:"expires_at"`
	}{"paimos.external-stage.launch-admission.v1", grant.grantID, grant.revision, grantDigest, admissionID,
		handoff.handoffID, handoff.credentialEpoch, candidate.TargetRef, candidate.Workflow, candidate.Environment,
		candidate.Artifact, handoff.stageKey, handoff.attemptNumber, handoff.planRevision, handoff.executionNumber,
		handoff.authorityEpoch, "sha256:" + strings.ToLower(handoff.planDigest), "sha256:" + strings.ToLower(handoff.predecessorDigest),
		"sha256:" + strings.ToLower(handoff.contextDigest), candidate.ReviewedPlanDigest, candidate.OperationBindingDigest,
		issued.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano)}
	identityRaw, _ := json.Marshal(identity)
	admissionDigestRaw := sha256.Sum256(identityRaw)
	admission := LaunchAdmission{
		Schema: LaunchAdmissionSchema, Version: LaunchAdmissionVersion, GrantID: grant.grantID, GrantRevision: int(grant.revision),
		GrantDigest: grantDigest, AdmissionID: admissionID, AdmissionDigest: "sha256:" + hex.EncodeToString(admissionDigestRaw[:]),
		HandoffID: handoff.handoffID, CredentialEpoch: handoff.credentialEpoch, TargetRef: candidate.TargetRef,
		Workflow: candidate.Workflow, Environment: candidate.Environment, Artifact: candidate.Artifact, Stage: handoff.stageKey,
		Attempt: handoff.attemptNumber, Plan: handoff.planRevision, Execution: handoff.executionNumber,
		Authority: handoff.authorityEpoch, PlanDigest: identity.PlanDigest, PredecessorDigest: identity.PredecessorDigest,
		ContextDigest: identity.ContextDigest, ReviewedPlanDigest: candidate.ReviewedPlanDigest,
		OperationBindingDigest: candidate.OperationBindingDigest, MaxLaunches: 1, UsedLaunches: 0,
		IssuedAt: identity.IssuedAt, ExpiresAt: identity.ExpiresAt, State: "issued",
	}
	response, _ := json.Marshal(admission)
	artifactJSON, _ := json.Marshal(candidate.Artifact)
	_, err = tx.ExecContext(ctx, `INSERT INTO external_stage_launch_admissions(
		admission_id,admission_digest,grant_id,handoff_row_id,handoff_id,credential_epoch,candidate_digest,
		candidate_idempotency_digest,target_ref,workflow_symbol,environment_symbol,artifact_json,reviewed_plan_digest,
		operation_binding_digest,stage_key,attempt_number,plan_revision,execution_number,authority_epoch,plan_digest,
		predecessor_digest,context_digest,max_launches,issued_at,expires_at,state,response_bytes)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,'issued',?)`, admissionID, admissionDigestRaw[:], grant.grantID,
		handoff.rowID, handoff.handoffID, handoff.credentialEpoch, candidateDigest[:], idempotencyDigest[:], candidate.TargetRef,
		candidate.Workflow, candidate.Environment, string(artifactJSON), candidate.ReviewedPlanDigest, candidate.OperationBindingDigest,
		handoff.stageKey, handoff.attemptNumber, handoff.planRevision, handoff.executionNumber, handoff.authorityEpoch,
		identity.PlanDigest, identity.PredecessorDigest, identity.ContextDigest, identity.IssuedAt, identity.ExpiresAt, response)
	if err != nil {
		return nil, mapConflict(err)
	}
	if s.beforeCommit != nil {
		if err := s.beforeCommit("launch_admit"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Service) ConsumeLaunch(ctx context.Context, principal Principal, handoffID, admissionID, idempotencyKey string,
	secret []byte, request ConsumeLaunchAdmissionRequest,
) ([]byte, error) {
	if idempotencyKey == "" || !uuidPattern(admissionID) || request.Schema != LaunchAdmissionSchema ||
		request.Version != LaunchAdmissionVersion || !digestString(request.AdmissionDigest) {
		return nil, ErrInvalid
	}
	requestDigest, _ := canonicalDigest(request)
	idempotencyDigest := sha256.Sum256([]byte(idempotencyKey))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	handoff, err := s.loadHandoffTx(ctx, tx, handoffID)
	if err != nil || s.authenticateExternalTx(ctx, tx, handoff, principal, secret) != nil {
		return nil, ErrNotFound
	}
	var storedDigest, priorRequest, priorIdempotency, receipt []byte
	var state, expiresAt string
	var credentialEpoch int64
	err = tx.QueryRowContext(ctx, `SELECT admission_digest,state,expires_at,credential_epoch,consume_request_digest,consume_idempotency_digest,receipt_bytes
		FROM external_stage_launch_admissions WHERE admission_id=? AND handoff_row_id=?`, admissionID, handoff.rowID).
		Scan(&storedDigest, &state, &expiresAt, &credentialEpoch, &priorRequest, &priorIdempotency, &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	requestedDigest, _ := hex.DecodeString(request.AdmissionDigest[7:])
	if subtle.ConstantTimeCompare(storedDigest, requestedDigest) != 1 || credentialEpoch != handoff.credentialEpoch {
		return nil, ErrConflict
	}
	if state == "consumed" {
		if subtle.ConstantTimeCompare(priorRequest, requestDigest[:]) != 1 || subtle.ConstantTimeCompare(priorIdempotency, idempotencyDigest[:]) != 1 {
			return nil, ErrConflict
		}
		return append([]byte(nil), receipt...), nil
	}
	now := s.clock.Now().UTC()
	expires, expiryErr := time.Parse(time.RFC3339Nano, expiresAt)
	if expiryErr != nil || !expires.After(now) {
		return nil, ErrConflict
	}
	var candidate LaunchCandidate
	var artifactJSON, targetRef, workflow, environment, reviewed, operation string
	err = tx.QueryRowContext(ctx, `SELECT target_ref,workflow_symbol,environment_symbol,artifact_json,reviewed_plan_digest,operation_binding_digest
		FROM external_stage_launch_admissions WHERE admission_id=?`, admissionID).
		Scan(&targetRef, &workflow, &environment, &artifactJSON, &reviewed, &operation)
	if err != nil || json.Unmarshal([]byte(artifactJSON), &candidate.Artifact) != nil {
		return nil, ErrConflict
	}
	candidate.TargetRef, candidate.Workflow, candidate.Environment = targetRef, workflow, environment
	candidate.ReviewedPlanDigest, candidate.OperationBindingDigest = reviewed, operation
	_, expectedArtifact, err := s.validateLaunchAuthorityTx(ctx, tx, handoff, candidate)
	if err != nil {
		return nil, err
	}
	if err := matchBuiltOwnerArtifact(expectedArtifact, &PharosEvidence{
		Workflow: candidate.Workflow, Environment: candidate.Environment,
		Artifact: ArtifactEvidence{Version: candidate.Artifact.Version, Digest: candidate.Artifact.Digest, CommitDigest: candidate.Artifact.CommitDigest},
	}, &candidate.Artifact); err != nil {
		return nil, ErrConflict
	}
	consumedAt := now.Format(time.RFC3339Nano)
	receiptValue := LaunchReceipt{Schema: LaunchAdmissionSchema, Version: LaunchAdmissionVersion, AdmissionID: admissionID,
		AdmissionDigest: request.AdmissionDigest, HandoffID: handoffID, CredentialEpoch: handoff.credentialEpoch,
		LaunchNumber: 1, State: "consumed", ConsumedAt: consumedAt}
	receipt, _ = json.Marshal(receiptValue)
	result, err := tx.ExecContext(ctx, `UPDATE external_stage_launch_admissions SET state='consumed',consume_request_digest=?,
		consume_idempotency_digest=?,receipt_bytes=?,consumed_at=? WHERE admission_id=? AND state='issued'`,
		requestDigest[:], idempotencyDigest[:], receipt, consumedAt, admissionID)
	if err != nil {
		return nil, mapConflict(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrConflict
	}
	if s.beforeCommit != nil {
		if err := s.beforeCommit("launch_consume"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return receipt, nil
}

type storedLaunchAdmission struct {
	response, candidateDigest, idempotencyDigest []byte
	credentialEpoch                              int64
}

func loadAdmissionByHandoff(ctx context.Context, tx *sql.Tx, handoffRowID int64) (storedLaunchAdmission, bool, error) {
	var admission storedLaunchAdmission
	err := tx.QueryRowContext(ctx, `SELECT response_bytes,candidate_digest,candidate_idempotency_digest,credential_epoch
		FROM external_stage_launch_admissions WHERE handoff_row_id=?`, handoffRowID).
		Scan(&admission.response, &admission.candidateDigest, &admission.idempotencyDigest, &admission.credentialEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return storedLaunchAdmission{}, false, nil
	}
	return admission, err == nil, err
}

func (s *Service) validateLaunchAuthorityTx(ctx context.Context, tx *sql.Tx, handoff handoffRow, candidate LaunchCandidate) (launchGrantRow, BuiltOwnerArtifact, error) {
	if handoff.stageKey != "deployment" || handoff.class != "pharos" || handoff.role != "owner" || handoff.state != "accepted" ||
		handoff.revokedAt != "" || handoff.terminalAt != "" || handoff.credentialEpoch < 1 || handoff.workflow != candidate.Workflow ||
		handoff.environment != candidate.Environment {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	now := s.clock.Now().UTC()
	handoffExpiry, err := time.Parse(time.RFC3339Nano, handoff.expiresAt)
	if err != nil || !handoffExpiry.After(now) {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	var registrationTarget string
	err = tx.QueryRowContext(ctx, `SELECT registration.target_ref FROM external_stage_reporter_registrations registration
		WHERE registration.id=? AND registration.delivery_id=? AND registration.project_id=? AND registration.target_ref=? AND `+LivePharosOwnerSQL,
		handoff.registrationID, handoff.deliveryID, handoff.projectID, candidate.TargetRef).Scan(&registrationTarget)
	if err != nil || registrationTarget != candidate.TargetRef {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	var grant launchGrantRow
	err = tx.QueryRowContext(ctx, `SELECT grant.grant_id,grant.revision,grant.grant_digest,grant.project_id,grant.draft_id,
		grant.draft_revision,grant.review_id,grant.batch_id,grant.delivery_id,grant.attempt_id,grant.attempt_number,
		grant.plan_revision,grant.baseline_ref,grant.content_digest,grant.revision_seal,grant.scope_digest,grant.worker_digest,grant.target_ref,
		grant.workflow_symbol,grant.environment_symbol,grant.max_launches,grant.human_user_id,grant.human_session_credential_id,
		grant.issued_at,grant.expires_at,COALESCE(grant.revoked_at,''),batch.lifecycle_intent_id,batch.scope_json,
		batch.worker_json,batch.delegated_launch_json,review.binding_hash,review.selected_requirement_refs_json
		FROM baseline_batch_launch_grants grant
		JOIN baseline_batch_batches batch ON batch.id=grant.batch_id AND batch.project_id=grant.project_id
		 AND batch.draft_id=grant.draft_id AND batch.draft_revision=grant.draft_revision AND batch.review_id=grant.review_id
		 AND batch.delivery_id=grant.delivery_id AND batch.attempt_id=grant.attempt_id AND batch.control_state='started'
		 AND batch.execution_mode='automatic' AND batch.baseline_ref=grant.baseline_ref
		 AND batch.content_digest=grant.content_digest AND batch.revision_seal=grant.revision_seal AND batch.started_by=grant.human_user_id
		JOIN baseline_batch_reviews review ON review.id=grant.review_id AND review.draft_id=grant.draft_id
		 AND review.draft_revision=grant.draft_revision AND review.execution_mode='automatic' AND review.confirmed=1
		 AND review.invalidated_at IS NULL AND review.human_user_id=grant.human_user_id
		 AND review.session_credential_id=grant.human_session_credential_id AND review.worker_json=batch.worker_json
		 AND review.delegated_launch_json=batch.delegated_launch_json
		JOIN baseline_batch_drafts draft ON draft.id=grant.draft_id AND draft.project_id=grant.project_id
		 AND draft.revision=grant.draft_revision AND draft.status='closed' AND draft.execution_mode='automatic'
		 AND draft.baseline_ref=grant.baseline_ref AND draft.content_digest=grant.content_digest
		 AND draft.revision_seal=grant.revision_seal AND draft.worker_json=batch.worker_json
		 AND draft.delegated_launch_json=batch.delegated_launch_json
		 AND draft.selected_requirement_refs_json=review.selected_requirement_refs_json
		JOIN projects project ON project.id=grant.project_id AND project.status='active'
		JOIN users human ON human.id=grant.human_user_id AND human.status='active'
		JOIN sessions session ON session.credential_id=grant.human_session_credential_id AND session.user_id=grant.human_user_id
		 AND session.acting_as_user_id IS NULL AND julianday(session.expires_at)>julianday(?)
		WHERE grant.delivery_id=? AND grant.attempt_id=? AND grant.project_id=?
		 AND (human.role IN ('admin','super_admin') OR EXISTS(
		  SELECT 1 FROM project_members member WHERE member.user_id=human.id AND member.project_id=grant.project_id AND member.access_level='editor'
		 ) OR (human.role='member' AND NOT EXISTS(
		  SELECT 1 FROM project_members member WHERE member.user_id=human.id AND member.project_id=grant.project_id
		 )))`, now.Format(time.RFC3339Nano), handoff.deliveryID, handoff.attemptID, handoff.projectID).
		Scan(&grant.grantID, &grant.revision, &grant.grantDigest, &grant.projectID, &grant.draftID, &grant.draftRevision,
			&grant.reviewID, &grant.batchID, &grant.deliveryID, &grant.attemptID, &grant.attemptNumber, &grant.planRevision,
			&grant.baselineRef, &grant.contentDigest, &grant.revisionSeal, &grant.scopeDigest, &grant.workerDigest, &grant.targetRef,
			&grant.workflow, &grant.environment, &grant.maxLaunches, &grant.humanUserID, &grant.humanSession,
			&grant.issuedAt, &grant.expiresAt, &grant.revokedAt, &grant.lifecycleIntentID, &grant.scopeJSON,
			&grant.workerJSON, &grant.delegatedJSON, &grant.reviewBinding, &grant.reviewSelectedJSON)
	if err != nil || grant.revokedAt != "" || grant.targetRef != candidate.TargetRef || grant.workflow != candidate.Workflow ||
		grant.environment != candidate.Environment || grant.maxLaunches != 1 || grant.attemptNumber != handoff.attemptNumber ||
		grant.planRevision != handoff.planRevision || !validLaunchGrantBinding(grant) {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	rootExpiry, err := time.Parse(time.RFC3339Nano, grant.expiresAt)
	if err != nil || !rootExpiry.After(now) {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	if !currentProjectTargetRef(ctx, tx, grant.projectID, candidate.TargetRef, candidate.Environment) {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	var currentAttempt, currentPlan int64
	if tx.QueryRowContext(ctx, `SELECT id,plan_revision FROM delivery_attempts WHERE delivery_id=? ORDER BY attempt_number DESC LIMIT 1`, handoff.deliveryID).
		Scan(&currentAttempt, &currentPlan) != nil || currentAttempt != handoff.attemptID || currentPlan != handoff.planRevision {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	var currentExecution, currentAuthority, currentStart, currentAuthorityEvent int64
	if tx.QueryRowContext(ctx, `SELECT execution_number,authority_epoch,execution_start_stage_event_id,authority_stage_event_id
		FROM delivery_stage_latest WHERE delivery_id=? AND attempt_id=? AND stage_key='deployment'`, handoff.deliveryID, handoff.attemptID).
		Scan(&currentExecution, &currentAuthority, &currentStart, &currentAuthorityEvent) != nil ||
		currentExecution != handoff.executionNumber || currentAuthority != handoff.authorityEpoch ||
		currentStart != handoff.startEventID || currentAuthorityEvent != handoff.authorityEventID {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	var lifecycleState string
	if tx.QueryRowContext(ctx, `SELECT state FROM lifecycle_intents WHERE id=? AND project_id=?`, grant.lifecycleIntentID, grant.projectID).Scan(&lifecycleState) != nil ||
		lifecycleState == "cancelled" || lifecycleState == "failed" || lifecycleState == "expired" {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	var qaState string
	if tx.QueryRowContext(ctx, `SELECT event.semantic_state FROM delivery_stage_latest latest
		JOIN delivery_stage_events event ON event.id=latest.semantic_stage_event_id
		WHERE latest.delivery_id=? AND latest.attempt_id=? AND latest.stage_key='qa'`, handoff.deliveryID, handoff.attemptID).Scan(&qaState) != nil || qaState != "succeeded" {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	artifact, err := LoadExplicitBuiltArtifact(ctx, tx, handoff.deliveryID, handoff.attemptID)
	if err != nil || !artifact.Complete() {
		return launchGrantRow{}, BuiltOwnerArtifact{}, ErrConflict
	}
	return grant, artifact, nil
}

func validLaunchGrantBinding(grant launchGrantRow) bool {
	scopeDigest := sha256.Sum256([]byte(grant.scopeJSON))
	workerDigest := sha256.Sum256([]byte(grant.workerJSON))
	if subtle.ConstantTimeCompare(scopeDigest[:], grant.scopeDigest) != 1 ||
		subtle.ConstantTimeCompare(workerDigest[:], grant.workerDigest) != 1 {
		return false
	}
	var scope launchScope
	var worker launchWorker
	var selection launchSelection
	if json.Unmarshal([]byte(grant.scopeJSON), &scope) != nil || json.Unmarshal([]byte(grant.workerJSON), &worker) != nil ||
		json.Unmarshal([]byte(grant.delegatedJSON), &selection) != nil {
		return false
	}
	canonicalRefs := append([]string(nil), scope.RequirementRefs...)
	sort.Strings(canonicalRefs)
	selectedJSON, _ := json.Marshal(canonicalRefs)
	if string(selectedJSON) != grant.reviewSelectedJSON || selection.TargetRef != grant.targetRef ||
		selection.Workflow != grant.workflow || selection.Environment != grant.environment ||
		selection.ExpiresAt != grant.expiresAt || selection.MaxLaunches != grant.maxLaunches {
		return false
	}
	reviewParts := []string{
		grant.contentDigest, grant.revisionSeal, strconv.FormatInt(grant.draftRevision, 10), "automatic",
		string(selectedJSON), worker.RuntimeID, worker.RuntimeGeneration, worker.AccountKey, worker.AccountLabel,
		worker.ProfileID, worker.ProfileVersion, worker.WorkspaceHandle, worker.WorkerName, grant.delegatedJSON,
	}
	reviewDigest := sha256.Sum256([]byte(strings.Join(reviewParts, "\x00")))
	if grant.reviewBinding != hex.EncodeToString(reviewDigest[:]) {
		return false
	}
	identity := struct {
		Domain         string `json:"domain"`
		GrantID        string `json:"grant_id"`
		Revision       int64  `json:"revision"`
		ProjectID      int64  `json:"project_id"`
		DraftID        int64  `json:"draft_id"`
		DraftRevision  int64  `json:"draft_revision"`
		ReviewID       int64  `json:"review_id"`
		BatchID        int64  `json:"batch_id"`
		DeliveryID     int64  `json:"delivery_id"`
		AttemptID      int64  `json:"attempt_id"`
		AttemptNumber  int64  `json:"attempt_number"`
		PlanRevision   int64  `json:"plan_revision"`
		BaselineRef    string `json:"baseline_ref"`
		ContentDigest  string `json:"content_digest"`
		RevisionSeal   string `json:"revision_seal"`
		ScopeDigest    string `json:"scope_digest"`
		WorkerDigest   string `json:"worker_digest"`
		TargetRef      string `json:"target_ref"`
		Workflow       string `json:"workflow"`
		Environment    string `json:"environment"`
		MaxLaunches    int    `json:"max_launches"`
		HumanUserID    int64  `json:"human_user_id"`
		HumanSessionID string `json:"human_session_credential_id"`
		IssuedAt       string `json:"issued_at"`
		ExpiresAt      string `json:"expires_at"`
	}{"paimos.baseline-batch.launch-grant.v1", grant.grantID, grant.revision, grant.projectID, grant.draftID,
		grant.draftRevision, grant.reviewID, grant.batchID, grant.deliveryID, grant.attemptID, grant.attemptNumber,
		grant.planRevision, grant.baselineRef, grant.contentDigest, grant.revisionSeal, hex.EncodeToString(scopeDigest[:]),
		hex.EncodeToString(workerDigest[:]), grant.targetRef, grant.workflow, grant.environment, grant.maxLaunches,
		grant.humanUserID, grant.humanSession, grant.issuedAt, grant.expiresAt}
	raw, err := json.Marshal(identity)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(raw)
	return subtle.ConstantTimeCompare(digest[:], grant.grantDigest) == 1
}

func currentProjectTargetRef(ctx context.Context, tx *sql.Tx, projectID int64, targetRef, environment string) bool {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM project_environments WHERE project_id=?`, projectID)
	if err != nil {
		return false
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		identity, err := targetidentity.LoadProjectEnvironment(ctx, tx, projectID, id)
		if err == nil && identity.EnvironmentSymbol == environment && targetidentity.Digest(identity) == targetRef {
			return true
		}
	}
	return false
}

func uuidPattern(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
)

type activationBinding struct {
	RunnerTarget
	projectID     int64
	projectStatus string
	projectKey    string
	issueNumber   int64
	deviceID      string
	bindingDigest [32]byte
}

func resolveActivation(ctx context.Context, tx *sql.Tx, runID int64) (activationBinding, error) {
	if runID <= 0 {
		return activationBinding{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	var binding activationBinding
	err := tx.QueryRowContext(ctx, `SELECT delivery.id,delivery.delivery_key,
		COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event WHERE event.delivery_id=delivery.id),0),
		issue.id,control_revision.revision,attempt.id,attempt.attempt_number,attempt.plan_revision,
		link.stage_key,link.execution_number,link.execution_start_stage_event_id,
		activation.authority_epoch,activation.authority_stage_event_id,activation.reporter_id,run.id,
		issue.project_id,project.status,project.key,issue.issue_number,run.device_id
		FROM agent_runs run
		JOIN delivery_agent_run_links link ON link.agent_run_id=run.id
		JOIN deliveries delivery ON delivery.id=link.delivery_id AND delivery.issue_id=run.issue_id
		JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		JOIN delivery_attempts attempt ON attempt.id=link.attempt_id AND attempt.delivery_id=delivery.id
		JOIN delivery_agent_run_activations activation ON activation.delivery_id=delivery.id
		 AND activation.attempt_id=attempt.id AND activation.stage_key=link.stage_key
		 AND activation.execution_number=link.execution_number AND activation.agent_run_id=run.id
		 AND activation.reporter_id=link.reporter_id
		JOIN delivery_stage_latest latest ON latest.delivery_id=delivery.id AND latest.attempt_id=attempt.id
		 AND latest.stage_key=link.stage_key AND latest.execution_number=link.execution_number
		 AND latest.execution_start_stage_event_id=link.execution_start_stage_event_id
		 AND latest.authority_epoch=activation.authority_epoch
		 AND latest.authority_stage_event_id=activation.authority_stage_event_id
		 AND latest.current_reporter_id=activation.reporter_id
		WHERE run.id=? AND run.status='running' AND run.delivery_instrumentation_version=1
		ORDER BY activation.authority_epoch DESC LIMIT 1`, runID).Scan(
		&binding.DeliveryID, &binding.DeliveryKey, &binding.DeliveryRevision,
		&binding.RootIssueID, &binding.IssueRevision, &binding.AttemptID, &binding.AttemptNumber,
		&binding.PlanRevision, &binding.StageKey, &binding.ExecutionNumber,
		&binding.ExecutionStartStageEventID, &binding.AuthorityEpoch, &binding.AuthorityStageEventID,
		&binding.ReporterID, &binding.RunID, &binding.projectID, &binding.projectStatus,
		&binding.projectKey, &binding.issueNumber, &binding.deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return activationBinding{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return activationBinding{}, storageError(ctx, err)
	}
	binding.bindingDigest = canonicalDigest("activation-binding",
		intField("delivery_id", binding.DeliveryID), stringField("delivery_key", binding.DeliveryKey),
		intField("delivery_revision", binding.DeliveryRevision), intField("root_issue_id", binding.RootIssueID),
		intField("issue_revision", binding.IssueRevision), intField("attempt_id", binding.AttemptID),
		intField("attempt_number", binding.AttemptNumber), intField("plan_revision", binding.PlanRevision),
		stringField("stage_key", binding.StageKey), intField("execution_number", binding.ExecutionNumber),
		intField("execution_start_stage_event_id", binding.ExecutionStartStageEventID),
		intField("authority_epoch", binding.AuthorityEpoch), intField("authority_stage_event_id", binding.AuthorityStageEventID),
		intField("reporter_id", binding.ReporterID), intField("run_id", binding.RunID), stringField("device_id", binding.deviceID))
	return binding, nil
}

func runnerActions(projectStatus string, supported []Action) ([]Action, error) {
	canonical, err := canonicalActions(supported, true)
	if err != nil {
		return nil, err
	}
	serverTruth := []Action{"run.cancel.running", "input.respond", "run.pause", "run.resume"}
	if projectStatus == "archived" {
		serverTruth = []Action{"run.cancel.running"}
	} else if projectStatus != "active" && projectStatus != "frozen" {
		return nil, domainError(ErrNotFound, CodeTargetNotFound)
	}
	actions := intersectActions(serverTruth, canonical)
	if len(actions) == 0 {
		return nil, domainError(ErrConflict, CodeCapabilityUnavailable)
	}
	return actions, nil
}

func (s *Service) IssueRunnerLease(ctx context.Context, principal auth.Principal, request LeaseIssueRequest) (LeaseProjection, error) {
	return s.putRunnerLease(ctx, principal, "lease.issue", "", 0, request.RunID, request.DeviceID,
		request.SupportedActions, request.OperationKeyDigest)
}

func (s *Service) RenewRunnerLease(ctx context.Context, principal auth.Principal, request LeaseRenewRequest) (LeaseProjection, error) {
	if !validUUID(request.LeaseID) || request.Revision <= 0 {
		return LeaseProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	// Resolve the run only after transactional reauthorization; zero here tells
	// putRunnerLease to load it from the named current lease.
	return s.putRunnerLease(ctx, principal, "lease.renew", request.LeaseID, request.Revision, 0,
		request.DeviceID, request.SupportedActions, request.OperationKeyDigest)
}

func (s *Service) putRunnerLease(ctx context.Context, principal auth.Principal, operation, expectedID string, expectedRevision, runID int64,
	deviceID string, supported []Action, operationKey [32]byte) (LeaseProjection, error) {
	if principal.Kind() != auth.PrincipalAPIKey {
		return LeaseProjection{}, domainError(ErrForbidden, CodeForbidden)
	}
	if err := validateDevice(deviceID); err != nil {
		return LeaseProjection{}, err
	}
	canonicalSupported, err := canonicalActions(supported, true)
	if err != nil {
		return LeaseProjection{}, err
	}
	keyDigest, err := operationKeyDigest(operationKey)
	if err != nil {
		return LeaseProjection{}, err
	}
	requestFields := []digestField{intField("run_id", runID), stringField("lease_id", expectedID),
		intField("revision", expectedRevision), stringField("device_id", deviceID)}
	requestFields = append(requestFields, actionFields("supported_action", canonicalSupported)...)
	requestDigest := canonicalDigest(operation, requestFields...)
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeRunner)
	if err != nil {
		return LeaseProjection{}, err
	}
	defer authz.tx.Rollback()
	if expectedID != "" {
		if err := authz.tx.QueryRowContext(ctx, `SELECT agent_run_id FROM control_capability_leases
			WHERE lease_id=? AND revision=? AND user_id=?`, expectedID, expectedRevision, authz.principal.UserID()).Scan(&runID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return LeaseProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
			}
			return LeaseProjection{}, storageError(ctx, err)
		}
		// Digest the resolved immutable run so renew replay cannot change its
		// request meaning merely because the caller omitted it by design.
		requestFields[0] = intField("run_id", runID)
		requestDigest = canonicalDigest(operation, requestFields...)
	}
	binding, err := resolveActivation(ctx, authz.tx, runID)
	if err != nil {
		return LeaseProjection{}, err
	}
	if deviceID != binding.deviceID {
		return LeaseProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, binding.projectID); err != nil {
		return LeaseProjection{}, err
	}
	actions, err := runnerActions(binding.projectStatus, canonicalSupported)
	if err != nil {
		return LeaseProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, operation, keyDigest, requestDigest)
	if err != nil {
		return LeaseProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadLeaseProjectionTx(ctx, authz.tx, authz.principal.UserID(), replay.leaseID.String, true)
		if loadErr != nil {
			return LeaseProjection{}, loadErr
		}
		if err := s.finishRead(authz); err != nil {
			return LeaseProjection{}, err
		}
		return projection, nil
	}
	actionDigest := digestActions(actions)
	leaseID, revision, current, currentCredential, currentDevice, currentBinding, currentActions, expiresAt, err :=
		currentLeaseTx(ctx, authz.tx, binding)
	if err != nil {
		return LeaseProjection{}, err
	}
	if expectedID != "" && (leaseID != expectedID || revision != expectedRevision || !current) {
		return LeaseProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	credential := authz.principal.SafeCredentialID()
	if current && expiredAt(s.clock.Now().UTC(), expiresAt) {
		if err := revokeLeaseRow(ctx, authz.tx, leaseID, revision, "lease_expired", "lease_expired"); err != nil {
			return LeaseProjection{}, err
		}
		current = false
		revision++
	}
	if current && currentCredential == credential && currentDevice == deviceID &&
		bytes.Equal(currentBinding, binding.bindingDigest[:]) && bytes.Equal(currentActions, actionDigest[:]) {
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_capability_leases SET
			expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','+90 seconds'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE lease_id=? AND revision=?`,
			leaseID, revision); err != nil {
			return LeaseProjection{}, sqliteConflict(err)
		}
		if err := insertLeaseEvent(ctx, authz.tx, leaseID, revision, "lease_renewed", ""); err != nil {
			return LeaseProjection{}, err
		}
	} else {
		if current {
			reason := "authority_changed"
			if currentCredential != credential {
				reason = "credential_revoked"
			}
			if err := revokeLeaseRow(ctx, authz.tx, leaseID, revision, "lease_revoked", reason); err != nil {
				return LeaseProjection{}, err
			}
			revision++
		}
		if leaseID == "" {
			leaseID, err = safeID(s.ids)
			if err != nil {
				return LeaseProjection{}, err
			}
			revision = 1
		}
		if err := insertLease(ctx, authz.tx, authz.principal, binding, leaseID, revision, actions, actionDigest); err != nil {
			return LeaseProjection{}, err
		}
		kind := "lease_issued"
		if revision > 1 {
			kind = "lease_renewed"
		}
		if err := insertLeaseEvent(ctx, authz.tx, leaseID, revision, kind, ""); err != nil {
			return LeaseProjection{}, err
		}
	}
	if containsAction(actions, "run.pause") {
		if _, err := authz.tx.ExecContext(ctx, `INSERT INTO control_runtime_states(
			agent_run_id,delivery_id,root_issue_id,attempt_id,stage_key,execution_number,
			execution_start_stage_event_id,state,revision)
			SELECT ?,?,?,?,?,?,?,'running',1 WHERE NOT EXISTS(
			 SELECT 1 FROM control_runtime_states WHERE agent_run_id=?)`, binding.RunID, binding.DeliveryID,
			binding.RootIssueID, binding.AttemptID, binding.StageKey, binding.ExecutionNumber,
			binding.ExecutionStartStageEventID, binding.RunID); err != nil {
			return LeaseProjection{}, sqliteConflict(err)
		}
	}
	projection, err := loadLeaseProjectionTx(ctx, authz.tx, authz.principal.UserID(), leaseID, true)
	if err != nil {
		return LeaseProjection{}, err
	}
	resultDigest := leaseProjectionDigest(projection)
	if err := insertOperation(ctx, authz.tx, authz.principal, operation, keyDigest, requestDigest, resultDigest, "lease_id", leaseID); err != nil {
		return LeaseProjection{}, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return LeaseProjection{}, err
	}
	return projection, nil
}

func currentLeaseTx(ctx context.Context, tx *sql.Tx, binding activationBinding) (id string, revision int64, current bool,
	credential, device string, bindingDigest, actions []byte, expiresAt time.Time, returnErr error) {
	var apiKey int64
	var expiry string
	err := tx.QueryRowContext(ctx, `SELECT lease_id,revision,actor_api_key_id,device_id,binding_digest,
		action_set_digest,expires_at,revoked_at IS NULL FROM control_capability_leases
		WHERE delivery_id=? AND attempt_id=? AND stage_key=? AND execution_number=?
		ORDER BY revision DESC LIMIT 1`, binding.DeliveryID, binding.AttemptID, binding.StageKey, binding.ExecutionNumber).
		Scan(&id, &revision, &apiKey, &device, &bindingDigest, &actions, &expiry, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, "", "", nil, nil, time.Time{}, nil
	}
	if err != nil {
		return "", 0, false, "", "", nil, nil, time.Time{}, storageError(ctx, err)
	}
	parsed, err := parseControlTime(expiry)
	if err != nil {
		return "", 0, false, "", "", nil, nil, time.Time{}, err
	}
	return id, revision, current, strconv.FormatInt(apiKey, 10), device, bindingDigest, actions, parsed, nil
}

func insertLease(ctx context.Context, tx *sql.Tx, principal auth.Principal, binding activationBinding, leaseID string,
	revision int64, actions []Action, actionDigest [32]byte) error {
	if principal.Kind() != auth.PrincipalAPIKey {
		return domainError(ErrForbidden, CodeForbidden)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_capability_leases(
		lease_id,revision,actor_user_id,user_id,principal_kind,actor_api_key_id,device_id,
		delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		attempt_id,attempt_number,plan_revision,stage_key,execution_number,execution_start_stage_event_id,
		authority_epoch,authority_stage_event_id,reporter_id,agent_run_id,binding_digest,
		action_set_digest,action_count,expires_at)
		VALUES(?,?,?,?,'api_key',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
		strftime('%Y-%m-%dT%H:%M:%fZ','now','+90 seconds'))`,
		leaseID, revision, principal.UserID(), principal.UserID(), principal.APIKeyID(), binding.deviceID,
		binding.DeliveryID, binding.DeliveryKey, binding.DeliveryRevision, binding.projectID,
		binding.RootIssueID, binding.IssueRevision, binding.AttemptID, binding.AttemptNumber, binding.PlanRevision,
		binding.StageKey, binding.ExecutionNumber, binding.ExecutionStartStageEventID, binding.AuthorityEpoch,
		binding.AuthorityStageEventID, binding.ReporterID, binding.RunID, binding.bindingDigest[:], actionDigest[:], len(actions))
	if err != nil {
		return sqliteConflict(err)
	}
	for _, action := range actions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO control_capability_lease_actions(lease_id,lease_revision,action)
			VALUES(?,?,?)`, leaseID, revision, action); err != nil {
			return sqliteConflict(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_capability_lease_seals(
		lease_id,lease_revision,binding_digest,action_set_digest,action_count) VALUES(?,?,?,?,?)`,
		leaseID, revision, binding.bindingDigest[:], actionDigest[:], len(actions)); err != nil {
		return sqliteConflict(err)
	}
	return nil
}

func revokeLeaseRow(ctx context.Context, tx *sql.Tx, id string, revision int64, eventKind, reason string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE control_capability_leases SET
		revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE lease_id=? AND revision=? AND revoked_at IS NULL`, id, revision); err != nil {
		return sqliteConflict(err)
	}
	return insertLeaseEvent(ctx, tx, id, revision, eventKind, reason)
}

func insertLeaseEvent(ctx context.Context, tx *sql.Tx, id string, revision int64, eventKind, reason string) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM control_events WHERE lease_id=?`, id).Scan(&sequence); err != nil {
		return storageError(ctx, err)
	}
	var safeReason any
	if reason != "" {
		safeReason = reason
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_events(
		sequence,event_kind,lease_id,lease_revision,executor_user_id,executor_principal_kind,
		executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,attempt_id,stage_key,
		execution_number,authority_epoch,reporter_id,agent_run_id,binding_digest,action_set_digest,
		subject_expires_at,subject_updated_at,safe_reason)
		SELECT ?,?,lease_id,revision,user_id,'api_key',actor_api_key_id,device_id,delivery_id,
		root_issue_id,issue_revision,attempt_id,stage_key,execution_number,authority_epoch,reporter_id,
		agent_run_id,binding_digest,action_set_digest,expires_at,updated_at,?
		FROM control_capability_leases WHERE lease_id=? AND revision=?`, sequence, eventKind, safeReason, id, revision)
	if err != nil {
		return sqliteConflict(err)
	}
	return nil
}

func loadLeaseProjectionTx(ctx context.Context, tx *sql.Tx, userID int64, leaseID string, requireLive bool) (LeaseProjection, error) {
	if !validUUID(leaseID) {
		return LeaseProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	var projection LeaseProjection
	var expiry string
	var projectID int64
	query := `SELECT lease.lease_id,lease.revision,lease.delivery_key,project.key||'-'||issue.issue_number,
		lease.expires_at,lease.project_id,lease.delivery_id,lease.delivery_revision,lease.root_issue_id,
		lease.issue_revision,lease.attempt_id,lease.attempt_number,lease.plan_revision,lease.stage_key,
		lease.execution_number,lease.execution_start_stage_event_id,lease.authority_epoch,
		lease.authority_stage_event_id,lease.reporter_id,lease.agent_run_id
		FROM control_capability_leases lease
		JOIN issues issue ON issue.id=lease.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=issue.project_id
		WHERE lease.lease_id=? AND lease.user_id=?`
	if requireLive {
		query += ` AND lease.revision=(SELECT MAX(revision) FROM control_capability_leases WHERE lease_id=lease.lease_id)
		 AND lease.revoked_at IS NULL AND lease.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	}
	query += ` ORDER BY lease.revision DESC LIMIT 1`
	err := tx.QueryRowContext(ctx, query, leaseID, userID).Scan(&projection.LeaseID, &projection.Revision,
		&projection.DeliveryKey, &projection.IssueKey, &expiry, &projectID,
		&projection.Target.DeliveryID, &projection.Target.DeliveryRevision, &projection.Target.RootIssueID,
		&projection.Target.IssueRevision, &projection.Target.AttemptID, &projection.Target.AttemptNumber,
		&projection.Target.PlanRevision, &projection.Target.StageKey, &projection.Target.ExecutionNumber,
		&projection.Target.ExecutionStartStageEventID, &projection.Target.AuthorityEpoch,
		&projection.Target.AuthorityStageEventID, &projection.Target.ReporterID, &projection.Target.RunID)
	projection.Target.DeliveryKey = projection.DeliveryKey
	if errors.Is(err, sql.ErrNoRows) {
		return LeaseProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return LeaseProjection{}, storageError(ctx, err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT action FROM control_capability_lease_actions
		WHERE lease_id=? AND lease_revision=? ORDER BY CASE action WHEN 'run.cancel.running' THEN 1
		WHEN 'input.respond' THEN 2 WHEN 'run.pause' THEN 3 WHEN 'run.resume' THEN 4 ELSE 99 END`,
		projection.LeaseID, projection.Revision)
	if err != nil {
		return LeaseProjection{}, storageError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var action Action
		if err := rows.Scan(&action); err != nil {
			return LeaseProjection{}, storageError(ctx, err)
		}
		projection.Actions = append(projection.Actions, action)
	}
	if err := requireProjectStatusForActions(ctx, tx, projectID, projection.Actions); err != nil {
		return LeaseProjection{}, err
	}
	projection.ExpiresAt, err = parseControlTime(expiry)
	return projection, err
}

func leaseProjectionDigest(projection LeaseProjection) [32]byte {
	fields := []digestField{stringField("lease_id", projection.LeaseID), intField("revision", projection.Revision),
		stringField("delivery_key", projection.DeliveryKey), intField("run_id", projection.Target.RunID),
		intField("authority_epoch", projection.Target.AuthorityEpoch),
		stringField("expires_at", projection.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"))}
	fields = append(fields, actionFields("action", projection.Actions)...)
	return canonicalDigest("lease-projection", fields...)
}

func (s *Service) RevokeRunnerLease(ctx context.Context, principal auth.Principal, request LeaseRevokeRequest) (LeaseProjection, error) {
	if principal.Kind() != auth.PrincipalAPIKey || !validUUID(request.LeaseID) || request.Revision <= 0 {
		return LeaseProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	keyDigest, err := operationKeyDigest(request.OperationKeyDigest)
	if err != nil {
		return LeaseProjection{}, err
	}
	requestDigest := canonicalDigest("lease.revoke", stringField("lease_id", request.LeaseID), intField("revision", request.Revision))
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeRunner)
	if err != nil {
		return LeaseProjection{}, err
	}
	defer authz.tx.Rollback()
	var projectID, ownerID int64
	if err := authz.tx.QueryRowContext(ctx, `SELECT project_id,user_id FROM control_capability_leases WHERE lease_id=? AND revision=?`,
		request.LeaseID, request.Revision).Scan(&projectID, &ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LeaseProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
		}
		return LeaseProjection{}, storageError(ctx, err)
	}
	if ownerID != authz.principal.UserID() {
		return LeaseProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, projectID); err != nil {
		return LeaseProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "lease.revoke", keyDigest, requestDigest)
	if err != nil {
		return LeaseProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadLeaseProjectionTx(ctx, authz.tx, authz.principal.UserID(), request.LeaseID, false)
		if loadErr != nil {
			return LeaseProjection{}, loadErr
		}
		if err := s.finishRead(authz); err != nil {
			return LeaseProjection{}, err
		}
		return projection, nil
	}
	projection, err := loadLeaseProjectionTx(ctx, authz.tx, authz.principal.UserID(), request.LeaseID, true)
	if err != nil {
		return LeaseProjection{}, err
	}
	if projection.Revision != request.Revision {
		return LeaseProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if err := revokeLeaseRow(ctx, authz.tx, request.LeaseID, request.Revision, "lease_revoked", "lease_revoked"); err != nil {
		return LeaseProjection{}, err
	}
	if err := insertOperation(ctx, authz.tx, authz.principal, "lease.revoke", keyDigest, requestDigest,
		leaseProjectionDigest(projection), "lease_id", request.LeaseID); err != nil {
		return LeaseProjection{}, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return LeaseProjection{}, err
	}
	return projection, nil
}

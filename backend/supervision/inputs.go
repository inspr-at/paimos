// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/inspr-at/paimos/backend/auth"
)

type leaseRecord struct {
	leaseID         string
	revision        int64
	userID          int64
	apiKeyID        int64
	deviceID        string
	projectID       int64
	projectStatus   string
	issueNumber     int64
	projectKey      string
	expiresAt       string
	bindingDigest   []byte
	actionSetDigest []byte
	target          RunnerTarget
}

func loadCurrentLeaseRecordTx(ctx context.Context, tx *sql.Tx, principal auth.Principal, leaseID string, revision int64,
	deviceID string, required Action) (leaseRecord, error) {
	return loadLeaseRecordTx(ctx, tx, principal, leaseID, revision, deviceID, required, true)
}

func loadLeaseRecordTx(ctx context.Context, tx *sql.Tx, principal auth.Principal, leaseID string, revision int64,
	deviceID string, required Action, requireActiveRun bool) (leaseRecord, error) {
	if principal.Kind() != auth.PrincipalAPIKey || !validUUID(leaseID) || revision <= 0 ||
		(required != "" && !knownActions[required]) {
		return leaseRecord{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	var record leaseRecord
	err := tx.QueryRowContext(ctx, `SELECT lease.lease_id,lease.revision,lease.user_id,lease.actor_api_key_id,
		lease.device_id,lease.project_id,project.status,issue.issue_number,project.key,lease.expires_at,
		lease.binding_digest,lease.action_set_digest,lease.delivery_id,lease.delivery_key,
		lease.delivery_revision,lease.root_issue_id,lease.issue_revision,lease.attempt_id,
		lease.attempt_number,lease.plan_revision,lease.stage_key,lease.execution_number,
		lease.execution_start_stage_event_id,lease.authority_epoch,lease.authority_stage_event_id,
		lease.reporter_id,lease.agent_run_id
		FROM control_capability_leases lease
		JOIN issues issue ON issue.id=lease.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=lease.project_id AND project.id=issue.project_id
		JOIN control_capability_lease_seals seal ON seal.lease_id=lease.lease_id
		 AND seal.lease_revision=lease.revision AND seal.binding_digest=lease.binding_digest
		 AND seal.action_set_digest=lease.action_set_digest
		JOIN control_capability_lease_actions action ON action.lease_id=lease.lease_id
		 AND action.lease_revision=lease.revision AND (?='' OR action.action=?)
		WHERE lease.lease_id=? AND lease.revision=? AND lease.user_id=? AND lease.actor_api_key_id=?
		 AND lease.revoked_at IS NULL AND lease.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		required, required, leaseID, revision, principal.UserID(), principal.APIKeyID()).Scan(
		&record.leaseID, &record.revision, &record.userID, &record.apiKeyID, &record.deviceID,
		&record.projectID, &record.projectStatus, &record.issueNumber, &record.projectKey,
		&record.expiresAt, &record.bindingDigest, &record.actionSetDigest,
		&record.target.DeliveryID, &record.target.DeliveryKey, &record.target.DeliveryRevision,
		&record.target.RootIssueID, &record.target.IssueRevision, &record.target.AttemptID,
		&record.target.AttemptNumber, &record.target.PlanRevision, &record.target.StageKey,
		&record.target.ExecutionNumber, &record.target.ExecutionStartStageEventID,
		&record.target.AuthorityEpoch, &record.target.AuthorityStageEventID, &record.target.ReporterID,
		&record.target.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return leaseRecord{}, domainError(ErrConflict, CodeCapabilityUnavailable)
	}
	if err != nil {
		return leaseRecord{}, storageError(ctx, err)
	}
	if deviceID != "" && deviceID != record.deviceID {
		return leaseRecord{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if record.projectStatus != "active" && record.projectStatus != "frozen" &&
		!(record.projectStatus == "archived" && (required == "" || required == Action("run.cancel.running"))) {
		return leaseRecord{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if requireActiveRun {
		current, resolveErr := resolveActivation(ctx, tx, record.target.RunID)
		if resolveErr != nil || current.RunnerTarget != record.target {
			return leaseRecord{}, domainError(ErrConflict, CodeStaleTarget)
		}
	} else {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM deliveries delivery
			JOIN issue_control_revisions control_revision ON control_revision.issue_id=? AND control_revision.revision=?
			JOIN delivery_agent_run_activations activation ON activation.delivery_id=? AND activation.attempt_id=?
			 AND activation.stage_key=? AND activation.execution_number=? AND activation.authority_epoch=?
			 AND activation.authority_stage_event_id=? AND activation.reporter_id=? AND activation.agent_run_id=?
			JOIN delivery_stage_latest latest ON latest.delivery_id=activation.delivery_id
			 AND latest.attempt_id=activation.attempt_id AND latest.stage_key=activation.stage_key
			 AND latest.execution_number=activation.execution_number AND latest.authority_epoch=activation.authority_epoch
			 AND latest.authority_stage_event_id=activation.authority_stage_event_id
			 AND latest.current_reporter_id=activation.reporter_id AND latest.execution_start_stage_event_id=?
			WHERE delivery.id=? AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			 WHERE event.delivery_id=delivery.id),0)=?`, record.target.RootIssueID, record.target.IssueRevision,
			record.target.DeliveryID, record.target.AttemptID, record.target.StageKey, record.target.ExecutionNumber,
			record.target.AuthorityEpoch, record.target.AuthorityStageEventID, record.target.ReporterID,
			record.target.RunID, record.target.ExecutionStartStageEventID, record.target.DeliveryID,
			record.target.DeliveryRevision).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return leaseRecord{}, domainError(ErrConflict, CodeStaleTarget)
		}
		if err != nil {
			return leaseRecord{}, storageError(ctx, err)
		}
	}
	return record, nil
}

func validateInputShape(request InputCreateRequest) error {
	if !knownInputKinds[string(request.Kind)] || !knownInputTemplates[string(request.PromptTemplate)] {
		return domainError(ErrInvalid, CodeInvalidRequest)
	}
	if request.Kind == "approval" {
		if request.PromptTemplate != "approval_required" || len(request.OptionCodes) != 0 {
			return domainError(ErrInvalid, CodeInvalidChoice)
		}
		return nil
	}
	if request.Kind != "choice" || request.PromptTemplate != "choice_required" ||
		len(request.OptionCodes) < 1 || len(request.OptionCodes) > 8 {
		return domainError(ErrInvalid, CodeInvalidChoice)
	}
	for index, code := range request.OptionCodes {
		if !knownInputOptionCodes[code] || code != fmt.Sprintf("choice_%d", index+1) {
			return domainError(ErrInvalid, CodeInvalidChoice)
		}
	}
	return nil
}

func (s *Service) CreateInputRequest(ctx context.Context, principal auth.Principal, request InputCreateRequest) (InputRequestProjection, error) {
	if err := validateInputShape(request); err != nil {
		return InputRequestProjection{}, err
	}
	if !validUUID(request.LeaseID) || request.LeaseRevision <= 0 || (request.RequestID != "" && !validUUID(request.RequestID)) {
		return InputRequestProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	keyDigest, err := operationKeyDigest(request.OperationKeyDigest)
	if err != nil {
		return InputRequestProjection{}, err
	}
	fields := []digestField{stringField("lease_id", request.LeaseID), intField("lease_revision", request.LeaseRevision),
		stringField("request_id", request.RequestID), stringField("kind", string(request.Kind)),
		stringField("prompt_template", string(request.PromptTemplate))}
	for index, code := range request.OptionCodes {
		fields = append(fields, stringField(fmt.Sprintf("option.%02d", index), code))
	}
	requestDigest := canonicalDigest("input.create", fields...)
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeRunner)
	if err != nil {
		return InputRequestProjection{}, err
	}
	defer authz.tx.Rollback()
	lease, err := loadCurrentLeaseRecordTx(ctx, authz.tx, authz.principal, request.LeaseID, request.LeaseRevision, "", "input.respond")
	if err != nil {
		return InputRequestProjection{}, err
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, lease.projectID); err != nil {
		return InputRequestProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "input.create", keyDigest, requestDigest)
	if err != nil {
		return InputRequestProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadInputProjectionTx(ctx, authz.tx, replay.inputRequestID.String, 0)
		if loadErr != nil {
			return InputRequestProjection{}, loadErr
		}
		if err := s.finishRead(authz); err != nil {
			return InputRequestProjection{}, err
		}
		return projection, nil
	}

	requestID := request.RequestID
	revision := int64(1)
	if requestID == "" {
		requestID, err = safeID(s.ids)
		if err != nil {
			return InputRequestProjection{}, err
		}
	} else {
		var currentRevision, stateRevision int64
		var terminal sql.NullInt64
		var kind, template string
		err := authz.tx.QueryRowContext(ctx, `SELECT state.current_revision,state.state_revision,state.terminal_event_id,
			request.request_kind,request.prompt_template FROM control_input_request_states state
			JOIN control_input_requests request ON request.request_id=state.request_id
			 AND request.revision=state.current_revision WHERE state.request_id=?`, requestID).
			Scan(&currentRevision, &stateRevision, &terminal, &kind, &template)
		if errors.Is(err, sql.ErrNoRows) {
			return InputRequestProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
		}
		if err != nil {
			return InputRequestProjection{}, storageError(ctx, err)
		}
		if terminal.Valid || kind != string(request.Kind) || template != string(request.PromptTemplate) {
			return InputRequestProjection{}, domainError(ErrConflict, CodeStaleTarget)
		}
		eventDigest := canonicalDigest("input.superseded", stringField("request_id", requestID), intField("revision", currentRevision))
		result, err := authz.tx.ExecContext(ctx, `INSERT INTO control_input_resolution_events(
			request_id,request_revision,sequence,event_kind,event_digest,safe_reason)
			VALUES(?,?,?,'superseded',?,'input_superseded')`, requestID, currentRevision, currentRevision, eventDigest[:])
		if err != nil {
			return InputRequestProjection{}, sqliteConflict(err)
		}
		terminalID, err := result.LastInsertId()
		if err != nil {
			return InputRequestProjection{}, storageError(ctx, err)
		}
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_input_request_states SET state_revision=?,terminal_event_id=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, stateRevision+1, terminalID, requestID); err != nil {
			return InputRequestProjection{}, sqliteConflict(err)
		}
		if err := insertInputEvent(ctx, authz.tx, requestID, currentRevision, "input_superseded"); err != nil {
			return InputRequestProjection{}, err
		}
		revision = currentRevision + 1
	}

	if _, err := authz.tx.ExecContext(ctx, `INSERT INTO control_input_requests(
		request_id,revision,lease_id,lease_revision,delivery_id,delivery_key,delivery_revision,
		project_id,root_issue_id,issue_revision,attempt_id,attempt_number,plan_revision,stage_key,
		execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,
		reporter_id,agent_run_id,request_kind,prompt_template,option_count,request_digest,expires_at)
		SELECT ?,?,lease_id,revision,delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,
		 issue_revision,attempt_id,attempt_number,plan_revision,stage_key,execution_number,
		 execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,agent_run_id,
		 ?,?,?,?,CASE WHEN lease.expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now','+60 seconds')
		 THEN lease.expires_at ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now','+60 seconds') END
		FROM control_capability_leases lease WHERE lease_id=? AND revision=?`, requestID, revision,
		request.Kind, request.PromptTemplate, len(request.OptionCodes), requestDigest[:], request.LeaseID, request.LeaseRevision); err != nil {
		return InputRequestProjection{}, sqliteConflict(err)
	}
	for index, code := range request.OptionCodes {
		if _, err := authz.tx.ExecContext(ctx, `INSERT INTO control_input_request_options(
			request_id,request_revision,ordinal,option_code) VALUES(?,?,?,?)`, requestID, revision, index+1, code); err != nil {
			return InputRequestProjection{}, sqliteConflict(err)
		}
	}
	if _, err := authz.tx.ExecContext(ctx, `INSERT INTO control_input_request_seals(request_id,request_revision) VALUES(?,?)`, requestID, revision); err != nil {
		return InputRequestProjection{}, sqliteConflict(err)
	}
	if revision == 1 {
		if _, err := authz.tx.ExecContext(ctx, `INSERT INTO control_input_request_states(request_id,current_revision,state_revision)
			VALUES(?,1,1)`, requestID); err != nil {
			return InputRequestProjection{}, sqliteConflict(err)
		}
	} else {
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_input_request_states SET current_revision=?,
			state_revision=state_revision+1,terminal_event_id=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE request_id=?`, revision, requestID); err != nil {
			return InputRequestProjection{}, sqliteConflict(err)
		}
	}
	if err := insertInputEvent(ctx, authz.tx, requestID, revision, "input_requested"); err != nil {
		return InputRequestProjection{}, err
	}
	projection, err := loadInputProjectionTx(ctx, authz.tx, requestID, revision)
	if err != nil {
		return InputRequestProjection{}, err
	}
	resultDigest := inputProjectionDigest(projection)
	if err := insertOperation(ctx, authz.tx, authz.principal, "input.create", keyDigest, requestDigest, resultDigest,
		"input_request_id", requestID); err != nil {
		return InputRequestProjection{}, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return InputRequestProjection{}, err
	}
	return projection, nil
}

func loadInputProjectionTx(ctx context.Context, tx *sql.Tx, requestID string, revision int64) (InputRequestProjection, error) {
	if !validUUID(requestID) || revision < 0 {
		return InputRequestProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	var projection InputRequestProjection
	var expiry string
	query := `SELECT request_id,revision,request_kind,prompt_template,expires_at,delivery_id,delivery_key,
		delivery_revision,root_issue_id,issue_revision,attempt_id,attempt_number,plan_revision,stage_key,
		execution_number,execution_start_stage_event_id,authority_epoch,authority_stage_event_id,reporter_id,agent_run_id
		FROM control_input_requests WHERE request_id=?`
	args := []any{requestID}
	if revision > 0 {
		query += ` AND revision=?`
		args = append(args, revision)
	}
	query += ` ORDER BY revision DESC LIMIT 1`
	err := tx.QueryRowContext(ctx, query, args...).Scan(&projection.RequestID, &projection.Revision,
		&projection.Kind, &projection.PromptTemplate, &expiry, &projection.Target.DeliveryID,
		&projection.Target.DeliveryKey, &projection.Target.DeliveryRevision, &projection.Target.RootIssueID,
		&projection.Target.IssueRevision, &projection.Target.AttemptID, &projection.Target.AttemptNumber,
		&projection.Target.PlanRevision, &projection.Target.StageKey, &projection.Target.ExecutionNumber,
		&projection.Target.ExecutionStartStageEventID, &projection.Target.AuthorityEpoch,
		&projection.Target.AuthorityStageEventID, &projection.Target.ReporterID, &projection.Target.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return InputRequestProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return InputRequestProjection{}, storageError(ctx, err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT option_code FROM control_input_request_options
		WHERE request_id=? AND request_revision=? ORDER BY ordinal`, projection.RequestID, projection.Revision)
	if err != nil {
		return InputRequestProjection{}, storageError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return InputRequestProjection{}, storageError(ctx, err)
		}
		projection.OptionCodes = append(projection.OptionCodes, code)
	}
	projection.ExpiresAt, err = parseControlTime(expiry)
	return projection, err
}

func inputProjectionDigest(projection InputRequestProjection) [32]byte {
	fields := []digestField{stringField("request_id", projection.RequestID), intField("revision", projection.Revision),
		stringField("kind", string(projection.Kind)), stringField("prompt_template", string(projection.PromptTemplate)),
		stringField("expires_at", projection.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"))}
	for index, code := range projection.OptionCodes {
		fields = append(fields, stringField(fmt.Sprintf("option.%02d", index), code))
	}
	return canonicalDigest("input-projection", fields...)
}

func insertInputEvent(ctx context.Context, tx *sql.Tx, requestID string, revision int64, eventKind string) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM control_events
		WHERE input_request_id=?`, requestID).Scan(&sequence); err != nil {
		return storageError(ctx, err)
	}
	requested := eventKind == "input_requested"
	_, err := tx.ExecContext(ctx, `INSERT INTO control_events(
		sequence,event_kind,input_request_id,input_request_revision,executor_user_id,
		executor_principal_kind,executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,
		attempt_id,stage_key,execution_number,authority_epoch,reporter_id,agent_run_id,binding_digest,
		parameter_digest,result_digest,safe_reason)
		SELECT ?,?,request.request_id,request.revision,
		 CASE WHEN ? THEN lease.user_id END,CASE WHEN ? THEN 'api_key' END,
		 CASE WHEN ? THEN lease.actor_api_key_id END,CASE WHEN ? THEN lease.device_id END,
		 request.delivery_id,request.root_issue_id,request.issue_revision,request.attempt_id,request.stage_key,
		 request.execution_number,request.authority_epoch,request.reporter_id,request.agent_run_id,lease.binding_digest,
		 CASE WHEN ? THEN request.request_digest END,CASE WHEN ? THEN NULL ELSE terminal.event_digest END,
		 CASE WHEN ? THEN NULL ELSE terminal.safe_reason END
		FROM control_input_requests request
		JOIN control_capability_leases lease ON lease.lease_id=request.lease_id AND lease.revision=request.lease_revision
		LEFT JOIN control_input_resolution_events terminal ON terminal.request_id=request.request_id
		 AND terminal.request_revision=request.revision
		WHERE request.request_id=? AND request.revision=?`, sequence, eventKind, requested, requested, requested,
		requested, requested, requested, requested, requestID, revision)
	if err != nil {
		return sqliteConflict(err)
	}
	return nil
}

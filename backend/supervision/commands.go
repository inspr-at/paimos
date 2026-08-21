// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/inspr-at/paimos/backend/auth"
)

type grantRecord struct {
	grantID, expiresAt       string
	revision, userID         int64
	projectID, rootIssueID   int64
	issueRevision            int64
	deliveryID               int64
	deliveryRevision         int64
	deliveryKey              string
	projectKey               string
	issueNumber              int64
	issuePriority            string
	issueETag, bindingDigest []byte
	actionDigest             []byte
}

func loadCurrentGrantRecordTx(ctx context.Context, tx *sql.Tx, principal auth.Principal, id string, revision int64, action Action) (grantRecord, error) {
	if !validUUID(id) || revision <= 0 || !knownActions[action] {
		return grantRecord{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	kind, session, apiKey, ok := credentialColumns(principal)
	if !ok {
		return grantRecord{}, domainError(ErrForbidden, CodeCredentialRevoked)
	}
	var record grantRecord
	err := tx.QueryRowContext(ctx, `SELECT grant.grant_id,grant.revision,grant.user_id,grant.expires_at,
		grant.project_id,grant.root_issue_id,grant.issue_revision,grant.delivery_id,grant.delivery_revision,
		grant.delivery_key,project.key,issue.issue_number,issue.priority,grant.issue_etag_digest,
		grant.binding_digest,grant.action_set_digest
		FROM control_capability_grants grant
		JOIN control_capability_grant_seals seal ON seal.grant_id=grant.grant_id
		 AND seal.grant_revision=grant.revision AND seal.binding_digest=grant.binding_digest
		 AND seal.action_set_digest=grant.action_set_digest
		JOIN control_capability_grant_actions granted ON granted.grant_id=grant.grant_id
		 AND granted.grant_revision=grant.revision AND granted.action=?
		JOIN deliveries delivery ON delivery.id=grant.delivery_id AND delivery.delivery_key=grant.delivery_key
		 AND delivery.issue_id=grant.root_issue_id
		JOIN issues issue ON issue.id=grant.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=grant.project_id AND project.id=issue.project_id
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		WHERE grant.grant_id=? AND grant.revision=? AND grant.user_id=? AND grant.principal_kind=?
		 AND grant.actor_session_credential_id IS ? AND grant.actor_api_key_id IS ?
		 AND grant.revoked_at IS NULL AND grant.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 AND control_revision.revision=grant.issue_revision
		 AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
		 WHERE event.delivery_id=delivery.id),0)=grant.delivery_revision
		 AND (project.status IN ('active','frozen') OR
		 (project.status='archived' AND ? IN ('run.cancel.queued','run.cancel.running')))`, action, id, revision,
		principal.UserID(), kind, session, apiKey, action).Scan(&record.grantID, &record.revision, &record.userID,
		&record.expiresAt, &record.projectID, &record.rootIssueID, &record.issueRevision,
		&record.deliveryID, &record.deliveryRevision, &record.deliveryKey, &record.projectKey,
		&record.issueNumber, &record.issuePriority, &record.issueETag, &record.bindingDigest, &record.actionDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return grantRecord{}, domainError(ErrConflict, CodeCapabilityUnavailable)
	}
	if err != nil {
		return grantRecord{}, storageError(ctx, err)
	}
	return record, nil
}

func validateCommandShape(request CommandCreateRequest) error {
	if !knownActions[request.Action] {
		return domainError(ErrInvalid, CodeInvalidAction)
	}
	switch request.Action {
	case "issue.priority.set":
		if request.Priority != "low" && request.Priority != "medium" && request.Priority != "high" {
			return domainError(ErrInvalid, CodeInvalidRequest)
		}
	case "input.respond":
		if !validUUID(request.InputRequestID) || request.InputRequestRevision <= 0 ||
			(request.InputResponse != "approve" && request.InputResponse != "reject" && request.InputResponse != "choice") {
			return domainError(ErrInvalid, CodeInvalidChoice)
		}
		if request.InputResponse == "choice" {
			if request.ChoiceOrdinal < 1 || request.ChoiceOrdinal > 8 {
				return domainError(ErrInvalid, CodeInvalidChoice)
			}
		} else if request.ChoiceOrdinal != 0 {
			return domainError(ErrInvalid, CodeInvalidChoice)
		}
	case "run.cancel.queued", "run.cancel.running":
		if request.RunID <= 0 {
			return domainError(ErrInvalid, CodeInvalidRequest)
		}
	case "run.pause", "run.resume":
		if request.RunID <= 0 || request.RuntimeRevision <= 0 {
			return domainError(ErrInvalid, CodeInvalidRequest)
		}
	}
	return nil
}

func (s *Service) CreateCommand(ctx context.Context, principal auth.Principal, request CommandCreateRequest) (CommandProjection, error) {
	if err := validateCommandShape(request); err != nil {
		return CommandProjection{}, err
	}
	keyDigest, err := operationKeyDigest(request.OperationKeyDigest)
	if err != nil {
		return CommandProjection{}, err
	}
	requestDigest := canonicalDigest("command.create", stringField("grant_id", request.GrantID),
		intField("grant_revision", request.GrantRevision), stringField("action", string(request.Action)),
		stringField("priority", request.Priority), intField("run_id", request.RunID),
		stringField("input_request_id", request.InputRequestID), intField("input_revision", request.InputRequestRevision),
		stringField("input_response", string(request.InputResponse)), intValueField("choice_ordinal", request.ChoiceOrdinal),
		intField("runtime_revision", request.RuntimeRevision))
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeActorWrite)
	if err != nil {
		return CommandProjection{}, err
	}
	defer authz.tx.Rollback()
	grant, err := loadCurrentGrantRecordTx(ctx, authz.tx, authz.principal, request.GrantID, request.GrantRevision, request.Action)
	if err != nil {
		return CommandProjection{}, err
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, grant.projectID); err != nil {
		return CommandProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "command.create", keyDigest, requestDigest)
	if err != nil {
		return CommandProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadCommandProjectionTx(ctx, authz.tx, replay.commandID.String)
		if loadErr != nil {
			return CommandProjection{}, loadErr
		}
		if err := s.finishRead(authz); err != nil {
			return CommandProjection{}, err
		}
		return projection, nil
	}

	commandID, err := safeID(s.ids)
	if err != nil {
		return CommandProjection{}, err
	}
	parameterDigest := commandParameterDigest(request)
	challengeDeadline := s.controlDeadline(s.challengeTTL)
	var targetDigest [32]byte
	var canonical []byte
	switch request.Action {
	case "issue.priority.set":
		targetDigest = canonicalDigest("command-target.issue", intField("issue_id", grant.rootIssueID),
			intField("issue_revision", grant.issueRevision), stringField("issue_etag", fmt.Sprintf("%x", grant.issueETag)))
		canonical = canonicalCommandDigest(grant, request.Action, targetDigest, parameterDigest)
		err = insertPriorityCommandTx(ctx, authz.tx, commandID, grant, request.Priority, parameterDigest, targetDigest, challengeDeadline)
	case "run.cancel.queued":
		var target RunnerTarget
		target, err = loadRunTargetTx(ctx, authz.tx, grant.deliveryID, request.RunID, "queued")
		if err == nil {
			targetDigest = targetSnapshotDigest(target)
			canonical = canonicalCommandDigest(grant, request.Action, targetDigest, parameterDigest)
			err = insertQueuedCommandTx(ctx, authz.tx, commandID, grant, target, parameterDigest, targetDigest, challengeDeadline)
		}
	default:
		var lease leaseRecord
		lease, err = loadLeaseForCommandTx(ctx, authz.tx, grant, request)
		if err == nil {
			targetDigest = targetSnapshotDigest(lease.target)
			canonical = canonicalCommandDigest(grant, request.Action, targetDigest, parameterDigest)
			err = insertLeaseCommandTx(ctx, authz.tx, commandID, grant, lease, request, parameterDigest, targetDigest, challengeDeadline)
		}
	}
	if err != nil {
		var existingID string
		lookupErr := authz.tx.QueryRowContext(ctx, `SELECT command_id FROM control_commands
			WHERE canonical_digest=? AND user_id=?`, canonical, authz.principal.UserID()).Scan(&existingID)
		if lookupErr != nil {
			return CommandProjection{}, err
		}
		projection, loadErr := loadCommandProjectionTx(ctx, authz.tx, existingID)
		if loadErr != nil {
			return CommandProjection{}, loadErr
		}
		if insertErr := insertOperation(ctx, authz.tx, authz.principal, "command.create", keyDigest, requestDigest,
			commandProjectionDigest(projection), "command_id", existingID); insertErr != nil {
			return CommandProjection{}, insertErr
		}
		if commitErr := commitWithWake(ctx, authz, nil, nil); commitErr != nil {
			return CommandProjection{}, commitErr
		}
		return projection, nil
	}
	if err := insertCommandEvent(ctx, authz.tx, commandID, "command_created"); err != nil {
		return CommandProjection{}, err
	}
	projection, err := loadCommandProjectionTx(ctx, authz.tx, commandID)
	if err != nil {
		return CommandProjection{}, err
	}
	if err := insertOperation(ctx, authz.tx, authz.principal, "command.create", keyDigest, requestDigest,
		commandProjectionDigest(projection), "command_id", commandID); err != nil {
		return CommandProjection{}, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return CommandProjection{}, err
	}
	return projection, nil
}

func commandParameterDigest(request CommandCreateRequest) [32]byte {
	return canonicalDigest("command-parameter", stringField("action", string(request.Action)),
		stringField("priority", request.Priority), stringField("input_response", string(request.InputResponse)),
		intValueField("choice_ordinal", request.ChoiceOrdinal), intField("runtime_revision", request.RuntimeRevision))
}

func targetSnapshotDigest(target RunnerTarget) [32]byte {
	return canonicalDigest("command-target.run", intField("delivery_id", target.DeliveryID),
		intField("delivery_revision", target.DeliveryRevision), intField("issue_id", target.RootIssueID),
		intField("issue_revision", target.IssueRevision), intField("attempt_id", target.AttemptID),
		stringField("stage_key", target.StageKey), intField("execution_number", target.ExecutionNumber),
		intField("execution_start", target.ExecutionStartStageEventID), intField("authority_epoch", target.AuthorityEpoch),
		intField("authority_event", target.AuthorityStageEventID), intField("reporter_id", target.ReporterID),
		intField("run_id", target.RunID))
}

func loadRunTargetTx(ctx context.Context, tx *sql.Tx, deliveryID, runID int64, status string) (RunnerTarget, error) {
	var target RunnerTarget
	err := tx.QueryRowContext(ctx, `SELECT delivery.id,delivery.delivery_key,
		COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event WHERE event.delivery_id=delivery.id),0),
		issue.id,control_revision.revision,attempt.id,attempt.attempt_number,attempt.plan_revision,
		link.stage_key,link.execution_number,link.execution_start_stage_event_id,latest.authority_epoch,
		latest.authority_stage_event_id,link.reporter_id,run.id
		FROM agent_runs run JOIN delivery_agent_run_links link ON link.agent_run_id=run.id
		JOIN deliveries delivery ON delivery.id=link.delivery_id AND delivery.issue_id=run.issue_id
		JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
		JOIN issue_control_revisions control_revision ON control_revision.issue_id=issue.id
		JOIN delivery_attempts attempt ON attempt.id=link.attempt_id AND attempt.delivery_id=delivery.id
		JOIN delivery_stage_latest latest ON latest.delivery_id=delivery.id AND latest.attempt_id=link.attempt_id
		 AND latest.stage_key=link.stage_key AND latest.execution_number=link.execution_number
		 AND latest.execution_start_stage_event_id=link.execution_start_stage_event_id
		 AND latest.current_reporter_id=link.reporter_id
		WHERE delivery.id=? AND run.id=? AND run.status=? AND run.delivery_instrumentation_version=1`, deliveryID, runID, status).
		Scan(&target.DeliveryID, &target.DeliveryKey, &target.DeliveryRevision, &target.RootIssueID,
			&target.IssueRevision, &target.AttemptID, &target.AttemptNumber, &target.PlanRevision,
			&target.StageKey, &target.ExecutionNumber, &target.ExecutionStartStageEventID,
			&target.AuthorityEpoch, &target.AuthorityStageEventID, &target.ReporterID, &target.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerTarget{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if err != nil {
		return RunnerTarget{}, storageError(ctx, err)
	}
	return target, nil
}

func loadLeaseForCommandTx(ctx context.Context, tx *sql.Tx, grant grantRecord, request CommandCreateRequest) (leaseRecord, error) {
	var id string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT lease.lease_id,lease.revision FROM control_capability_leases lease
		JOIN control_capability_lease_actions action ON action.lease_id=lease.lease_id
		 AND action.lease_revision=lease.revision AND action.action=?
		WHERE lease.delivery_id=? AND lease.agent_run_id=? AND lease.revoked_at IS NULL
		 AND lease.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now') ORDER BY lease.revision DESC LIMIT 1`,
		request.Action, grant.deliveryID, request.RunID).Scan(&id, &revision)
	if request.Action == "input.respond" {
		err = tx.QueryRowContext(ctx, `SELECT lease_id,lease_revision FROM control_input_requests
			WHERE request_id=? AND revision=?`, request.InputRequestID,
			request.InputRequestRevision).Scan(&id, &revision)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return leaseRecord{}, domainError(ErrConflict, CodeCapabilityUnavailable)
	}
	if err != nil {
		return leaseRecord{}, storageError(ctx, err)
	}
	var executorUserID, executorAPIKeyID int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id,actor_api_key_id FROM control_capability_leases
		WHERE lease_id=? AND revision=?`, id, revision).Scan(&executorUserID, &executorAPIKeyID); err != nil {
		return leaseRecord{}, storageError(ctx, err)
	}
	executor, principalErr := auth.NewAPIKeyPrincipal(executorAPIKeyID, executorUserID, auth.ScopeSet{ScopeRunner: {}})
	if principalErr != nil {
		return leaseRecord{}, domainError(ErrInvariant, CodeInvariant)
	}
	lease, err := loadCurrentLeaseRecordTx(ctx, tx, executor, id, revision, "", request.Action)
	if err != nil {
		return leaseRecord{}, err
	}
	if lease.target.DeliveryID != grant.deliveryID || lease.target.DeliveryRevision != grant.deliveryRevision ||
		lease.target.RootIssueID != grant.rootIssueID || lease.target.IssueRevision != grant.issueRevision {
		return leaseRecord{}, domainError(ErrConflict, CodeStaleTarget)
	}
	return lease, nil
}

func insertPriorityCommandTx(ctx context.Context, tx *sql.Tx, id string, grant grantRecord, priority string,
	parameter, target [32]byte, challengeDeadline string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO control_commands(command_id,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,canonical_digest,grant_id,grant_revision,grant_expires_at,
		grant_binding_digest,grant_action_digest,action,status,challenge_template,delivery_id,delivery_key,
		delivery_revision,project_id,root_issue_id,issue_revision,issue_etag_digest,target_snapshot_digest,
		priority_value,parameter_digest,expires_at)
		SELECT ?,actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,?,grant_id,
		revision,expires_at,binding_digest,action_set_digest,'issue.priority.set','pending_confirmation',
		'issue_priority_set',delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,?,?,?,CASE WHEN expires_at<? THEN expires_at ELSE ? END
		FROM control_capability_grants WHERE grant_id=? AND revision=?`, id,
		canonicalCommandDigest(grant, "issue.priority.set", target, parameter), target[:], priority, parameter[:],
		challengeDeadline, challengeDeadline, grant.grantID, grant.revision)
	return sqliteConflict(err)
}

func insertQueuedCommandTx(ctx context.Context, tx *sql.Tx, id string, grant grantRecord, target RunnerTarget,
	parameter, targetDigest [32]byte, challengeDeadline string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO control_commands(command_id,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,canonical_digest,grant_id,grant_revision,grant_expires_at,
		grant_binding_digest,grant_action_digest,action,status,challenge_template,delivery_id,delivery_key,
		delivery_revision,project_id,root_issue_id,issue_revision,issue_etag_digest,target_snapshot_digest,
		attempt_id,attempt_number,plan_revision,stage_key,execution_number,execution_start_stage_event_id,
		authority_epoch,authority_stage_event_id,reporter_id,agent_run_id,parameter_digest,expires_at)
		SELECT ?,actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,?,grant_id,
		revision,expires_at,binding_digest,action_set_digest,'run.cancel.queued','pending_confirmation',
		'run_cancel_queued',delivery_id,delivery_key,delivery_revision,project_id,root_issue_id,issue_revision,
		issue_etag_digest,?,?,?,?,?,?,?,?,?,?,?,?,CASE WHEN expires_at<? THEN expires_at ELSE ? END
		FROM control_capability_grants WHERE grant_id=? AND revision=?`, id,
		canonicalCommandDigest(grant, "run.cancel.queued", targetDigest, parameter), targetDigest[:],
		target.AttemptID, target.AttemptNumber, target.PlanRevision, target.StageKey, target.ExecutionNumber,
		target.ExecutionStartStageEventID, target.AuthorityEpoch, target.AuthorityStageEventID, target.ReporterID,
		target.RunID, parameter[:], challengeDeadline, challengeDeadline, grant.grantID, grant.revision)
	return sqliteConflict(err)
}

func insertLeaseCommandTx(ctx context.Context, tx *sql.Tx, id string, grant grantRecord, lease leaseRecord,
	request CommandCreateRequest, parameter, target [32]byte, challengeDeadline string) error {
	template := map[Action]string{"run.cancel.running": "run_cancel_running", "input.respond": "input_" + string(request.InputResponse),
		"run.pause": "run_pause", "run.resume": "run_resume"}[request.Action]
	var inputID, inputExpiry any
	var inputRevision, runtimeRevision, choiceOrdinal any
	var inputResponse, choiceCode any
	if request.Action == "input.respond" {
		inputID, inputRevision, inputResponse = request.InputRequestID, request.InputRequestRevision, request.InputResponse
		if request.InputResponse == "choice" {
			choiceOrdinal, choiceCode = request.ChoiceOrdinal, "choice_"+strconv.Itoa(request.ChoiceOrdinal)
		}
		if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM control_input_requests request
			JOIN control_input_request_seals seal ON seal.request_id=request.request_id AND seal.request_revision=request.revision
			JOIN control_input_request_states state ON state.request_id=request.request_id
			 AND state.current_revision=request.revision AND state.terminal_event_id IS NULL
			WHERE request.request_id=? AND request.revision=? AND request.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			request.InputRequestID, request.InputRequestRevision).Scan(&inputExpiry); err != nil {
			return domainError(ErrConflict, CodeStaleTarget)
		}
		if request.InputResponse == "choice" {
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM control_input_request_options WHERE request_id=?
				AND request_revision=? AND ordinal=? AND option_code=?`, request.InputRequestID,
				request.InputRequestRevision, request.ChoiceOrdinal, choiceCode).Scan(&exists); err != nil {
				return domainError(ErrInvalid, CodeInvalidChoice)
			}
		}
	}
	if request.Action == "run.pause" || request.Action == "run.resume" {
		runtimeRevision = request.RuntimeRevision
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM control_runtime_states WHERE agent_run_id=? AND revision=?`,
			lease.target.RunID, request.RuntimeRevision).Scan(&state); err != nil ||
			(request.Action == "run.pause" && state != "running") || (request.Action == "run.resume" && state != "paused") {
			return domainError(ErrConflict, CodeStaleTarget)
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_commands(command_id,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,canonical_digest,grant_id,grant_revision,grant_expires_at,
		grant_binding_digest,grant_action_digest,action,status,challenge_template,delivery_id,delivery_key,
		delivery_revision,project_id,root_issue_id,issue_revision,issue_etag_digest,target_snapshot_digest,
		attempt_id,attempt_number,plan_revision,stage_key,execution_number,execution_start_stage_event_id,
		authority_epoch,authority_stage_event_id,reporter_id,agent_run_id,lease_id,lease_revision,lease_expires_at,
		lease_binding_digest,lease_action_digest,input_request_id,input_request_revision,input_request_expires_at,
		runtime_revision,input_response_kind,input_choice_ordinal,input_choice_code,parameter_digest,expires_at)
		SELECT ?,actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,?,grant_id,
		revision,expires_at,binding_digest,action_set_digest,?,'pending_confirmation',?,delivery_id,delivery_key,
		delivery_revision,project_id,root_issue_id,issue_revision,issue_etag_digest,?,
		?,?,?,?,?,?,?,?,?,?,
		?,?,?,?,?,
		?,?,?,
		?,
		?,?,?,
		?,MIN(expires_at,?,COALESCE(?,?),?)
		FROM control_capability_grants WHERE grant_id=? AND revision=?`, id,
		canonicalCommandDigest(grant, request.Action, target, parameter), request.Action, template, target[:],
		lease.target.AttemptID, lease.target.AttemptNumber, lease.target.PlanRevision, lease.target.StageKey,
		lease.target.ExecutionNumber, lease.target.ExecutionStartStageEventID, lease.target.AuthorityEpoch,
		lease.target.AuthorityStageEventID, lease.target.ReporterID, lease.target.RunID, lease.leaseID, lease.revision,
		lease.expiresAt, lease.bindingDigest, lease.actionSetDigest, inputID, inputRevision, inputExpiry, runtimeRevision,
		inputResponse, choiceOrdinal, choiceCode, parameter[:], lease.expiresAt, inputExpiry, lease.expiresAt, challengeDeadline,
		grant.grantID, grant.revision)
	return sqliteConflict(err)
}

func canonicalCommandDigest(grant grantRecord, action Action, target, parameter [32]byte) []byte {
	digest := canonicalDigest("command", intField("user_id", grant.userID), stringField("action", string(action)),
		stringField("grant_id", grant.grantID), intField("grant_revision", grant.revision),
		stringField("target", digestHex(target)), stringField("parameter", digestHex(parameter)))
	return digest[:]
}

func loadCommandProjectionTx(ctx context.Context, tx *sql.Tx, commandID string) (CommandProjection, error) {
	if !validUUID(commandID) {
		return CommandProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	var projection CommandProjection
	var outcome, reason sql.NullString
	var expiry string
	var priority, inputKind, choiceCode, runtimeState sql.NullString
	var runID, choiceOrdinal, runtimeRevision sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT command.command_id,command.status_revision,command.action,command.status,
		command.outcome,command.safe_reason,command.challenge_template,command.expires_at,command.priority_value,
		command.agent_run_id,request.request_kind,command.input_choice_ordinal,command.input_choice_code,
		CASE command.action WHEN 'run.pause' THEN 'running' WHEN 'run.resume' THEN 'paused' END,
		command.runtime_revision,project.key||'-'||issue.issue_number,command.delivery_key
		FROM control_commands command LEFT JOIN control_input_requests request
		 ON request.request_id=command.input_request_id AND request.revision=command.input_request_revision
		JOIN issues issue ON issue.id=command.root_issue_id AND issue.deleted_at IS NULL
		 AND issue.project_id=command.project_id
		JOIN projects project ON project.id=command.project_id AND project.status<>'deleted'
		WHERE command.command_id=?`, commandID).Scan(&projection.CommandID, &projection.StatusRevision,
		&projection.Action, &projection.Status, &outcome, &reason, &projection.ChallengeTemplate, &expiry,
		&priority, &runID, &inputKind, &choiceOrdinal, &choiceCode, &runtimeState, &runtimeRevision,
		&projection.Display.IssueKey, &projection.Display.DeliveryKey)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return CommandProjection{}, storageError(ctx, err)
	}
	projection.Outcome, projection.Reason = Outcome(outcome.String), SafeReason(reason.String)
	projection.Display.Priority, projection.Display.RunID = priority.String, runID.Int64
	projection.Display.InputKind = InputKind(inputKind.String)
	projection.Display.ChoiceOrdinal, projection.Display.ChoiceCode = int(choiceOrdinal.Int64), choiceCode.String
	projection.Display.RuntimeState, projection.Display.RuntimeRevision = RuntimeState(runtimeState.String), runtimeRevision.Int64
	projection.ExpiresAt, err = parseControlTime(expiry)
	return projection, err
}

func commandProjectionDigest(projection CommandProjection) [32]byte {
	return canonicalDigest("command-projection", stringField("command_id", projection.CommandID),
		intField("status_revision", projection.StatusRevision), stringField("action", string(projection.Action)),
		stringField("status", string(projection.Status)), stringField("outcome", string(projection.Outcome)),
		stringField("reason", string(projection.Reason)))
}

func (s *Service) GetCommand(ctx context.Context, principal auth.Principal, request CommandGetRequest) (CommandProjection, error) {
	if !validUUID(request.CommandID) {
		return CommandProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	authz, err := s.beginAuthorized(ctx, principal, true, ScopeActorWrite)
	if err != nil {
		return CommandProjection{}, err
	}
	defer authz.tx.Rollback()
	kind, session, apiKey, ok := credentialColumns(authz.principal)
	if !ok {
		return CommandProjection{}, domainError(ErrForbidden, CodeCredentialRevoked)
	}
	var projectID int64
	if err := authz.tx.QueryRowContext(ctx, `SELECT command.project_id FROM control_commands command
		JOIN issues issue ON issue.id=command.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=command.project_id AND project.id=issue.project_id
		 AND project.status<>'deleted' WHERE command.command_id=? AND command.user_id=? AND command.principal_kind=?
		 AND command.actor_session_credential_id IS ? AND command.actor_api_key_id IS ?`, request.CommandID,
		authz.principal.UserID(), kind, session, apiKey).Scan(&projectID); err != nil {
		return CommandProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, projectID); err != nil {
		return CommandProjection{}, err
	}
	projection, err := loadCommandProjectionTx(ctx, authz.tx, request.CommandID)
	if err != nil {
		return CommandProjection{}, err
	}
	if err := s.finishRead(authz); err != nil {
		return CommandProjection{}, err
	}
	return projection, nil
}

func (s *Service) ConfirmCommand(ctx context.Context, principal auth.Principal, request CommandConfirmRequest) (CommandProjection, error) {
	return s.transitionCommand(ctx, principal, request.CommandID, request.StatusRevision, request.OperationKeyDigest, true)
}

func (s *Service) WithdrawCommand(ctx context.Context, principal auth.Principal, request CommandWithdrawRequest) (CommandProjection, error) {
	return s.transitionCommand(ctx, principal, request.CommandID, request.StatusRevision, request.OperationKeyDigest, false)
}

func (s *Service) transitionCommand(ctx context.Context, principal auth.Principal, commandID string, revision int64,
	operationKey [32]byte, confirm bool) (CommandProjection, error) {
	if !validUUID(commandID) || revision <= 0 {
		return CommandProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	keyDigest, err := operationKeyDigest(operationKey)
	if err != nil {
		return CommandProjection{}, err
	}
	operation := "command.withdraw"
	if confirm {
		operation = "command.confirm"
	}
	requestDigest := canonicalDigest(operation, stringField("command_id", commandID), intField("status_revision", revision))
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeActorWrite)
	if err != nil {
		return CommandProjection{}, err
	}
	defer authz.tx.Rollback()
	kind, session, apiKey, ok := credentialColumns(authz.principal)
	if !ok {
		return CommandProjection{}, domainError(ErrForbidden, CodeCredentialRevoked)
	}
	var projectID, issueID, issueRevision, runID int64
	var action Action
	var status string
	var issueETag []byte
	if err := authz.tx.QueryRowContext(ctx, `SELECT project_id,root_issue_id,issue_revision,COALESCE(agent_run_id,0),
		action,status,issue_etag_digest FROM control_commands WHERE command_id=? AND user_id=? AND principal_kind=?
		 AND actor_session_credential_id IS ? AND actor_api_key_id IS ?`, commandID, authz.principal.UserID(),
		kind, session, apiKey).Scan(&projectID, &issueID, &issueRevision, &runID, &action, &status, &issueETag); err != nil {
		return CommandProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, projectID); err != nil {
		return CommandProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, operation, keyDigest, requestDigest)
	if err != nil {
		return CommandProjection{}, err
	}
	if replay.found {
		projection, loadErr := loadCommandProjectionTx(ctx, authz.tx, replay.commandID.String)
		if loadErr != nil {
			return CommandProjection{}, loadErr
		}
		if err := s.finishRead(authz); err != nil {
			return CommandProjection{}, err
		}
		return projection, nil
	}
	if revision != 1 {
		return CommandProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if (confirm && (status == "accepted" || status == "applied" || status == "rejected")) || (!confirm && status == "expired") {
		projection, loadErr := loadCommandProjectionTx(ctx, authz.tx, commandID)
		if loadErr != nil {
			return CommandProjection{}, loadErr
		}
		if !confirm && projection.Reason != "withdrawn" {
			return CommandProjection{}, domainError(ErrConflict, CodeStaleTarget)
		}
		if err := insertOperation(ctx, authz.tx, authz.principal, operation, keyDigest, requestDigest,
			commandProjectionDigest(projection), "command_id", commandID); err != nil {
			return CommandProjection{}, err
		}
		if err := commitWithWake(ctx, authz, nil, nil); err != nil {
			return CommandProjection{}, err
		}
		return projection, nil
	}
	if status != "pending_confirmation" {
		return CommandProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if confirm {
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status='accepted',status_revision=2,
			accepted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=?`, commandID); err != nil {
			return CommandProjection{}, sqliteConflict(err)
		}
		if err := insertCommandEvent(ctx, authz.tx, commandID, "command_accepted"); err != nil {
			return CommandProjection{}, err
		}
	} else {
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status='expired',status_revision=2,
			safe_reason='withdrawn',terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, commandID); err != nil {
			return CommandProjection{}, sqliteConflict(err)
		}
		if err := insertCommandEvent(ctx, authz.tx, commandID, "command_withdrawn"); err != nil {
			return CommandProjection{}, err
		}
	}
	var wake *CommitWake
	if confirm && (action == "issue.priority.set" || action == "run.cancel.queued") {
		if s.mutator == nil || s.changes == nil {
			return CommandProjection{}, domainError(ErrUnavailable, CodeDependencyUnavailable)
		}
		resultDigest := canonicalDigest("synchronous-result", stringField("command_id", commandID), stringField("outcome", "applied"))
		if action == "issue.priority.set" {
			var priority string
			if err := authz.tx.QueryRowContext(ctx, `SELECT priority_value FROM control_commands WHERE command_id=?`, commandID).Scan(&priority); err != nil {
				return CommandProjection{}, storageError(ctx, err)
			}
			var etag [32]byte
			copy(etag[:], issueETag)
			if err := mutationError(ctx, s.mutator.SetIssuePriorityTx(ctx, authz.tx, PriorityMutation{IssueID: issueID,
				ExpectedRevision: issueRevision, ExpectedETagDigest: etag, Priority: priority,
				ActorUserID: authz.principal.UserID(), CommandID: commandID})); err != nil {
				return CommandProjection{}, err
			}
		}
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status='applied',status_revision=3,
			outcome='applied',result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest[:], commandID); err != nil {
			return CommandProjection{}, sqliteConflict(err)
		}
		if action == "run.cancel.queued" {
			if err := mutationError(ctx, s.mutator.CancelQueuedRunTx(ctx, authz.tx, RunCancellationMutation{IssueID: issueID,
				RunID: runID, ExpectedState: "queued", ActorUserID: authz.principal.UserID(), CommandID: commandID})); err != nil {
				return CommandProjection{}, err
			}
			if err := insertCancellationFact(ctx, authz.tx, commandID, runID); err != nil {
				return CommandProjection{}, err
			}
		}
		if err := insertCommandEvent(ctx, authz.tx, commandID, "command_applied"); err != nil {
			return CommandProjection{}, err
		}
		if action == "run.cancel.queued" {
			if err := insertCancellationEvent(ctx, authz.tx, commandID, runID); err != nil {
				return CommandProjection{}, err
			}
		}
		captured, err := s.changes.RecordControlChangeTx(ctx, authz.tx, ControlChange{IssueID: issueID,
			RunID: runID, CommandID: commandID, Action: action})
		if err != nil {
			return CommandProjection{}, mutationError(ctx, err)
		}
		wake = &captured
	} else if confirm {
		var leaseID string
		var leaseRevision int64
		var canonical []byte
		if err := authz.tx.QueryRowContext(ctx, `SELECT lease_id,lease_revision,canonical_digest FROM control_commands
			WHERE command_id=?`, commandID).Scan(&leaseID, &leaseRevision, &canonical); err != nil {
			return CommandProjection{}, storageError(ctx, err)
		}
		if _, err := authz.tx.ExecContext(ctx, `INSERT INTO control_outbox(command_id,lease_id,lease_revision,
			delivery_state,effect_digest) VALUES(?,?,?,'queued',?)`, commandID, leaseID, leaseRevision, canonical); err != nil {
			return CommandProjection{}, sqliteConflict(err)
		}
		if err := insertCommandEvent(ctx, authz.tx, commandID, "effect_queued"); err != nil {
			return CommandProjection{}, err
		}
	}
	projection, err := loadCommandProjectionTx(ctx, authz.tx, commandID)
	if err != nil {
		return CommandProjection{}, err
	}
	if err := insertOperation(ctx, authz.tx, authz.principal, operation, keyDigest, requestDigest,
		commandProjectionDigest(projection), "command_id", commandID); err != nil {
		return CommandProjection{}, err
	}
	if err := commitWithWake(ctx, authz, s.changes, wake); err != nil {
		return CommandProjection{}, err
	}
	return projection, nil
}

func insertCommandEvent(ctx context.Context, tx *sql.Tx, commandID, kind string) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM control_events WHERE command_id=?`, commandID).Scan(&sequence); err != nil {
		return storageError(ctx, err)
	}
	executor := kind == "effect_claimed" || kind == "effect_outcome_unknown" || kind == "effect_acknowledged" ||
		kind == "effect_reconciled" || kind == "runtime_changed"
	runtime := kind == "runtime_changed"
	_, err := tx.ExecContext(ctx, `INSERT INTO control_events(sequence,event_kind,command_id,command_status_revision,
		actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,executor_user_id,
		executor_principal_kind,executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,
		attempt_id,stage_key,execution_number,authority_epoch,reporter_id,agent_run_id,action,command_status,
		runtime_state,runtime_revision,outcome,safe_reason,parameter_digest,binding_digest,action_set_digest,result_digest)
		SELECT ?,?,command.command_id,command.status_revision,command.actor_user_id,command.user_id,
		 command.principal_kind,command.actor_session_credential_id,command.actor_api_key_id,
		 CASE WHEN ? THEN outbox.claim_user_id END,CASE WHEN ? THEN 'api_key' END,
		 CASE WHEN ? THEN outbox.claim_api_key_id END,CASE WHEN ? THEN outbox.claim_device_id END,
		 command.delivery_id,command.root_issue_id,command.issue_revision,command.attempt_id,command.stage_key,
		 command.execution_number,command.authority_epoch,command.reporter_id,command.agent_run_id,command.action,
		 command.status,CASE WHEN ? THEN runtime.state END,CASE WHEN ? THEN runtime.revision END,
		 command.outcome,command.safe_reason,command.parameter_digest,command.target_snapshot_digest,
		 command.grant_action_digest,command.result_digest FROM control_commands command
		LEFT JOIN control_outbox outbox ON outbox.command_id=command.command_id
		LEFT JOIN control_runtime_states runtime ON runtime.agent_run_id=command.agent_run_id
		WHERE command.command_id=?`, sequence, kind, executor, executor, executor, executor, runtime, runtime, commandID)
	if err != nil {
		return sqliteConflict(err)
	}
	return nil
}

func insertCancellationFact(ctx context.Context, tx *sql.Tx, commandID string, runID int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_run_cancellation_facts(
		run_id,cancellation_cause,command_id,recorded_at)
		VALUES(?,'operator_command',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, runID, commandID)
	return sqliteConflict(err)
}

func insertCancellationEvent(ctx context.Context, tx *sql.Tx, commandID string, runID int64) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM control_events WHERE cancellation_run_id=?`, runID).Scan(&sequence); err != nil {
		return storageError(ctx, err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_events(sequence,event_kind,cancellation_run_id,
		cancellation_command_id,cancellation_cause,actor_user_id,user_id,principal_kind,
		actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,attempt_id,
		stage_key,execution_number,authority_epoch,reporter_id,agent_run_id,action,command_status,outcome,
		parameter_digest,binding_digest,action_set_digest,result_digest)
		SELECT ?,'cancellation_recorded',agent_run_id,command_id,'operator_command',actor_user_id,user_id,
		principal_kind,actor_session_credential_id,actor_api_key_id,delivery_id,root_issue_id,issue_revision,
		attempt_id,stage_key,execution_number,authority_epoch,reporter_id,agent_run_id,action,status,outcome,
		parameter_digest,target_snapshot_digest,grant_action_digest,result_digest FROM control_commands WHERE command_id=?`,
		sequence, commandID)
	return sqliteConflict(err)
}

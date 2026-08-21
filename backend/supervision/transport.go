// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/inspr-at/paimos/backend/auth"
)

func (s *Service) Pull(ctx context.Context, principal auth.Principal, request PullRequest) (PullProjection, error) {
	if !validUUID(request.LeaseID) || request.LeaseRevision <= 0 || request.Cursor < 0 {
		return PullProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	authz, err := s.beginAuthorized(ctx, principal, true, ScopeRunner)
	if err != nil {
		return PullProjection{}, err
	}
	defer authz.tx.Rollback()
	lease, err := loadCurrentLeaseRecordTx(ctx, authz.tx, authz.principal, request.LeaseID, request.LeaseRevision, "", "")
	if err != nil {
		return PullProjection{}, err
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, lease.projectID); err != nil {
		return PullProjection{}, err
	}
	var projection PullProjection
	if err := authz.tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM control_outbox`).Scan(&projection.SnapshotHighWater); err != nil {
		return PullProjection{}, storageError(ctx, err)
	}
	rows, err := authz.tx.QueryContext(ctx, `SELECT outbox.id,command.command_id,command.action,outbox.effect_sequence,
		outbox.lease_id,outbox.lease_revision,command.delivery_id,command.delivery_key,command.delivery_revision,
		command.root_issue_id,command.issue_revision,command.attempt_id,command.attempt_number,command.plan_revision,
		command.stage_key,command.execution_number,command.execution_start_stage_event_id,command.authority_epoch,
		command.authority_stage_event_id,command.reporter_id,command.agent_run_id,COALESCE(command.input_request_id,''),
		COALESCE(command.input_request_revision,0),COALESCE(command.input_response_kind,''),
		COALESCE(command.input_choice_ordinal,0),COALESCE(command.input_choice_code,''),COALESCE(command.runtime_revision,0)
		FROM control_outbox outbox JOIN control_commands command ON command.command_id=outbox.command_id
		WHERE outbox.lease_id=? AND outbox.lease_revision=? AND outbox.delivery_state='queued'
		 AND outbox.id>? AND outbox.id<=? ORDER BY outbox.id LIMIT ?`, request.LeaseID,
		request.LeaseRevision, request.Cursor, projection.SnapshotHighWater, PullProbePageSize)
	if err != nil {
		return PullProjection{}, storageError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var effect EffectProjection
		if err := rows.Scan(&effect.OutboxID, &effect.CommandID, &effect.Action, &effect.EffectSequence,
			&effect.LeaseID, &effect.LeaseRevision, &effect.Target.DeliveryID, &effect.Target.DeliveryKey,
			&effect.Target.DeliveryRevision, &effect.Target.RootIssueID, &effect.Target.IssueRevision,
			&effect.Target.AttemptID, &effect.Target.AttemptNumber, &effect.Target.PlanRevision,
			&effect.Target.StageKey, &effect.Target.ExecutionNumber, &effect.Target.ExecutionStartStageEventID,
			&effect.Target.AuthorityEpoch, &effect.Target.AuthorityStageEventID, &effect.Target.ReporterID,
			&effect.Target.RunID, &effect.InputRequestID, &effect.InputRevision, &effect.InputResponse,
			&effect.ChoiceOrdinal, &effect.ChoiceCode, &effect.RuntimeRevision); err != nil {
			return PullProjection{}, storageError(ctx, err)
		}
		projection.Effects = append(projection.Effects, effect)
	}
	if err := rows.Err(); err != nil {
		return PullProjection{}, storageError(ctx, err)
	}
	if len(projection.Effects) > PullPageSize {
		projection.HasMore = true
		projection.Effects = projection.Effects[:PullPageSize]
	}
	projection.NextCursor = request.Cursor
	if len(projection.Effects) > 0 {
		projection.NextCursor = projection.Effects[len(projection.Effects)-1].OutboxID
	}
	if err := s.finishRead(authz); err != nil {
		return PullProjection{}, err
	}
	return projection, nil
}

func (s *Service) Claim(ctx context.Context, principal auth.Principal, request ClaimRequest) (EffectProjection, error) {
	if !validUUID(request.CommandID) || !validUUID(request.LeaseID) || request.LeaseRevision <= 0 ||
		request.EffectSequence != 1 {
		return EffectProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	if err := validateDevice(request.DeviceID); err != nil {
		return EffectProjection{}, err
	}
	keyDigest, err := operationKeyDigest(request.OperationKeyDigest)
	if err != nil {
		return EffectProjection{}, err
	}
	requestDigest := canonicalDigest("command.claim", stringField("command_id", request.CommandID),
		stringField("lease_id", request.LeaseID), intField("lease_revision", request.LeaseRevision),
		intField("effect_sequence", request.EffectSequence), stringField("device_id", request.DeviceID))
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeRunner)
	if err != nil {
		return EffectProjection{}, err
	}
	defer authz.tx.Rollback()
	lease, err := loadCurrentLeaseRecordTx(ctx, authz.tx, authz.principal, request.LeaseID, request.LeaseRevision, request.DeviceID, "")
	if err != nil {
		return EffectProjection{}, err
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, lease.projectID); err != nil {
		return EffectProjection{}, err
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "command.claim", keyDigest, requestDigest)
	if err != nil {
		return EffectProjection{}, err
	}
	if !replay.found {
		result, err := authz.tx.ExecContext(ctx, `UPDATE control_outbox SET delivery_state='claimed',claim_sequence=1,
			claim_user_id=?,claim_principal_kind='api_key',claim_api_key_id=?,claim_device_id=?,
			claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=? AND lease_id=? AND lease_revision=? AND effect_sequence=1 AND delivery_state='queued'`,
			authz.principal.UserID(), authz.principal.APIKeyID(), request.DeviceID, request.CommandID,
			request.LeaseID, request.LeaseRevision)
		if err != nil {
			return EffectProjection{}, sqliteConflict(err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			var claimantUser, claimantKey int64
			var claimantDevice, state string
			if queryErr := authz.tx.QueryRowContext(ctx, `SELECT delivery_state,claim_user_id,claim_api_key_id,
				claim_device_id FROM control_outbox WHERE command_id=? AND lease_id=? AND lease_revision=?`,
				request.CommandID, request.LeaseID, request.LeaseRevision).
				Scan(&state, &claimantUser, &claimantKey, &claimantDevice); queryErr != nil || state != "claimed" ||
				claimantUser != authz.principal.UserID() || claimantKey != authz.principal.APIKeyID() ||
				claimantDevice != request.DeviceID {
				return EffectProjection{}, domainError(ErrConflict, CodeAlreadyClaimed)
			}
		} else {
			if err := insertCommandEvent(ctx, authz.tx, request.CommandID, "effect_claimed"); err != nil {
				return EffectProjection{}, err
			}
		}
	}
	effect, err := loadEffectTx(ctx, authz.tx, request.CommandID)
	if err != nil {
		return EffectProjection{}, err
	}
	if !replay.found {
		if err := insertOperation(ctx, authz.tx, authz.principal, "command.claim", keyDigest, requestDigest,
			effectProjectionDigest(effect), "command_id", request.CommandID); err != nil {
			return EffectProjection{}, err
		}
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return EffectProjection{}, err
	}
	return effect, nil
}

func loadEffectTx(ctx context.Context, tx *sql.Tx, commandID string) (EffectProjection, error) {
	var effect EffectProjection
	err := tx.QueryRowContext(ctx, `SELECT outbox.id,command.command_id,command.action,outbox.effect_sequence,
		outbox.lease_id,outbox.lease_revision,command.delivery_id,command.delivery_key,command.delivery_revision,
		command.root_issue_id,command.issue_revision,command.attempt_id,command.attempt_number,command.plan_revision,
		command.stage_key,command.execution_number,command.execution_start_stage_event_id,command.authority_epoch,
		command.authority_stage_event_id,command.reporter_id,command.agent_run_id,COALESCE(command.input_request_id,''),
		COALESCE(command.input_request_revision,0),COALESCE(command.input_response_kind,''),
		COALESCE(command.input_choice_ordinal,0),COALESCE(command.input_choice_code,''),COALESCE(command.runtime_revision,0)
		FROM control_outbox outbox JOIN control_commands command ON command.command_id=outbox.command_id
		WHERE command.command_id=?`, commandID).Scan(&effect.OutboxID, &effect.CommandID, &effect.Action,
		&effect.EffectSequence, &effect.LeaseID, &effect.LeaseRevision, &effect.Target.DeliveryID,
		&effect.Target.DeliveryKey, &effect.Target.DeliveryRevision, &effect.Target.RootIssueID,
		&effect.Target.IssueRevision, &effect.Target.AttemptID, &effect.Target.AttemptNumber,
		&effect.Target.PlanRevision, &effect.Target.StageKey, &effect.Target.ExecutionNumber,
		&effect.Target.ExecutionStartStageEventID, &effect.Target.AuthorityEpoch,
		&effect.Target.AuthorityStageEventID, &effect.Target.ReporterID, &effect.Target.RunID,
		&effect.InputRequestID, &effect.InputRevision, &effect.InputResponse, &effect.ChoiceOrdinal,
		&effect.ChoiceCode, &effect.RuntimeRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return EffectProjection{}, domainError(ErrNotFound, CodeTargetNotFound)
	}
	if err != nil {
		return EffectProjection{}, storageError(ctx, err)
	}
	return effect, nil
}

func effectProjectionDigest(effect EffectProjection) [32]byte {
	return canonicalDigest("effect-projection", intField("outbox_id", effect.OutboxID),
		stringField("command_id", effect.CommandID), stringField("action", string(effect.Action)),
		intField("lease_revision", effect.LeaseRevision), intField("effect_sequence", effect.EffectSequence))
}

func validateResult(request ResultRequest) error {
	if !validUUID(request.CommandID) || !validUUID(request.LeaseID) || request.LeaseRevision <= 0 ||
		request.EffectSequence != 1 || request.ClaimSequence != 1 || request.ResultSequence != 1 {
		return domainError(ErrInvalid, CodeInvalidRequest)
	}
	if request.Outcome == "applied" {
		if request.Reason != "" {
			return domainError(ErrInvalid, CodeInvalidRequest)
		}
		return nil
	}
	if request.Outcome != "rejected" || (request.Reason != "effect_rejected" && request.Reason != "unsupported_platform" &&
		request.Reason != "process_termination_failed" && request.Reason != "natural_exit") {
		return domainError(ErrInvalid, CodeInvalidRequest)
	}
	return validateDevice(request.DeviceID)
}

func (s *Service) RecordResult(ctx context.Context, principal auth.Principal, request ResultRequest) (CommandProjection, error) {
	if err := validateResult(request); err != nil {
		return CommandProjection{}, err
	}
	if err := validateDevice(request.DeviceID); err != nil {
		return CommandProjection{}, err
	}
	keyDigest, err := operationKeyDigest(request.OperationKeyDigest)
	if err != nil {
		return CommandProjection{}, err
	}
	requestDigest := canonicalDigest("command.result", stringField("command_id", request.CommandID),
		stringField("lease_id", request.LeaseID), intField("lease_revision", request.LeaseRevision),
		stringField("device_id", request.DeviceID), stringField("outcome", string(request.Outcome)),
		stringField("reason", string(request.Reason)))
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeRunner)
	if err != nil {
		return CommandProjection{}, err
	}
	defer authz.tx.Rollback()
	resultDigest := canonicalDigest("runner-result", stringField("command_id", request.CommandID),
		stringField("outcome", string(request.Outcome)), stringField("reason", string(request.Reason)),
		intField("result_sequence", request.ResultSequence))
	var action Action
	var issueID, runID, statusRevision, projectID int64
	var commandStatus, outboxState string
	var commandOutcome, commandReason, outboxReason sql.NullString
	var storedCommandDigest, storedOutboxDigest []byte
	err = authz.tx.QueryRowContext(ctx, `SELECT command.action,command.root_issue_id,command.agent_run_id,
		command.status_revision,command.project_id,command.status,command.outcome,command.safe_reason,
		command.result_digest,outbox.delivery_state,outbox.safe_reason,outbox.result_digest
		FROM control_commands command JOIN control_outbox outbox ON outbox.command_id=command.command_id
		JOIN issues issue ON issue.id=command.root_issue_id AND issue.deleted_at IS NULL
		JOIN projects project ON project.id=command.project_id AND project.id=issue.project_id
		WHERE command.command_id=? AND command.lease_id=? AND command.lease_revision=?
		 AND outbox.lease_id=command.lease_id AND outbox.lease_revision=command.lease_revision
		 AND outbox.claim_user_id=? AND outbox.claim_api_key_id=? AND outbox.claim_device_id=?
		 AND outbox.claim_sequence=1
		 AND (project.status IN ('active','frozen') OR
		 (project.status='archived' AND command.action='run.cancel.running'))`, request.CommandID,
		request.LeaseID, request.LeaseRevision, authz.principal.UserID(), authz.principal.APIKeyID(),
		request.DeviceID).Scan(&action, &issueID, &runID, &statusRevision, &projectID, &commandStatus,
		&commandOutcome, &commandReason, &storedCommandDigest, &outboxState, &outboxReason, &storedOutboxDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	if err != nil {
		return CommandProjection{}, storageError(ctx, err)
	}
	if err := requireProjectEdit(ctx, authz.tx, authz.user, projectID); err != nil {
		return CommandProjection{}, err
	}
	if (request.Reason == "process_termination_failed" || request.Reason == "natural_exit") && action != "run.cancel.running" {
		return CommandProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	direct := commandStatus == "accepted" && statusRevision == 2 && !commandOutcome.Valid &&
		outboxState == "claimed" && !outboxReason.Valid
	lateUnknown := commandStatus == "accepted" && statusRevision == 3 && commandOutcome.String == "outcome_unknown" &&
		commandReason.String == "runner_lost" && outboxState == "claimed" && outboxReason.String == "runner_lost"
	terminal := (commandStatus == "applied" || commandStatus == "rejected") &&
		(statusRevision == 3 || statusRevision == 4) && outboxState == "acknowledged"
	if direct {
		requireActiveRun := request.Reason != "natural_exit"
		if _, err := loadLeaseRecordTx(ctx, authz.tx, authz.principal, request.LeaseID, request.LeaseRevision,
			request.DeviceID, "", requireActiveRun); err != nil {
			return CommandProjection{}, err
		}
	}
	replay, err := lookupOperation(ctx, authz.tx, authz.principal, "command.result", keyDigest, requestDigest)
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
	if terminal {
		exactOutcome := commandOutcome.String == string(request.Outcome)
		exactReason := commandReason.String == string(request.Reason) && outboxReason.String == string(request.Reason)
		if request.Outcome == "applied" {
			exactReason = !commandReason.Valid && !outboxReason.Valid
		}
		if !exactOutcome || !exactReason || !bytes.Equal(storedCommandDigest, resultDigest[:]) ||
			!bytes.Equal(storedOutboxDigest, resultDigest[:]) {
			return CommandProjection{}, domainError(ErrConflict, CodeStaleTarget)
		}
		projection, loadErr := loadCommandProjectionTx(ctx, authz.tx, request.CommandID)
		if loadErr != nil {
			return CommandProjection{}, loadErr
		}
		if err := insertOperation(ctx, authz.tx, authz.principal, "command.result", keyDigest, requestDigest,
			commandProjectionDigest(projection), "command_id", request.CommandID); err != nil {
			return CommandProjection{}, err
		}
		if err := commitWithWake(ctx, authz, nil, nil); err != nil {
			return CommandProjection{}, err
		}
		return projection, nil
	}
	if !direct && !lateUnknown {
		return CommandProjection{}, domainError(ErrConflict, CodeStaleTarget)
	}
	status := "applied"
	var reason any
	if request.Outcome == "rejected" {
		status, reason = "rejected", request.Reason
	}
	nextRevision := int64(3)
	effectEvent := "effect_acknowledged"
	if lateUnknown {
		nextRevision = 4
		effectEvent = "effect_reconciled"
	}
	if _, err := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status=?,status_revision=?,outcome=?,safe_reason=?,
		result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE command_id=?`, status, nextRevision, request.Outcome, reason, resultDigest[:], request.CommandID); err != nil {
		return CommandProjection{}, sqliteConflict(err)
	}
	if _, err := authz.tx.ExecContext(ctx, `UPDATE control_outbox SET delivery_state='acknowledged',result_sequence=1,
		result_digest=?,result_outcome=?,safe_reason=?,acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=?`, resultDigest[:], request.Outcome,
		reason, request.CommandID); err != nil {
		return CommandProjection{}, sqliteConflict(err)
	}
	commandEvent := "command_applied"
	if request.Outcome == "rejected" {
		commandEvent = "command_rejected"
	}
	if err := insertCommandEvent(ctx, authz.tx, request.CommandID, commandEvent); err != nil {
		return CommandProjection{}, err
	}
	if err := insertCommandEvent(ctx, authz.tx, request.CommandID, effectEvent); err != nil {
		return CommandProjection{}, err
	}
	var wake *CommitWake
	if request.Outcome == "applied" {
		switch action {
		case "input.respond":
			if err := resolveInputResultTx(ctx, authz.tx, request.CommandID, resultDigest); err != nil {
				return CommandProjection{}, err
			}
		case "run.pause", "run.resume":
			if err := updateRuntimeResultTx(ctx, authz.tx, request.CommandID, action, resultDigest); err != nil {
				return CommandProjection{}, err
			}
		case "run.cancel.running":
			if s.mutator == nil {
				return CommandProjection{}, domainError(ErrUnavailable, CodeDependencyUnavailable)
			}
			if err := mutationError(ctx, s.mutator.CancelRunningRunTx(ctx, authz.tx, RunCancellationMutation{IssueID: issueID,
				RunID: runID, ExpectedState: "running", ActorUserID: authz.principal.UserID(), CommandID: request.CommandID})); err != nil {
				return CommandProjection{}, err
			}
			if err := insertCancellationFact(ctx, authz.tx, request.CommandID, runID); err != nil {
				return CommandProjection{}, err
			}
			if err := insertCancellationEvent(ctx, authz.tx, request.CommandID, runID); err != nil {
				return CommandProjection{}, err
			}
		}
		if s.changes != nil {
			captured, err := s.changes.RecordControlChangeTx(ctx, authz.tx, ControlChange{IssueID: issueID, RunID: runID,
				CommandID: request.CommandID, Action: action})
			if err != nil {
				return CommandProjection{}, mutationError(ctx, err)
			}
			wake = &captured
		}
	}
	projection, err := loadCommandProjectionTx(ctx, authz.tx, request.CommandID)
	if err != nil {
		return CommandProjection{}, err
	}
	if err := insertOperation(ctx, authz.tx, authz.principal, "command.result", keyDigest, requestDigest,
		commandProjectionDigest(projection), "command_id", request.CommandID); err != nil {
		return CommandProjection{}, err
	}
	if err := commitWithWake(ctx, authz, s.changes, wake); err != nil {
		return CommandProjection{}, err
	}
	return projection, nil
}

func resolveInputResultTx(ctx context.Context, tx *sql.Tx, commandID string, resultDigest [32]byte) error {
	var requestID, kind string
	var revision int64
	var ordinal sql.NullInt64
	var code sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT input_request_id,input_request_revision,input_response_kind,
		input_choice_ordinal,input_choice_code FROM control_commands WHERE command_id=?`, commandID).
		Scan(&requestID, &revision, &kind, &ordinal, &code); err != nil {
		return storageError(ctx, err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO control_input_resolution_events(request_id,request_revision,
		sequence,event_kind,choice_ordinal,choice_code,event_digest,command_id) VALUES(?,?,?,?,?,?,?,?)`,
		requestID, revision, revision, kind, nullableInt(ordinal), nullableString(code), resultDigest[:], commandID)
	if err != nil {
		return sqliteConflict(err)
	}
	terminalID, err := result.LastInsertId()
	if err != nil {
		return storageError(ctx, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_input_request_states SET state_revision=state_revision+1,
		terminal_event_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE request_id=?`, terminalID, requestID); err != nil {
		return sqliteConflict(err)
	}
	return insertResolvedInputEvent(ctx, tx, requestID, revision)
}

func nullableInt(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

func nullableString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func insertResolvedInputEvent(ctx context.Context, tx *sql.Tx, requestID string, revision int64) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM control_events WHERE input_request_id=?`, requestID).Scan(&sequence); err != nil {
		return storageError(ctx, err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO control_events(sequence,event_kind,input_request_id,input_request_revision,
		actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,executor_user_id,
		executor_principal_kind,executor_api_key_id,device_id,delivery_id,root_issue_id,issue_revision,attempt_id,
		stage_key,execution_number,authority_epoch,reporter_id,agent_run_id,action,command_status,outcome,
		parameter_digest,binding_digest,result_digest)
		SELECT ?,'input_resolved',request.request_id,request.revision,command.actor_user_id,command.user_id,
		 command.principal_kind,command.actor_session_credential_id,command.actor_api_key_id,outbox.claim_user_id,
		 'api_key',outbox.claim_api_key_id,outbox.claim_device_id,request.delivery_id,request.root_issue_id,
		 request.issue_revision,request.attempt_id,request.stage_key,request.execution_number,request.authority_epoch,
		 request.reporter_id,request.agent_run_id,'input.respond','applied','applied',command.parameter_digest,
		 lease.binding_digest,terminal.event_digest FROM control_input_requests request
		JOIN control_input_resolution_events terminal ON terminal.request_id=request.request_id AND terminal.request_revision=request.revision
		JOIN control_commands command ON command.command_id=terminal.command_id
		JOIN control_outbox outbox ON outbox.command_id=command.command_id
		JOIN control_capability_leases lease ON lease.lease_id=request.lease_id AND lease.revision=request.lease_revision
		WHERE request.request_id=? AND request.revision=?`, sequence, requestID, revision)
	return sqliteConflict(err)
}

func updateRuntimeResultTx(ctx context.Context, tx *sql.Tx, commandID string, action Action, digest [32]byte) error {
	state := "paused"
	if action == "run.resume" {
		state = "running"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_runtime_states SET state=?,revision=revision+1,
		last_command_id=?,last_result_digest=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE agent_run_id=(SELECT agent_run_id FROM control_commands WHERE command_id=?)`, state, commandID,
		digest[:], commandID); err != nil {
		return sqliteConflict(err)
	}
	return insertCommandEvent(ctx, tx, commandID, "runtime_changed")
}

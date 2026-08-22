// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) StartStageRetry(ctx context.Context, req StageStartRequest) (StageRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StageRef{}, err
	}
	defer tx.Rollback()
	effects := s.NewEffects()
	ref, err := s.StartStageRetryTx(ctx, tx, effects, req)
	if err != nil {
		return StageRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return StageRef{}, err
	}
	effects.Dispatch(ctx)
	return ref, nil
}

func (s *Store) StartStageRetryTx(ctx context.Context, tx *sql.Tx, effects *Effects, req StageStartRequest) (StageRef, error) {
	return s.startStageRetryTx(ctx, tx, effects, req, false)
}

func (s *Store) startStageRetryTx(ctx context.Context, tx *sql.Tx, effects *Effects, req StageStartRequest, allowAtomicRunLink bool) (StageRef, error) {
	if req.IssueID <= 0 || stageOrder(req.StageKey) == 0 || validateActor(req.Reporter) != nil || req.IdempotencyKey == "" {
		return StageRef{}, fmt.Errorf("%w: invalid stage start", ErrInvalid)
	}
	if (req.ExpectedCurrentExecution == nil) != (req.ExpectedCurrentAuthorityEpoch == nil) ||
		(req.ExpectedCurrentExecution != nil && (*req.ExpectedCurrentExecution < 0 || *req.ExpectedCurrentAuthorityEpoch < 0 ||
			((*req.ExpectedCurrentExecution == 0) != (*req.ExpectedCurrentAuthorityEpoch == 0)))) {
		return StageRef{}, fmt.Errorf("%w: invalid current-stage expectation", ErrInvalid)
	}
	if req.Reporter.Type == "agent_run" {
		runID, ok := agentRunActorID(req.Reporter)
		if !allowAtomicRunLink || !ok {
			return StageRef{}, fmt.Errorf("%w: agent-run authority requires atomic run linkage", ErrInvariant)
		}
		var runIssue int64
		var instrumentation int
		if err := tx.QueryRowContext(ctx, `SELECT issue_id,delivery_instrumentation_version FROM agent_runs WHERE id=?`, runID).
			Scan(&runIssue, &instrumentation); err != nil || runIssue != req.IssueID || instrumentation != 1 {
			return StageRef{}, fmt.Errorf("%w: agent-run authority is not an instrumented root run", ErrInvariant)
		}
	}
	if req.ReasonCode == "" {
		req.ReasonCode = "stage_start"
	}
	if validatePersistedKey(req.ReasonCode, safeReasonCode, 64) != nil || validateBoundedText(req.ReasonText, maxReasonBytes, true) != nil {
		return StageRef{}, fmt.Errorf("%w: invalid stage reason", ErrInvalid)
	}
	if _, err := s.authorize(ctx, tx, req.IssueID, req.Reporter, "delivery.stage.start", nil); err != nil {
		return StageRef{}, err
	}
	d, err := loadDeliveryByIssue(ctx, tx, req.IssueID)
	if errors.Is(err, sql.ErrNoRows) {
		return StageRef{}, ErrNotFound
	}
	if err != nil {
		return StageRef{}, err
	}
	now := formatTime(s.now())
	reporterID, err := ensureReporterTx(ctx, tx, d.ID, req.Reporter, now)
	if err != nil {
		return StageRef{}, err
	}
	payload := struct {
		AttemptNumber            int64  `json:"attempt_number"`
		StageKey                 string `json:"stage_key"`
		ReporterType             string `json:"reporter_type"`
		ReporterKey              string `json:"reporter_key"`
		ReasonCode               string `json:"reason_code"`
		ReasonText               string `json:"reason_text"`
		ExpectedCurrentExecution *int64 `json:"expected_current_execution,omitempty"`
		ExpectedCurrentEpoch     *int64 `json:"expected_current_authority_epoch,omitempty"`
	}{req.AttemptNumber, req.StageKey, req.Reporter.Type, req.Reporter.OpaqueKey, req.ReasonCode, req.ReasonText,
		req.ExpectedCurrentExecution, req.ExpectedCurrentAuthorityEpoch}
	if prior, err := lookupEnvelopeDuplicateForActor(ctx, tx, d, req.Reporter, "stage_execution_started", req.IdempotencyKey, payload); err != nil {
		return StageRef{}, err
	} else if prior.Duplicate {
		return loadStageRefByDeliveryEvent(ctx, tx, prior.ID)
	}
	attempt, err := loadCurrentAttempt(ctx, tx, d.ID)
	if err != nil {
		return StageRef{}, err
	}
	if req.AttemptNumber != attempt.AttemptNumber {
		return StageRef{}, ErrConflict
	}
	if req.ExpectedCurrentExecution != nil {
		current, currentErr := loadCurrentStage(ctx, tx, d.ID, req.AttemptNumber, req.StageKey)
		if *req.ExpectedCurrentExecution == 0 {
			if currentErr == nil {
				return StageRef{}, fmt.Errorf("%w: stage already has a current execution", ErrConflict)
			}
			if !errors.Is(currentErr, sql.ErrNoRows) {
				return StageRef{}, currentErr
			}
		} else {
			if errors.Is(currentErr, sql.ErrNoRows) {
				return StageRef{}, fmt.Errorf("%w: expected current stage is absent", ErrConflict)
			}
			if currentErr != nil {
				return StageRef{}, currentErr
			}
			if current.ExecutionNumber != *req.ExpectedCurrentExecution || current.AuthorityEpoch != *req.ExpectedCurrentAuthorityEpoch {
				return StageRef{}, ErrStaleAuthority
			}
			terminal, _, terminalErr := stageExecutionTerminal(ctx, tx, current.AttemptID, req.StageKey, current.ExecutionNumber)
			if terminalErr != nil {
				return StageRef{}, terminalErr
			}
			if !terminal {
				return StageRef{}, fmt.Errorf("%w: current stage execution is still active", ErrConflict)
			}
		}
	}
	policy := policyForStage(attempt.Policies, req.StageKey)
	if policy == nil || policy.Applicability != "required" {
		return StageRef{}, fmt.Errorf("%w: stage is not required in this attempt", ErrInvariant)
	}
	basedOn, err := nearestRequiredPredecessor(ctx, tx, attempt, req.StageKey)
	if err != nil {
		return StageRef{}, err
	}
	var nextExecution int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(execution_number),0)+1 FROM delivery_stage_events
		WHERE attempt_id=? AND stage_key=?`, attempt.ID, req.StageKey).Scan(&nextExecution); err != nil {
		return StageRef{}, err
	}
	var retryOf, previous sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT execution_start_stage_event_id,authority_stage_event_id FROM delivery_stage_latest
		WHERE attempt_id=? AND stage_key=?`, attempt.ID, req.StageKey).Scan(&retryOf, &previous)
	event, err := s.appendEnvelopeTx(ctx, tx, effects, d, reporterID, "stage_execution_started", req.IdempotencyKey,
		payload, req.ReasonCode, req.ReasonText, "stage", "stage_event", nil, nil, now)
	if err != nil {
		return StageRef{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_stage_events(
		delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,
		delivery_event_id,event_type,reporter_id,previous_stage_event_id,based_on_stage_event_id,
		retry_of_stage_event_id,authority_source_sequence_cutoff,semantic_state,server_received_at)
		VALUES(?,?,?,?,1,1,?,'execution_started',?,?,?,?,0,'active',?)`, d.ID, attempt.ID, req.StageKey,
		nextExecution, event.ID, reporterID, nullableNullInt64(previous), basedOn, nullableNullInt64(retryOf), now)
	if err != nil {
		return StageRef{}, err
	}
	stageEventID, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_stage_latest(
		delivery_id,attempt_id,stage_key,execution_number,authority_epoch,current_reporter_id,
		execution_start_stage_event_id,authority_stage_event_id,updated_at)
		VALUES(?,?,?,?,1,?,?,?,?)
		ON CONFLICT(attempt_id,stage_key) DO UPDATE SET
		delivery_id=excluded.delivery_id,execution_number=excluded.execution_number,
		authority_epoch=excluded.authority_epoch,current_reporter_id=excluded.current_reporter_id,
		execution_start_stage_event_id=excluded.execution_start_stage_event_id,
		authority_stage_event_id=excluded.authority_stage_event_id,
		semantic_stage_event_id=NULL,heartbeat_stage_event_id=NULL,estimate_stage_event_id=NULL,
		updated_at=excluded.updated_at`, d.ID, attempt.ID, req.StageKey, nextExecution, reporterID,
		stageEventID, stageEventID, now); err != nil {
		return StageRef{}, err
	}
	return StageRef{DeliveryID: d.ID, AttemptID: attempt.ID, AttemptNumber: attempt.AttemptNumber,
		StageKey: req.StageKey, ExecutionNumber: nextExecution, AuthorityEpoch: 1, ReporterID: reporterID,
		ExecutionStartEventID: stageEventID, BasedOnStageEventID: basedOn}, nil
}

func loadProgressResetRefByDeliveryEvent(ctx context.Context, q DBTX, eventID int64) (StageRef, error) {
	var out StageRef
	var based sql.NullInt64
	err := q.QueryRowContext(ctx, `WITH RECURSIVE authority(id,event_type,reporter_id,previous_stage_event_id) AS (
		SELECT anchor.id,anchor.event_type,anchor.reporter_id,anchor.previous_stage_event_id
		FROM delivery_stage_events reset JOIN delivery_stage_events anchor
		 ON anchor.id=reset.reset_authority_anchor_stage_event_id
		WHERE reset.delivery_event_id=?
		UNION ALL
		SELECT prior.id,prior.event_type,prior.reporter_id,prior.previous_stage_event_id
		FROM delivery_stage_events prior JOIN authority current ON prior.id=current.previous_stage_event_id
		WHERE current.event_type='progress_reset_authorized'
	)
	SELECT se.delivery_id,se.attempt_id,a.attempt_number,se.stage_key,se.execution_number,se.authority_epoch,
		(SELECT reporter_id FROM authority WHERE event_type IN ('execution_started','handoff') LIMIT 1),
		se.execution_start_stage_event_id,start.based_on_stage_event_id
	FROM delivery_stage_events se JOIN delivery_attempts a ON a.id=se.attempt_id
	JOIN delivery_stage_events start ON start.id=se.execution_start_stage_event_id
	WHERE se.delivery_event_id=? AND se.event_type='progress_reset_authorized'`, eventID, eventID).
		Scan(&out.DeliveryID, &out.AttemptID, &out.AttemptNumber, &out.StageKey, &out.ExecutionNumber,
			&out.AuthorityEpoch, &out.ReporterID, &out.ExecutionStartEventID, &based)
	if errors.Is(err, sql.ErrNoRows) {
		return StageRef{}, ErrNotFound
	}
	out.BasedOnStageEventID = nullInt64Ptr(based)
	return out, err
}

func (s *Store) RecordHandoff(ctx context.Context, req HandoffRequest) (StageRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StageRef{}, err
	}
	defer tx.Rollback()
	effects := s.NewEffects()
	ref, err := s.RecordHandoffTx(ctx, tx, effects, req)
	if err != nil {
		return StageRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return StageRef{}, err
	}
	effects.Dispatch(ctx)
	return ref, nil
}

func (s *Store) AuthorizeProgressReset(ctx context.Context, req ProgressResetRequest) (StageRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StageRef{}, err
	}
	defer tx.Rollback()
	effects := s.NewEffects()
	ref, err := s.AuthorizeProgressResetTx(ctx, tx, effects, req)
	if err != nil {
		return StageRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return StageRef{}, err
	}
	effects.Dispatch(ctx)
	return ref, nil
}

func (s *Store) RecordHandoffTx(ctx context.Context, tx *sql.Tx, effects *Effects, req HandoffRequest) (StageRef, error) {
	if validateActor(req.From) != nil || validateActor(req.To) != nil || req.From == req.To || req.IdempotencyKey == "" ||
		stageOrder(req.StageKey) == 0 || req.AttemptNumber <= 0 || req.ExecutionNumber <= 0 || req.AuthorityEpoch <= 0 ||
		validatePersistedKey(req.ReasonCode, safeReasonCode, 64) != nil || validateBoundedText(req.ReasonText, maxReasonBytes, false) != nil {
		return StageRef{}, fmt.Errorf("%w: invalid handoff", ErrInvalid)
	}
	if _, err := s.authorize(ctx, tx, req.IssueID, req.From, "delivery.stage.handoff", nil); err != nil {
		return StageRef{}, err
	}
	d, err := loadDeliveryByIssue(ctx, tx, req.IssueID)
	if err != nil {
		return StageRef{}, ErrNotFound
	}
	now := formatTime(s.now())
	fromID, err := ensureReporterTx(ctx, tx, d.ID, req.From, now)
	if err != nil {
		return StageRef{}, err
	}
	toID, err := ensureReporterTx(ctx, tx, d.ID, req.To, now)
	if err != nil {
		return StageRef{}, err
	}
	payload := struct {
		Attempt, Execution, Epoch                                       int64
		Stage, FromType, FromKey, ToType, ToKey, ReasonCode, ReasonText string
	}{req.AttemptNumber, req.ExecutionNumber, req.AuthorityEpoch, req.StageKey, req.From.Type, req.From.OpaqueKey,
		req.To.Type, req.To.OpaqueKey, req.ReasonCode, req.ReasonText}
	if prior, err := lookupEnvelopeDuplicateForActor(ctx, tx, d, req.To, "handoff", req.IdempotencyKey, payload); err != nil {
		return StageRef{}, err
	} else if prior.Duplicate {
		return loadStageRefByDeliveryEvent(ctx, tx, prior.ID)
	}
	attempt, err := loadCurrentAttempt(ctx, tx, d.ID)
	if err != nil {
		return StageRef{}, err
	}
	if attempt.AttemptNumber != req.AttemptNumber {
		return StageRef{}, fmt.Errorf("%w: handoff targets a superseded attempt", ErrConflict)
	}
	current, err := loadCurrentStage(ctx, tx, d.ID, req.AttemptNumber, req.StageKey)
	if err != nil {
		return StageRef{}, err
	}
	if current.ExecutionNumber != req.ExecutionNumber || current.AuthorityEpoch != req.AuthorityEpoch || current.ReporterID != fromID {
		return StageRef{}, ErrStaleAuthority
	}
	if req.From.Type == "agent_run" {
		runID, ok := agentRunActorID(req.From)
		var count int
		if !ok || tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_agent_run_activations
			WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?
			 AND agent_run_id=? AND reporter_id=?`, current.AttemptID, req.StageKey, req.ExecutionNumber,
			req.AuthorityEpoch, runID, fromID).Scan(&count) != nil || count != 1 {
			return StageRef{}, fmt.Errorf("%w: current agent-run authority lacks activation", ErrInvariant)
		}
	}
	var activationRunID, activationCutoff int64
	if req.To.Type == "agent_run" {
		var ok bool
		activationRunID, ok = agentRunActorID(req.To)
		if !ok {
			return StageRef{}, fmt.Errorf("%w: invalid agent-run handoff target", ErrInvariant)
		}
		var linkedReporter int64
		var runStatus string
		if err := tx.QueryRowContext(ctx, `SELECT link.reporter_id,run.status FROM delivery_agent_run_links link
			JOIN agent_runs run ON run.id=link.agent_run_id
			WHERE link.agent_run_id=? AND link.attempt_id=? AND link.stage_key=? AND link.execution_number=?`, activationRunID,
			current.AttemptID, req.StageKey, req.ExecutionNumber).Scan(&linkedReporter, &runStatus); err != nil || linkedReporter != toID {
			return StageRef{}, fmt.Errorf("%w: agent-run handoff target is not linked to this execution", ErrInvariant)
		}
		if runStatus != "queued" && runStatus != "running" {
			return StageRef{}, fmt.Errorf("%w: terminal agent run cannot regain stage authority", ErrConflict)
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM agent_run_telemetry WHERE run_id=?`, activationRunID).
			Scan(&activationCutoff); err != nil {
			return StageRef{}, err
		}
	}
	if terminal, _, err := stageExecutionTerminal(ctx, tx, current.AttemptID, req.StageKey, req.ExecutionNumber); err != nil {
		return StageRef{}, err
	} else if terminal {
		return StageRef{}, fmt.Errorf("%w: stage execution is terminal; start a retry", ErrConflict)
	}
	nextEpoch := current.AuthorityEpoch + 1
	nextSequence, err := nextStageEventSequence(ctx, tx, current.AttemptID, req.StageKey, current.ExecutionNumber)
	if err != nil {
		return StageRef{}, err
	}
	var sourceCutoff int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(source_sequence),0) FROM delivery_stage_events
		WHERE attempt_id=? AND stage_key=? AND execution_number=? AND reporter_id=?`, current.AttemptID,
		req.StageKey, current.ExecutionNumber, toID).Scan(&sourceCutoff); err != nil {
		return StageRef{}, err
	}
	event, err := s.appendEnvelopeTx(ctx, tx, effects, d, toID, "handoff", req.IdempotencyKey, payload,
		req.ReasonCode, req.ReasonText, "stage", "stage_event", nil, nil, now)
	if err != nil {
		return StageRef{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_stage_events(
		delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,delivery_event_id,
		event_type,reporter_id,execution_start_stage_event_id,previous_stage_event_id,handoff_from_reporter_id,
		authority_source_sequence_cutoff,reason_code,reason_text,server_received_at)
		VALUES(?,?,?,?,?,?,?,'handoff',?,?,?,?,?,?,?,?)`, d.ID, current.AttemptID, req.StageKey,
		current.ExecutionNumber, nextSequence, nextEpoch, event.ID, toID, current.ExecutionStartEventID,
		current.AuthorityStageEventID, fromID, sourceCutoff, req.ReasonCode, req.ReasonText, now)
	if err != nil {
		return StageRef{}, err
	}
	stageEventID, _ := res.LastInsertId()
	if activationRunID > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_agent_run_activations(delivery_id,attempt_id,stage_key,
			execution_number,authority_epoch,agent_run_id,reporter_id,authority_stage_event_id,
			telemetry_sequence_cutoff,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, d.ID, current.AttemptID,
			req.StageKey, current.ExecutionNumber, nextEpoch, activationRunID, toID, stageEventID, activationCutoff, now); err != nil {
			return StageRef{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delivery_stage_latest SET authority_epoch=?,current_reporter_id=?,
		authority_stage_event_id=?,semantic_stage_event_id=NULL,heartbeat_stage_event_id=NULL,
		estimate_stage_event_id=NULL,updated_at=? WHERE attempt_id=? AND stage_key=?`, nextEpoch, toID,
		stageEventID, now, current.AttemptID, req.StageKey); err != nil {
		return StageRef{}, err
	}
	current.AuthorityEpoch, current.ReporterID, current.AuthorityStageEventID = nextEpoch, toID, stageEventID
	return current.StageRef, nil
}

func (s *Store) AuthorizeProgressResetTx(ctx context.Context, tx *sql.Tx, effects *Effects, req ProgressResetRequest) (StageRef, error) {
	if validateActor(req.Actor) != nil || validatePersistedKey(req.ReasonCode, safeReasonCode, 64) != nil ||
		validateBoundedText(req.ReasonText, maxReasonBytes, false) != nil || req.IdempotencyKey == "" {
		return StageRef{}, fmt.Errorf("%w: invalid progress reset", ErrInvalid)
	}
	if _, err := s.authorize(ctx, tx, req.IssueID, req.Actor, "delivery.progress.reset", nil); err != nil {
		return StageRef{}, err
	}
	d, err := loadDeliveryByIssue(ctx, tx, req.IssueID)
	if err != nil {
		return StageRef{}, ErrNotFound
	}
	now := formatTime(s.now())
	payload := struct {
		Attempt, Execution, Epoch                          int64
		Stage, ActorType, ActorKey, ReasonCode, ReasonText string
	}{req.AttemptNumber, req.ExecutionNumber, req.AuthorityEpoch, req.StageKey, req.Actor.Type, req.Actor.OpaqueKey,
		req.ReasonCode, req.ReasonText}
	if prior, err := lookupEnvelopeDuplicateForActor(ctx, tx, d, req.Actor, "progress_reset_authorized", req.IdempotencyKey, payload); err != nil {
		return StageRef{}, err
	} else if prior.Duplicate {
		return loadProgressResetRefByDeliveryEvent(ctx, tx, prior.ID)
	}
	actorID, err := ensureReporterTx(ctx, tx, d.ID, req.Actor, now)
	if err != nil {
		return StageRef{}, err
	}
	attempt, err := loadCurrentAttempt(ctx, tx, d.ID)
	if err != nil {
		return StageRef{}, err
	}
	if attempt.AttemptNumber != req.AttemptNumber {
		return StageRef{}, fmt.Errorf("%w: reset targets a superseded attempt", ErrConflict)
	}
	current, err := loadCurrentStage(ctx, tx, d.ID, req.AttemptNumber, req.StageKey)
	if err != nil {
		return StageRef{}, err
	}
	if current.ExecutionNumber != req.ExecutionNumber || current.AuthorityEpoch != req.AuthorityEpoch {
		return StageRef{}, ErrStaleAuthority
	}
	if terminal, _, err := stageExecutionTerminal(ctx, tx, current.AttemptID, req.StageKey, req.ExecutionNumber); err != nil {
		return StageRef{}, err
	} else if terminal {
		return StageRef{}, fmt.Errorf("%w: stage execution is terminal; start a retry", ErrConflict)
	}
	var cutoff, resetEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(MAX(CASE WHEN authority_epoch=? AND reporter_id=? THEN source_sequence END),0),
		COALESCE(MAX(reset_epoch),0)+1
		FROM delivery_stage_events WHERE attempt_id=? AND stage_key=? AND execution_number=?`,
		req.AuthorityEpoch, current.ReporterID, current.AttemptID, req.StageKey, req.ExecutionNumber).
		Scan(&cutoff, &resetEpoch); err != nil {
		return StageRef{}, err
	}
	resetSourceKind := "stage_events"
	var telemetryRunID, telemetryCutoff sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT link.agent_run_id,
		COALESCE((SELECT MAX(t.sequence) FROM agent_run_telemetry t WHERE t.run_id=link.agent_run_id),0)
		FROM delivery_agent_run_links link
		WHERE link.attempt_id=? AND link.stage_key=? AND link.execution_number=? AND link.reporter_id=?`,
		current.AttemptID, req.StageKey, req.ExecutionNumber, current.ReporterID).
		Scan(&telemetryRunID, &telemetryCutoff)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StageRef{}, err
	}
	if err == nil {
		resetSourceKind = "stage_and_agent_run_telemetry"
	}
	var ownerAuthorityAnchor int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM delivery_stage_events
		WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?
		 AND reporter_id=? AND event_type IN ('execution_started','handoff')
		ORDER BY event_sequence DESC LIMIT 1`, current.AttemptID, req.StageKey, req.ExecutionNumber,
		req.AuthorityEpoch, current.ReporterID).Scan(&ownerAuthorityAnchor); err != nil {
		return StageRef{}, fmt.Errorf("%w: reset authority owner anchor is missing", ErrInvariant)
	}
	nextSequence, err := nextStageEventSequence(ctx, tx, current.AttemptID, req.StageKey, req.ExecutionNumber)
	if err != nil {
		return StageRef{}, err
	}
	event, err := s.appendEnvelopeTx(ctx, tx, effects, d, actorID, "progress_reset_authorized", req.IdempotencyKey,
		payload, req.ReasonCode, req.ReasonText, "stage", "stage_event", nil, &cutoff, now)
	if err != nil {
		return StageRef{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_stage_events(
		delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,delivery_event_id,event_type,
		reporter_id,execution_start_stage_event_id,previous_stage_event_id,reset_epoch,reset_source_cutoff,
		reset_source_kind,reset_telemetry_run_id,reset_telemetry_sequence_cutoff,
		reset_authority_anchor_stage_event_id,reset_owner_reporter_id,reason_code,reason_text,server_received_at)
		VALUES(?,?,?,?,?,?,?,'progress_reset_authorized',?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.ID, current.AttemptID, req.StageKey,
		current.ExecutionNumber, nextSequence, current.AuthorityEpoch, event.ID, actorID,
		current.ExecutionStartEventID, current.AuthorityStageEventID, resetEpoch, cutoff,
		resetSourceKind, nullableNullInt64(telemetryRunID), nullableNullInt64(telemetryCutoff),
		ownerAuthorityAnchor, current.ReporterID, req.ReasonCode, req.ReasonText, now)
	if err != nil {
		return StageRef{}, err
	}
	stageEventID, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE delivery_stage_latest SET authority_stage_event_id=?,estimate_stage_event_id=NULL,
		updated_at=? WHERE attempt_id=? AND stage_key=?`, stageEventID, now, current.AttemptID, req.StageKey); err != nil {
		return StageRef{}, err
	}
	current.AuthorityStageEventID = stageEventID
	return current.StageRef, nil
}

type currentStage struct {
	StageRef
	AuthorityStageEventID int64
}

func loadCurrentStage(ctx context.Context, q DBTX, deliveryID, attemptNumber int64, stage string) (currentStage, error) {
	var out currentStage
	var based sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT l.delivery_id,l.attempt_id,a.attempt_number,l.stage_key,l.execution_number,
		l.authority_epoch,l.current_reporter_id,l.execution_start_stage_event_id,s.based_on_stage_event_id,
		l.authority_stage_event_id
		FROM delivery_stage_latest l JOIN delivery_attempts a ON a.id=l.attempt_id
		JOIN delivery_stage_events s ON s.id=l.execution_start_stage_event_id
		WHERE l.delivery_id=? AND a.attempt_number=? AND l.stage_key=?`, deliveryID, attemptNumber, stage).
		Scan(&out.DeliveryID, &out.AttemptID, &out.AttemptNumber, &out.StageKey, &out.ExecutionNumber,
			&out.AuthorityEpoch, &out.ReporterID, &out.ExecutionStartEventID, &based, &out.AuthorityStageEventID)
	out.BasedOnStageEventID = nullInt64Ptr(based)
	return out, err
}

func loadStageRefByDeliveryEvent(ctx context.Context, q DBTX, eventID int64) (StageRef, error) {
	var out StageRef
	var based sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT se.delivery_id,se.attempt_id,a.attempt_number,se.stage_key,se.execution_number,
		se.authority_epoch,se.reporter_id,COALESCE(se.execution_start_stage_event_id,se.id),start.based_on_stage_event_id
		FROM delivery_stage_events se JOIN delivery_attempts a ON a.id=se.attempt_id
		JOIN delivery_stage_events start ON start.id=COALESCE(se.execution_start_stage_event_id,se.id)
		WHERE se.delivery_event_id=?`, eventID).Scan(&out.DeliveryID, &out.AttemptID, &out.AttemptNumber,
		&out.StageKey, &out.ExecutionNumber, &out.AuthorityEpoch, &out.ReporterID,
		&out.ExecutionStartEventID, &based)
	if errors.Is(err, sql.ErrNoRows) {
		return StageRef{}, ErrNotFound
	}
	out.BasedOnStageEventID = nullInt64Ptr(based)
	return out, err
}

func loadStageRefByStageEvent(ctx context.Context, q DBTX, stageEventID int64) (StageRef, error) {
	var out StageRef
	var based sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT se.delivery_id,se.attempt_id,a.attempt_number,se.stage_key,se.execution_number,
		se.authority_epoch,se.reporter_id,COALESCE(se.execution_start_stage_event_id,se.id),start.based_on_stage_event_id
		FROM delivery_stage_events se JOIN delivery_attempts a ON a.id=se.attempt_id
		JOIN delivery_stage_events start ON start.id=COALESCE(se.execution_start_stage_event_id,se.id)
		WHERE se.id=?`, stageEventID).Scan(&out.DeliveryID, &out.AttemptID, &out.AttemptNumber,
		&out.StageKey, &out.ExecutionNumber, &out.AuthorityEpoch, &out.ReporterID,
		&out.ExecutionStartEventID, &based)
	if errors.Is(err, sql.ErrNoRows) {
		return StageRef{}, ErrNotFound
	}
	out.BasedOnStageEventID = nullInt64Ptr(based)
	return out, err
}

func nextStageEventSequence(ctx context.Context, q DBTX, attemptID int64, stage string, execution int64) (int64, error) {
	var next int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_sequence),0)+1 FROM delivery_stage_events
		WHERE attempt_id=? AND stage_key=? AND execution_number=?`, attemptID, stage, execution).Scan(&next)
	return next, err
}

func policyForStage(policies []Policy, stage string) *Policy {
	for i := range policies {
		if policies[i].StageKey == stage {
			return &policies[i]
		}
	}
	return nil
}

func nearestRequiredPredecessor(ctx context.Context, q DBTX, attempt Attempt, stage string) (*int64, error) {
	return nearestRequiredPredecessorMode(ctx, q, attempt, stage, false)
}

func nearestRequiredPredecessorMode(ctx context.Context, q DBTX, attempt Attempt, stage string, includeHidden bool) (*int64, error) {
	idx := stageOrder(stage) - 1
	for i := idx - 1; i >= 0; i-- {
		if attempt.Policies[i].Applicability != "required" {
			continue
		}
		id, ok, err := eligibleSuccessEventIDMode(ctx, q, attempt, attempt.Policies[i].StageKey, includeHidden)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: required predecessor %s is not eligible", ErrInvariant, attempt.Policies[i].StageKey)
		}
		return &id, nil
	}
	return nil, nil
}

// stageExecutionCurrentLineage verifies the complete required-stage chain, not
// merely the target's numeric execution/owner. This keeps a downstream linked
// run historical after any upstream retry invalidates its based-on fact.
func stageExecutionCurrentLineage(ctx context.Context, q DBTX, attempt Attempt, stage string, executionStartID int64) (bool, error) {
	return stageExecutionCurrentLineageMode(ctx, q, attempt, stage, executionStartID, false)
}

func stageExecutionCurrentLineageMode(ctx context.Context, q DBTX, attempt Attempt, stage string, executionStartID int64, includeHidden bool) (bool, error) {
	var predecessor int64
	for _, policy := range attempt.Policies {
		if policy.Applicability != "required" {
			if policy.StageKey == stage {
				return false, nil
			}
			continue
		}
		if policy.StageKey == stage {
			var based sql.NullInt64
			if err := q.QueryRowContext(ctx, `SELECT based_on_stage_event_id FROM delivery_stage_events
				WHERE id=? AND attempt_id=? AND stage_key=? AND event_type='execution_started'`,
				executionStartID, attempt.ID, stage).Scan(&based); err != nil {
				return false, fmt.Errorf("load %s execution start lineage: %w", stage, err)
			}
			return nullableInt64Equal(based, predecessor), nil
		}
		semanticID, eligible, err := eligibleSuccessEventIDMode(ctx, q, attempt, policy.StageKey, includeHidden)
		if err != nil {
			return false, fmt.Errorf("check %s predecessor eligibility: %w", policy.StageKey, err)
		}
		if !eligible {
			return false, nil
		}
		var based sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT start.based_on_stage_event_id
			FROM delivery_stage_latest latest JOIN delivery_stage_events start
			 ON start.id=latest.execution_start_stage_event_id
			WHERE latest.attempt_id=? AND latest.stage_key=?`, attempt.ID, policy.StageKey).Scan(&based); err != nil {
			return false, fmt.Errorf("load %s predecessor lineage: %w", policy.StageKey, err)
		}
		if !nullableInt64Equal(based, predecessor) {
			return false, nil
		}
		predecessor = semanticID
	}
	return false, nil
}

func nullableInt64Equal(value sql.NullInt64, expected int64) bool {
	if expected == 0 {
		return !value.Valid
	}
	return value.Valid && value.Int64 == expected
}

func eligibleSuccessEventID(ctx context.Context, q DBTX, attempt Attempt, stage string) (int64, bool, error) {
	return eligibleSuccessEventIDMode(ctx, q, attempt, stage, false)
}

func eligibleSuccessEventIDMode(ctx context.Context, q DBTX, attempt Attempt, stage string, includeHidden bool) (int64, bool, error) {
	policy := policyForStage(attempt.Policies, stage)
	if policy == nil || policy.Applicability != "required" {
		return 0, false, nil
	}
	var semanticID, authority, reporter, currentBlockers int64
	var state string
	var based, semanticSpecRevision sql.NullInt64
	var deliverySpecRevision int64
	err := q.QueryRowContext(ctx, `SELECT sem.id,sem.semantic_state,sem.authority_epoch,sem.reporter_id,
		sem.current_blocker_count,start.based_on_stage_event_id,sem.spec_revision,d.spec_revision
		FROM delivery_stage_latest l
		JOIN delivery_stage_events sem ON sem.id=l.semantic_stage_event_id
		JOIN delivery_stage_events start ON start.id=l.execution_start_stage_event_id
		JOIN deliveries d ON d.id=l.delivery_id
		WHERE l.attempt_id=? AND l.stage_key=? AND sem.authority_epoch=l.authority_epoch
		AND sem.reporter_id=l.current_reporter_id`, attempt.ID, stage).
		Scan(&semanticID, &state, &authority, &reporter, &currentBlockers, &based, &semanticSpecRevision, &deliverySpecRevision)
	_ = authority
	_ = reporter
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if state != "succeeded" || currentBlockers != 0 {
		return semanticID, false, nil
	}
	if stage == StageSpecification && (!semanticSpecRevision.Valid || semanticSpecRevision.Int64 != deliverySpecRevision) {
		return semanticID, false, nil
	}
	var passed, failed int
	primary, alternate := allowedEvidenceTypes(stage)
	if err := q.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN outcome='passed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN outcome='failed' THEN 1 ELSE 0 END),0)
		FROM delivery_evidence WHERE stage_event_id=? AND evidence_type IN (?,?)`, semanticID, primary, alternate).
		Scan(&passed, &failed); err != nil {
		return 0, false, err
	}
	if failed > 0 {
		return semanticID, false, nil
	}
	if stage == StageSpecification {
		var issueID int64
		if err := q.QueryRowContext(ctx, `SELECT issue_id FROM deliveries WHERE id=?`, attempt.DeliveryID).Scan(&issueID); err != nil {
			return 0, false, err
		}
		var digest string
		var err error
		if includeHidden {
			digest, err = canonicalStoredIssueSpecDigest(ctx, q, issueID)
		} else {
			digest, err = canonicalIssueSpecDigest(ctx, q, issueID)
		}
		if err != nil {
			return 0, false, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_evidence
			WHERE stage_event_id=? AND evidence_type IN ('spec_acceptance','approval')
			AND outcome='passed' AND digest_sha256=?`, semanticID, digest).Scan(&passed); err != nil {
			return 0, false, err
		}
	}
	if passed == 0 {
		return semanticID, false, nil
	}
	expected, err := nearestRequiredPredecessorMode(ctx, q, attempt, stage, includeHidden)
	if err != nil {
		return semanticID, false, nil
	}
	if !sameNullableID(based, expected) {
		return semanticID, false, nil
	}
	return semanticID, true, nil
}

func allowedEvidenceType(stage string) string {
	switch stage {
	case StageSpecification:
		return "spec_acceptance"
	case StageImplementation:
		return "implementation_result"
	case StageQA:
		return "test_result"
	case StageDeployment:
		return "deployment_result"
	case StageVerification:
		return "verification_result"
	default:
		return ""
	}
}

func allowedEvidenceTypes(stage string) (string, string) {
	primary := allowedEvidenceType(stage)
	switch stage {
	case StageSpecification:
		return primary, "approval"
	case StageImplementation:
		return primary, "artifact"
	default:
		return primary, primary
	}
}

func evidenceAllowed(stage, kind string) bool {
	if kind == allowedEvidenceType(stage) {
		return true
	}
	return (stage == StageSpecification && kind == "approval") || (stage == StageImplementation && kind == "artifact")
}

func sameNullableID(actual sql.NullInt64, expected *int64) bool {
	if expected == nil {
		return !actual.Valid
	}
	return actual.Valid && actual.Int64 == *expected
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var commitDigest = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func agentRunActorID(actor Actor) (int64, bool) {
	if actor.Type != "agent_run" || !strings.HasPrefix(actor.OpaqueKey, "run:") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(actor.OpaqueKey, "run:"), 10, 64)
	return id, err == nil && id > 0
}

// RecordRunTelemetryChangeTx links a PAI-799 telemetry append to the durable
// delivery invalidation stream without copying provider telemetry into the
// delivery domain. The telemetry row and hint share the caller's transaction.
func (s *Store) RecordRunTelemetryChangeTx(ctx context.Context, tx *sql.Tx, effects *Effects, runID, sourceSequence int64) error {
	if runID <= 0 || sourceSequence <= 0 {
		return ErrInvalid
	}
	var instrumentation int
	if err := tx.QueryRowContext(ctx, `SELECT delivery_instrumentation_version FROM agent_runs WHERE id=?`, runID).
		Scan(&instrumentation); err != nil {
		return err
	}
	if instrumentation == 0 {
		return nil
	}
	var d deliveryRow
	var project sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT d.id,d.issue_id,d.delivery_key,d.project_id_hint
		FROM delivery_agent_run_links link JOIN deliveries d ON d.id=link.delivery_id
		WHERE link.agent_run_id=?`, runID).Scan(&d.ID, &d.IssueID, &d.Key, &project)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: instrumented telemetry run is unlinked", ErrInvariant)
	}
	if err != nil {
		return err
	}
	d.ProjectID = nullInt64Ptr(project)
	var deletedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM issues WHERE id=?`, d.IssueID).Scan(&deletedAt); err != nil {
		return err
	}
	// Telemetry remains durable in its run-local ledger while the root is
	// hidden. It must not create an audience signal or require live-project
	// authorization until the issue is restored.
	if deletedAt.Valid {
		return nil
	}
	actor := Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)}
	if _, err := s.authorize(ctx, tx, d.IssueID, actor, "delivery.run.telemetry", nil); err != nil {
		return err
	}
	snapshot, err := s.SnapshotByIssueTx(ctx, tx, d.IssueID)
	if err != nil {
		return err
	}
	currentAuthority := false
	var activationCutoff int64
	for _, stage := range snapshot.Stages {
		if stage.OwnerRunID != nil && *stage.OwnerRunID == runID && stage.CurrentLineage {
			currentAuthority = true
			activationCutoff = stage.AuthorityActivationCutoff
			break
		}
	}
	if !currentAuthority {
		// The run-local PAI-799 ledger remains authoritative history, but revoked
		// telemetry cannot invalidate or later re-enter the current delivery.
		return nil
	}
	var kind, phase, activity, blockerState, receivedAt string
	var heartbeat, needsInput int
	var estimateRevision sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT kind,phase,heartbeat,activity,needs_input,blocker_state,
		estimate_revision,server_received_at FROM agent_run_telemetry WHERE run_id=? AND sequence=?`,
		runID, sourceSequence).Scan(&kind, &phase, &heartbeat, &activity, &needsInput, &blockerState,
		&estimateRevision, &receivedAt); err != nil {
		return err
	}
	semantic := kind == "phase" || kind == "needs_input" || kind == "blocker" || phase != "unknown" ||
		activity != "" || needsInput == 1 || blockerState != "none"
	emit := semantic || estimateRevision.Valid
	if !emit && heartbeat == 1 {
		var prior string
		err := tx.QueryRowContext(ctx, `SELECT server_received_at FROM agent_run_telemetry
			WHERE run_id=? AND sequence>? AND sequence<? AND heartbeat=1 ORDER BY sequence DESC LIMIT 1`,
			runID, activationCutoff, sourceSequence).Scan(&prior)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// The first heartbeat crosses the never-signaled -> live boundary.
			emit = true
		case err != nil:
			return err
		default:
			previousAt, parseErr := time.Parse(time.RFC3339Nano, prior)
			currentAt, currentErr := time.Parse(time.RFC3339Nano, receivedAt)
			if parseErr != nil || currentErr != nil {
				return fmt.Errorf("%w: malformed telemetry receipt time", ErrInvariant)
			}
			// Ordinary cadence heartbeats remain in telemetry history but do not
			// spam the durable delivery stream. The first beat after a stale gap
			// is a semantic recovery boundary and does invalidate the projection.
			emit = currentAt.Sub(previousAt) >= s.freshness.HeartbeatTimeout
		}
	}
	if !emit {
		return nil
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_revision),0) FROM delivery_events WHERE delivery_id=?`, d.ID).Scan(&revision); err != nil {
		return err
	}
	hint, err := appendChangeTx(ctx, tx, d, revision, "telemetry", "telemetry", &runID, &sourceSequence, receivedAt)
	if err != nil {
		return err
	}
	effects.add(hint)
	return nil
}

func (s *Store) recordLegacyRunMutationChangeTx(ctx context.Context, tx *sql.Tx, effects *Effects, runID int64) error {
	var status string
	var issueDeleted sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ar.status,i.deleted_at FROM agent_runs ar
		JOIN issues i ON i.id=ar.issue_id WHERE ar.id=?`, runID).Scan(&status, &issueDeleted); err != nil {
		return err
	}
	// Hidden synthetic membership is derived directly from the run status. Its
	// lifecycle must remain writable, but it cannot create or wake an Agent Mode
	// audience. In particular an M145-seeded root has no prior run hint to find.
	if issueDeleted.Valid {
		return nil
	}
	if status == "queued" || status == "running" {
		// Active-to-active updates do not change the synthetic row. Initial and
		// inactive-to-active membership are covered by M145 insert/update guards.
		return nil
	}
	var hint ChangeHint
	var project, revoked, sourceID, sourceSequence sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
		revoked_project_id,change_sequence,delivery_revision,kind,source_kind,source_id,source_sequence,server_received_at
		FROM delivery_change_log WHERE kind='run' AND source_kind='agent_run' AND source_id=?
		ORDER BY id DESC LIMIT 1`, runID).Scan(&hint.InternalID, &hint.CursorToken, &hint.DeliveryID,
		&hint.RootIssueID, &hint.DeliveryKey, &project, &revoked, &hint.ChangeSequence,
		&hint.DeliveryRevision, &hint.Kind, &hint.SourceKind, &sourceID, &sourceSequence, &hint.ServerReceivedAt)
	if err != nil {
		return fmt.Errorf("%w: legacy run membership change is missing", ErrInvariant)
	}
	hint.ProjectIDHint, hint.RevokedProjectID = nullInt64Ptr(project), nullInt64Ptr(revoked)
	hint.SourceID, hint.SourceSequence = nullInt64Ptr(sourceID), nullInt64Ptr(sourceSequence)
	effects.add(hint)
	return nil
}

func (s *Store) RecordRunMutationChangeTx(ctx context.Context, tx *sql.Tx, effects *Effects, runID int64) error {
	if runID <= 0 {
		return ErrInvalid
	}
	var instrumentation int
	if err := tx.QueryRowContext(ctx, `SELECT delivery_instrumentation_version FROM agent_runs WHERE id=?`, runID).
		Scan(&instrumentation); err != nil {
		return err
	}
	if instrumentation == 0 {
		return s.recordLegacyRunMutationChangeTx(ctx, tx, effects, runID)
	}
	var d deliveryRow
	var project sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT d.id,d.issue_id,d.delivery_key,d.project_id_hint
		FROM delivery_agent_run_links link JOIN deliveries d ON d.id=link.delivery_id WHERE link.agent_run_id=?`, runID).
		Scan(&d.ID, &d.IssueID, &d.Key, &project)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: instrumented run is unlinked", ErrInvariant)
	}
	if err != nil {
		return err
	}
	d.ProjectID = nullInt64Ptr(project)
	actor := Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)}
	if _, err := s.authorize(ctx, tx, d.IssueID, actor, "delivery.run.mutation", nil); err != nil {
		return err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_revision),0) FROM delivery_events WHERE delivery_id=?`, d.ID).Scan(&revision); err != nil {
		return err
	}
	now := formatTime(s.now())
	hint, err := appendChangeTx(ctx, tx, d, revision, "run", "agent_run", &runID, nil, now)
	if err != nil {
		return err
	}
	effects.add(hint)
	return nil
}

// recordRunLifecycleObservedTx preserves a late or already-sealed linked run's
// lifecycle outcome without reopening canonical stage truth. The envelope key
// is derived from immutable run identity plus observed status, so transport or
// reconciler replay produces neither another audit event nor another wakeup.
func (s *Store) recordRunLifecycleObservedTx(ctx context.Context, tx *sql.Tx, effects *Effects, runID int64, status string, hidden bool) error {
	if runID <= 0 {
		return ErrInvalid
	}
	switch status {
	case "completed", "tests_passed", "tests_failed", "deployed", "failed", "cancelled", "drafted":
	default:
		return fmt.Errorf("%w: run lifecycle observation is not terminal", ErrInvalid)
	}
	var d deliveryRow
	var project sql.NullInt64
	var reporterID int64
	var persistedStatus string
	err := tx.QueryRowContext(ctx, `SELECT d.id,d.issue_id,d.delivery_key,d.project_id_hint,link.reporter_id,ar.status
		FROM delivery_agent_run_links link JOIN deliveries d ON d.id=link.delivery_id
		JOIN agent_runs ar ON ar.id=link.agent_run_id WHERE link.agent_run_id=?`, runID).
		Scan(&d.ID, &d.IssueID, &d.Key, &project, &reporterID, &persistedStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: instrumented run is unlinked", ErrInvariant)
	}
	if err != nil {
		return err
	}
	if persistedStatus != status {
		return fmt.Errorf("%w: lifecycle status mismatch", ErrConflict)
	}
	d.ProjectID = nullInt64Ptr(project)
	actor := Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)}
	if !hidden {
		if _, err := s.authorize(ctx, tx, d.IssueID, actor, "delivery.run.normalize", nil); err != nil {
			return err
		}
	}
	payload := struct {
		RunID  int64  `json:"run_id"`
		Status string `json:"status"`
	}{RunID: runID, Status: status}
	idempotencyKey := fmt.Sprintf("run-lifecycle:%d:%s", runID, status)
	_, err = s.appendEnvelopeTxMode(ctx, tx, effects, d, reporterID, "run_lifecycle_observed", idempotencyKey,
		payload, "run_lifecycle", "", "run", "agent_run", &runID, nil, formatTime(s.now()), !hidden)
	return err
}

// BootstrapRunTx cuts a post-M144 run over atomically. Implementation and
// follow-up runs record the authenticated click as approval of the current
// canonical issue specification before implementation starts. Drafts start a
// specification execution only and are never approval evidence.
func (s *Store) BootstrapRunTx(ctx context.Context, tx *sql.Tx, effects *Effects, req RunBootstrap) (StageRef, error) {
	if req.IssueID <= 0 || req.RunID <= 0 || (req.Mode != "implementation" && req.Mode != "draft") ||
		validateActor(req.Actor) != nil || validatePersistedKey(req.IdempotencyKey, safeOpaqueKey, 80) != nil {
		return StageRef{}, fmt.Errorf("%w: invalid run bootstrap", ErrInvalid)
	}
	var issueID, instrumentation int64
	if err := tx.QueryRowContext(ctx, `SELECT issue_id,delivery_instrumentation_version FROM agent_runs WHERE id=?`, req.RunID).
		Scan(&issueID, &instrumentation); err != nil {
		return StageRef{}, err
	}
	if issueID != req.IssueID || instrumentation != 1 {
		return StageRef{}, fmt.Errorf("%w: post-cutover run marker/link mismatch", ErrInvariant)
	}
	attempt, err := s.EnsureCurrentAttemptTx(ctx, tx, effects, req.IssueID, req.Actor, req.IdempotencyKey+":attempt")
	if err != nil {
		return StageRef{}, err
	}
	runActor := Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", req.RunID)}
	if req.Mode == "draft" {
		stage, err := s.startStageRetryTx(ctx, tx, effects, StageStartRequest{IssueID: req.IssueID,
			AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: runActor,
			ReasonCode: "draft_requested", IdempotencyKey: req.IdempotencyKey + ":spec"}, true)
		if err != nil {
			return StageRef{}, err
		}
		return stage, s.LinkAgentRunTx(ctx, tx, effects, req.IssueID, req.RunID, stage, req.IdempotencyKey+":link")
	}
	if req.Actor.Type != "user" {
		return StageRef{}, fmt.Errorf("%w: implementation bootstrap requires an authenticated user", ErrUnauthorized)
	}
	digest, err := canonicalIssueSpecDigest(ctx, tx, req.IssueID)
	if err != nil {
		return StageRef{}, err
	}
	spec, err := s.StartStageRetryTx(ctx, tx, effects, StageStartRequest{IssueID: req.IssueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: req.Actor,
		ReasonCode: "implementation_approval", IdempotencyKey: req.IdempotencyKey + ":spec"})
	if err != nil {
		return StageRef{}, err
	}
	if _, err := s.ReportStageTx(ctx, tx, effects, StageReport{IssueID: req.IssueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: spec.ExecutionNumber,
		AuthorityEpoch: spec.AuthorityEpoch, Reporter: req.Actor, IdempotencyKey: req.IdempotencyKey + ":approve",
		Kind: "semantic", State: "succeeded", Activity: "Specification approved for implementation",
		Evidence:   []Evidence{{Type: "approval", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: digest}},
		ReasonCode: "human_approval"}); err != nil {
		return StageRef{}, err
	}
	implementation, err := s.startStageRetryTx(ctx, tx, effects, StageStartRequest{IssueID: req.IssueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageImplementation, Reporter: runActor,
		ReasonCode: "implementation_requested", IdempotencyKey: req.IdempotencyKey + ":implementation"}, true)
	if err != nil {
		return StageRef{}, err
	}
	return implementation, s.LinkAgentRunTx(ctx, tx, effects, req.IssueID, req.RunID, implementation, req.IdempotencyKey+":link")
}

func (s *Store) LinkAgentRunTx(ctx context.Context, tx *sql.Tx, effects *Effects, issueID, runID int64, stage StageRef, idempotencyKey string) error {
	if issueID <= 0 || runID <= 0 || validatePersistedKey(idempotencyKey, safeOpaqueKey, 128) != nil {
		return fmt.Errorf("%w: invalid run link", ErrInvalid)
	}
	d, err := loadDeliveryByIssue(ctx, tx, issueID)
	if err != nil {
		return err
	}
	var runIssue, instrumentation int64
	if err := tx.QueryRowContext(ctx, `SELECT issue_id,delivery_instrumentation_version FROM agent_runs WHERE id=?`, runID).
		Scan(&runIssue, &instrumentation); err != nil {
		return err
	}
	if runIssue != issueID || instrumentation != 1 || stage.DeliveryID != d.ID {
		return fmt.Errorf("%w: cross-root or uninstrumented run link", ErrInvariant)
	}
	runActor := Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)}
	if _, err := s.authorize(ctx, tx, issueID, runActor, "delivery.run.link", nil); err != nil {
		return err
	}
	payload := struct {
		RunID, AttemptNumber, ExecutionNumber int64
		StageKey                              string
	}{runID, stage.AttemptNumber, stage.ExecutionNumber, stage.StageKey}
	if prior, duplicateErr := lookupEnvelopeDuplicateForActor(ctx, tx, d, runActor, "run_linked", idempotencyKey, payload); duplicateErr != nil {
		return duplicateErr
	} else if prior.Duplicate {
		var linked int64
		if err := tx.QueryRowContext(ctx, `SELECT agent_run_id FROM delivery_agent_run_links WHERE link_delivery_event_id=?`, prior.ID).Scan(&linked); err != nil {
			return err
		}
		if linked != runID {
			return ErrConflict
		}
		var activation int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_agent_run_activations
			WHERE agent_run_id=? AND attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?`,
			runID, stage.AttemptID, stage.StageKey, stage.ExecutionNumber, stage.AuthorityEpoch).Scan(&activation); err != nil {
			return err
		}
		if activation != 1 {
			return fmt.Errorf("%w: linked run lacks its authority activation", ErrInvariant)
		}
		return nil
	}
	current, err := loadCurrentStage(ctx, tx, d.ID, stage.AttemptNumber, stage.StageKey)
	if err != nil {
		return err
	}
	now := formatTime(s.now())
	reporterID, err := ensureReporterTx(ctx, tx, d.ID, runActor, now)
	if err != nil {
		return err
	}
	if current.ExecutionNumber != stage.ExecutionNumber || current.AuthorityEpoch != stage.AuthorityEpoch || current.ReporterID != reporterID {
		return ErrStaleAuthority
	}
	event, err := s.appendEnvelopeTx(ctx, tx, effects, d, reporterID, "run_linked", idempotencyKey, payload,
		"run_linked", "", "run", "agent_run", &runID, nil, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO delivery_agent_run_links(
		agent_run_id,root_issue_id,delivery_id,attempt_id,stage_key,execution_number,
		execution_start_stage_event_id,reporter_id,link_delivery_event_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, runID, issueID, d.ID, stage.AttemptID, stage.StageKey,
		stage.ExecutionNumber, stage.ExecutionStartEventID, reporterID, event.ID, now)
	if err != nil {
		return err
	}
	var cutoff int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM agent_run_telemetry WHERE run_id=?`, runID).Scan(&cutoff); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO delivery_agent_run_activations(delivery_id,attempt_id,stage_key,
		execution_number,authority_epoch,agent_run_id,reporter_id,authority_stage_event_id,
		telemetry_sequence_cutoff,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, d.ID, stage.AttemptID, stage.StageKey,
		stage.ExecutionNumber, stage.AuthorityEpoch, runID, reporterID, current.AuthorityStageEventID, cutoff, now)
	return err
}

func (s *Store) NormalizeRunTx(ctx context.Context, tx *sql.Tx, effects *Effects, normalization RunNormalization) error {
	if normalization.RunID <= 0 || validatePersistedKey(normalization.IdempotencyKey, safeOpaqueKey, 128) != nil {
		return fmt.Errorf("%w: invalid run normalization", ErrInvalid)
	}
	var issueID, deliveryID, attemptID, currentAttemptID, attemptNumber, execution, currentExecution, epoch, reporterID, currentReporterID, executionStartID int64
	var stage, reporterType, reporterKey, runStatus, commitSHA, commitBase string
	var attachment sql.NullInt64
	var deletedAt sql.NullString
	var runIssueID int64
	var instrumentation int
	err := tx.QueryRowContext(ctx, `SELECT link.root_issue_id,link.delivery_id,link.attempt_id,a.attempt_number,link.stage_key,
		link.execution_number,l.execution_number,l.authority_epoch,link.reporter_id,l.current_reporter_id,
		link.execution_start_stage_event_id,
		(SELECT ca.id FROM delivery_attempts ca JOIN delivery_attempt_policy_seals seal
		 ON seal.delivery_id=ca.delivery_id AND seal.attempt_id=ca.id
		 WHERE ca.delivery_id=link.delivery_id ORDER BY ca.attempt_number DESC LIMIT 1),
		r.reporter_type,r.opaque_key,
		ar.status,ar.commit_sha,ar.commit_base_sha,ar.log_attachment_id,
		ar.issue_id,ar.delivery_instrumentation_version,i.deleted_at
		FROM delivery_agent_run_links link JOIN delivery_attempts a ON a.id=link.attempt_id
		JOIN delivery_stage_latest l ON l.attempt_id=link.attempt_id AND l.stage_key=link.stage_key
		JOIN delivery_reporters r ON r.id=link.reporter_id
		JOIN agent_runs ar ON ar.id=link.agent_run_id
		JOIN issues i ON i.id=link.root_issue_id WHERE link.agent_run_id=?`, normalization.RunID).
		Scan(&issueID, &deliveryID, &attemptID, &attemptNumber, &stage, &execution, &currentExecution, &epoch, &reporterID, &currentReporterID,
			&executionStartID, &currentAttemptID,
			&reporterType, &reporterKey, &runStatus, &commitSHA, &commitBase, &attachment,
			&runIssueID, &instrumentation, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: instrumented run is unlinked", ErrInvariant)
	}
	if err != nil {
		return err
	}
	if normalization.Status != "" && normalization.Status != runStatus {
		return fmt.Errorf("%w: lifecycle status mismatch", ErrConflict)
	}
	if runIssueID != issueID || instrumentation != 1 {
		return fmt.Errorf("%w: run normalization lineage mismatch", ErrInvariant)
	}
	hidden := deletedAt.Valid
	actor := Actor{Type: reporterType, OpaqueKey: reporterKey}
	if !hidden {
		if _, err := s.authorize(ctx, tx, issueID, actor, "delivery.run.normalize", nil); err != nil {
			return err
		}
	}
	// A late old run may close after a retry, handoff, or project-move attempt
	// superseded its authority. Its legal lifecycle must still commit, but it
	// can only invalidate the delivery read—not alter current stage truth.
	currentLineage := attemptID == currentAttemptID && execution == currentExecution && reporterID == currentReporterID
	if currentLineage {
		attempt, loadErr := loadAttemptByNumber(ctx, tx, deliveryID, attemptNumber)
		if loadErr != nil {
			return fmt.Errorf("load run attempt: %w", loadErr)
		}
		currentLineage, loadErr = stageExecutionCurrentLineageMode(ctx, tx, attempt, stage, executionStartID, hidden)
		if loadErr != nil {
			return fmt.Errorf("validate run stage lineage: %w", loadErr)
		}
	}
	if !currentLineage {
		if err := s.recordRunLifecycleObservedTx(ctx, tx, effects, normalization.RunID, runStatus, hidden); err != nil {
			return fmt.Errorf("record superseded run lifecycle: %w", err)
		}
		return nil
	}
	state, activity := "", ""
	var evidence []Evidence
	switch runStatus {
	case "drafted":
		state, activity = "draft_ready", "Implementation draft ready"
	case "tests_failed", "failed":
		state, activity = "failed", "Implementation run failed"
	case "cancelled":
		state, activity = "cancelled", "Implementation run cancelled"
	case "completed", "tests_passed", "deployed":
		state, activity = "succeeded", "Implementation result reported"
		commitSHA = strings.ToLower(strings.TrimSpace(commitSHA))
		commitBase = strings.ToLower(strings.TrimSpace(commitBase))
		if commitDigest.MatchString(commitSHA) && commitSHA != commitBase {
			evidence = append(evidence, Evidence{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: commitSHA})
		} else if attachment.Valid {
			id := attachment.Int64
			evidence = append(evidence, Evidence{Type: "implementation_result", Outcome: "passed", ReferenceKind: "attachment", AttachmentID: &id})
		} else {
			state, activity = "failed", "Implementation result lacked bounded evidence"
		}
	default:
		return nil
	}
	if stage == StageSpecification && runStatus != "drafted" && runStatus != "failed" && runStatus != "cancelled" {
		return fmt.Errorf("%w: specification run has an invalid lifecycle outcome", ErrInvariant)
	}
	if stage != StageImplementation && stage != StageSpecification {
		return fmt.Errorf("%w: run linked to an unsupported lifecycle stage", ErrInvariant)
	}
	id := normalization.RunID
	report := StageReport{IssueID: issueID, AttemptNumber: attemptNumber,
		StageKey: stage, ExecutionNumber: execution, AuthorityEpoch: epoch, Reporter: actor,
		IdempotencyKey: normalization.IdempotencyKey, Kind: "semantic", State: state, Activity: activity,
		Evidence: evidence, ReasonCode: "run_lifecycle"}
	if terminal, _, terminalErr := stageExecutionTerminal(ctx, tx, attemptID, stage, execution); terminalErr != nil {
		return terminalErr
	} else if terminal {
		d, loadErr := loadDeliveryByIssue(ctx, tx, issueID)
		if loadErr != nil {
			return loadErr
		}
		prior, duplicateErr := lookupEnvelopeDuplicateForActor(ctx, tx, d, actor, "run_normalized",
			normalization.IdempotencyKey, canonicalStageReportPayload(report))
		if duplicateErr != nil {
			return duplicateErr
		}
		if prior.Duplicate {
			return nil
		}
		if err := s.recordRunLifecycleObservedTx(ctx, tx, effects, normalization.RunID, runStatus, hidden); err != nil {
			return fmt.Errorf("record sealed run lifecycle: %w", err)
		}
		return nil
	}
	_, err = s.reportStageTxMode(ctx, tx, effects, report, "lifecycle_normalized", "run_normalized", "run", "agent_run", &id, false, hidden)
	if err != nil {
		return fmt.Errorf("normalize run stage: %w", err)
	}
	return nil
}

func loadAttemptByNumber(ctx context.Context, q DBTX, deliveryID, number int64) (Attempt, error) {
	var a Attempt
	var previous, project sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT a.id,a.delivery_id,a.attempt_number,a.plan_revision,a.previous_attempt_id,
		a.project_id_at_start,a.reason_code,a.reason_text,a.created_at FROM delivery_attempts a
		JOIN delivery_attempt_policy_seals seal ON seal.delivery_id=a.delivery_id AND seal.attempt_id=a.id
		WHERE a.delivery_id=? AND a.attempt_number=?`,
		deliveryID, number).Scan(&a.ID, &a.DeliveryID, &a.AttemptNumber, &a.PlanRevision, &previous, &project,
		&a.ReasonCode, &a.ReasonText, &a.CreatedAt)
	if err != nil {
		return Attempt{}, err
	}
	a.PreviousAttemptID, a.ProjectIDAtStart = nullInt64Ptr(previous), nullInt64Ptr(project)
	a.Policies, err = loadPolicies(ctx, q, a.ID)
	return a, err
}

func currentPassedEvidence(ctx context.Context, q DBTX, attemptID int64, stage string) ([]Evidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT e.evidence_type,e.outcome,e.reference_kind,e.reference_value,e.digest_sha256,e.attachment_id
		FROM delivery_stage_latest l JOIN delivery_evidence e ON e.stage_event_id=l.semantic_stage_event_id
		WHERE l.attempt_id=? AND l.stage_key=? AND e.outcome='passed' ORDER BY e.ordinal`, attemptID, stage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		var e Evidence
		var attachment sql.NullInt64
		if err := rows.Scan(&e.Type, &e.Outcome, &e.ReferenceKind, &e.ReferenceValue, &e.DigestSHA256, &attachment); err != nil {
			return nil, err
		}
		e.AttachmentID = nullInt64Ptr(attachment)
		out = append(out, e)
	}
	return out, rows.Err()
}

func canonicalIssueSpecDigest(ctx context.Context, q DBTX, issueID int64) (string, error) {
	var title, description, criteria string
	if err := q.QueryRowContext(ctx, `SELECT title,description,acceptance_criteria FROM issues WHERE id=? AND deleted_at IS NULL`, issueID).
		Scan(&title, &description, &criteria); err != nil {
		return "", err
	}
	return canonicalIssueSpecDigestValues(title, description, criteria), nil
}

func canonicalStoredIssueSpecDigest(ctx context.Context, q DBTX, issueID int64) (string, error) {
	var title, description, criteria string
	if err := q.QueryRowContext(ctx, `SELECT title,description,acceptance_criteria FROM issues WHERE id=?`, issueID).
		Scan(&title, &description, &criteria); err != nil {
		return "", err
	}
	return canonicalIssueSpecDigestValues(title, description, criteria), nil
}

func canonicalIssueSpecDigestValues(title, description, criteria string) string {
	raw := strings.Join([]string{"paimos-issue-spec-v1", title, description, criteria}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

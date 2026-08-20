// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

func (s *Store) ReportStage(ctx context.Context, report StageReport) (StageRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StageRef{}, err
	}
	defer tx.Rollback()
	effects := s.NewEffects()
	ref, err := s.ReportStageTx(ctx, tx, effects, report)
	if err != nil {
		return StageRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return StageRef{}, err
	}
	effects.Dispatch(ctx)
	return ref, nil
}

func (s *Store) ReportStageTx(ctx context.Context, tx *sql.Tx, effects *Effects, report StageReport) (StageRef, error) {
	return s.reportStageTx(ctx, tx, effects, report, "semantic_report", "stage_reported", "stage", "stage_event", nil, true)
}

func (s *Store) reportStageTx(ctx context.Context, tx *sql.Tx, effects *Effects, report StageReport, semanticEventType, envelopeKind, changeKind, sourceKind string, sourceID *int64, requireCurrentAttempt bool) (StageRef, error) {
	return s.reportStageTxMode(ctx, tx, effects, report, semanticEventType, envelopeKind, changeKind,
		sourceKind, sourceID, requireCurrentAttempt, false)
}

func (s *Store) reportStageTxMode(ctx context.Context, tx *sql.Tx, effects *Effects, report StageReport, semanticEventType, envelopeKind, changeKind, sourceKind string, sourceID *int64, requireCurrentAttempt, hidden bool) (StageRef, error) {
	if err := validateStageReport(report); err != nil {
		return StageRef{}, err
	}
	var projectAtCompletion *int64
	if hidden {
		var project sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id=? AND deleted_at IS NOT NULL`, report.IssueID).Scan(&project); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return StageRef{}, ErrNotFound
			}
			return StageRef{}, err
		}
		projectAtCompletion = nullInt64Ptr(project)
	} else {
		var err error
		projectAtCompletion, err = s.authorize(ctx, tx, report.IssueID, report.Reporter, "delivery.stage.report", nil)
		if err != nil {
			return StageRef{}, err
		}
	}
	d, err := loadDeliveryByIssue(ctx, tx, report.IssueID)
	if err != nil {
		return StageRef{}, ErrNotFound
	}
	now := formatTime(s.now())
	reporterID, err := ensureReporterTx(ctx, tx, d.ID, report.Reporter, now)
	if err != nil {
		return StageRef{}, err
	}
	payload := canonicalStageReportPayload(report)
	if report.Kind == "heartbeat" {
		if prior, duplicate, err := lookupHeartbeatDuplicate(ctx, tx, d, report.IdempotencyKey, payload); err != nil {
			return StageRef{}, err
		} else if duplicate {
			return loadStageRefByStageEvent(ctx, tx, prior)
		}
	} else {
		if prior, err := lookupEnvelopeDuplicateForActor(ctx, tx, d, report.Reporter, envelopeKind, report.IdempotencyKey, payload); err != nil {
			return StageRef{}, err
		} else if prior.Duplicate {
			return loadStageRefByDeliveryEvent(ctx, tx, prior.ID)
		}
	}
	if requireCurrentAttempt {
		attempt, err := loadCurrentAttempt(ctx, tx, d.ID)
		if err != nil {
			return StageRef{}, err
		}
		if attempt.AttemptNumber != report.AttemptNumber {
			return StageRef{}, fmt.Errorf("%w: report targets a superseded attempt", ErrConflict)
		}
	}
	current, err := loadCurrentStage(ctx, tx, d.ID, report.AttemptNumber, report.StageKey)
	if err != nil {
		return StageRef{}, ErrConflict
	}
	if current.ExecutionNumber != report.ExecutionNumber || current.AuthorityEpoch != report.AuthorityEpoch || current.ReporterID != reporterID {
		return StageRef{}, ErrStaleAuthority
	}
	if terminal, _, err := stageExecutionTerminal(ctx, tx, current.AttemptID, report.StageKey, report.ExecutionNumber); err != nil {
		return StageRef{}, err
	} else if terminal {
		return StageRef{}, fmt.Errorf("%w: stage execution is terminal; start a retry", ErrConflict)
	}
	if report.SourceSequence != nil {
		var activationCutoff int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(authority_source_sequence_cutoff,0)
			FROM delivery_stage_events WHERE attempt_id=? AND stage_key=? AND execution_number=?
			 AND authority_epoch=? AND reporter_id=? AND event_type IN ('execution_started','handoff')
			 ORDER BY event_sequence DESC LIMIT 1`, current.AttemptID, report.StageKey, report.ExecutionNumber,
			report.AuthorityEpoch, reporterID).Scan(&activationCutoff); err != nil {
			return StageRef{}, fmt.Errorf("%w: authority activation cutoff is missing", ErrInvariant)
		}
		if *report.SourceSequence <= activationCutoff {
			return StageRef{}, fmt.Errorf("%w: report source sequence predates authority activation", ErrConflict)
		}
		var accepted int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(source_sequence),0) FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?`, current.AttemptID,
			report.StageKey, report.ExecutionNumber, report.AuthorityEpoch).Scan(&accepted); err != nil {
			return StageRef{}, err
		}
		if *report.SourceSequence <= accepted {
			return StageRef{}, fmt.Errorf("%w: report source sequence is not increasing", ErrConflict)
		}
	}
	if report.Kind == "estimate" {
		var acceptedRevision int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(estimate_revision),0) FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?`, current.AttemptID,
			report.StageKey, report.ExecutionNumber, report.AuthorityEpoch).Scan(&acceptedRevision); err != nil {
			return StageRef{}, err
		}
		if report.Estimate.Revision == nil || *report.Estimate.Revision < acceptedRevision {
			return StageRef{}, fmt.Errorf("%w: estimate revision is not increasing", ErrConflict)
		}
		if *report.Estimate.Revision == acceptedRevision && acceptedRevision > 0 {
			var prior EstimateEvidence
			var progress sql.NullFloat64
			var eta, etaMin, etaMax sql.NullInt64
			var confidence sql.NullFloat64
			if err := tx.QueryRowContext(ctx, `SELECT estimate_revision,progress_percent,eta_seconds,
				eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis
				FROM delivery_stage_events WHERE attempt_id=? AND stage_key=? AND execution_number=?
				 AND authority_epoch=? AND reporter_id=? AND event_type='estimate'
				ORDER BY event_sequence DESC LIMIT 1`, current.AttemptID, report.StageKey, report.ExecutionNumber,
				report.AuthorityEpoch, reporterID).Scan(&prior.Revision, &progress, &eta, &etaMin, &etaMax,
				&prior.Source, &confidence, &prior.Basis); err != nil {
				return StageRef{}, err
			}
			prior.Progress, prior.ETASeconds, prior.ETAMin, prior.ETAMax = nullFloat64Ptr(progress),
				nullInt64Ptr(eta), nullInt64Ptr(etaMin), nullInt64Ptr(etaMax)
			prior.Confidence = nullFloat64Ptr(confidence)
			if !reflect.DeepEqual(prior, report.Estimate) {
				return StageRef{}, fmt.Errorf("%w: estimate values changed without a new revision", ErrConflict)
			}
		}
	}
	eventType := report.Kind
	if report.Kind == "semantic" {
		eventType = semanticEventType
	}
	nextSequence, err := nextStageEventSequence(ctx, tx, current.AttemptID, report.StageKey, report.ExecutionNumber)
	if err != nil {
		return StageRef{}, err
	}
	reasonCode := report.ReasonCode
	if reasonCode == "" {
		reasonCode = "stage_report"
	}
	event := envelopeResult{}
	heartbeatChange := false
	var heartbeatPayloadHash []byte
	if report.Kind == "heartbeat" {
		heartbeatPayloadHash, err = canonicalHash(payload)
		if err != nil {
			return StageRef{}, err
		}
		heartbeatChange, err = shouldEmitHeartbeatChange(ctx, tx, current, reporterID, now, s.freshness.HeartbeatTimeout)
		if err != nil {
			return StageRef{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_revision),0) FROM delivery_events WHERE delivery_id=?`, d.ID).
			Scan(&event.Revision); err != nil {
			return StageRef{}, err
		}
	} else {
		event, err = s.appendEnvelopeTxMode(ctx, tx, effects, d, reporterID, envelopeKind, report.IdempotencyKey,
			payload, reasonCode, report.ReasonText, changeKind, sourceKind, sourceID, report.SourceSequence, now, !hidden)
		if err != nil {
			return StageRef{}, err
		}
	}

	blockerRows := []blockerFact(nil)
	if report.Kind == "semantic" {
		blockerRows, err = buildBlockerFacts(ctx, tx, current, report.Blockers, now)
		if err != nil {
			return StageRef{}, err
		}
	}
	endedAt := any(nil)
	if report.Kind == "semantic" && (report.State == "succeeded" || report.State == "failed" || report.State == "cancelled" || report.State == "draft_ready") {
		endedAt = now
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_stage_events(
		delivery_id,attempt_id,stage_key,execution_number,event_sequence,authority_epoch,delivery_event_id,
		event_type,reporter_id,execution_start_stage_event_id,previous_stage_event_id,source_sequence,source_idempotency_key,source_payload_hash,
		semantic_state,activity,needs_input,declared_blocker_count,current_blocker_count,declared_evidence_count,
		heartbeat,estimate_revision,progress_percent,eta_seconds,eta_min_seconds,eta_max_seconds,
		estimate_source,estimate_confidence,estimate_basis,spec_revision,reason_code,reason_text,server_received_at,ended_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.ID, current.AttemptID,
		report.StageKey, report.ExecutionNumber, nextSequence, report.AuthorityEpoch, nullableDeliveryEventID(report, event.ID), eventType,
		reporterID, current.ExecutionStartEventID, current.AuthorityStageEventID, report.SourceSequence,
		nullableHeartbeatKey(report), nullableHeartbeatHash(report, heartbeatPayloadHash),
		nullableSemantic(report), report.Activity, boolInt(report.NeedsInput), len(blockerRows), len(report.Blockers),
		len(report.Evidence), boolInt(report.Kind == "heartbeat"), report.Estimate.Revision, report.Estimate.Progress,
		report.Estimate.ETASeconds, report.Estimate.ETAMin, report.Estimate.ETAMax, report.Estimate.Source,
		report.Estimate.Confidence, report.Estimate.Basis, nullableSpecRevision(report, d.SpecRevision), reasonCode, report.ReasonText, now, endedAt)
	if err != nil {
		return StageRef{}, err
	}
	stageEventID, _ := res.LastInsertId()
	if report.Kind == "heartbeat" && heartbeatChange && !hidden {
		hint, err := appendChangeTx(ctx, tx, d, event.Revision, changeKind, sourceKind, &stageEventID, report.SourceSequence, now)
		if err != nil {
			return StageRef{}, err
		}
		effects.add(hint)
	}
	for i, blocker := range blockerRows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_stage_blockers(
			delivery_id,stage_event_id,ordinal,blocker_key,blocker_class,summary,is_current,is_human_wait,
			interval_started_at,interval_ended_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, d.ID, stageEventID, i,
			blocker.Key, blocker.Class, blocker.Summary, boolInt(blocker.Current), boolInt(blocker.HumanWait),
			blocker.StartedAt, now); err != nil {
			return StageRef{}, err
		}
	}
	for i, evidence := range report.Evidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_evidence(
			delivery_id,root_issue_id,stage_event_id,ordinal,evidence_type,outcome,reference_kind,
			reference_value,digest_sha256,attachment_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, d.ID,
			report.IssueID, stageEventID, i, evidence.Type, evidence.Outcome, evidence.ReferenceKind,
			evidence.ReferenceValue, evidence.DigestSHA256, evidence.AttachmentID, now); err != nil {
			return StageRef{}, err
		}
	}
	pointer := ""
	switch report.Kind {
	case "semantic":
		pointer = "semantic_stage_event_id"
	case "heartbeat":
		pointer = "heartbeat_stage_event_id"
	case "estimate":
		pointer = "estimate_stage_event_id"
	}
	// #nosec G202 -- pointer is selected exclusively from the three constants above.
	if _, err := tx.ExecContext(ctx, `UPDATE delivery_stage_latest SET `+pointer+`=?,updated_at=?
		WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=? AND current_reporter_id=?`,
		stageEventID, now, current.AttemptID, report.StageKey, report.ExecutionNumber, report.AuthorityEpoch, reporterID); err != nil {
		return StageRef{}, err
	}
	if report.Kind == "semantic" && report.State == "succeeded" {
		attempt, err := loadAttemptByNumber(ctx, tx, d.ID, report.AttemptNumber)
		if err != nil {
			return StageRef{}, err
		}
		if err := materializeDurationIfEligible(ctx, tx, d, attempt, current, stageEventID, now, projectAtCompletion); err != nil {
			return StageRef{}, err
		}
	}
	return current.StageRef, nil
}

func lookupHeartbeatDuplicate(ctx context.Context, q DBTX, d deliveryRow, key string, payload any) (int64, bool, error) {
	if validatePersistedKey(key, safeOpaqueKey, 128) != nil {
		return 0, false, fmt.Errorf("%w: invalid idempotency key", ErrInvalid)
	}
	hash, err := canonicalHash(payload)
	if err != nil {
		return 0, false, err
	}
	var id int64
	var prior []byte
	err = q.QueryRowContext(ctx, `SELECT id,source_payload_hash FROM delivery_stage_events
		WHERE delivery_id=? AND source_idempotency_key=?`, d.ID, key).Scan(&id, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !bytes.Equal(prior, hash) {
		return 0, false, fmt.Errorf("%w: idempotency key has a different canonical payload", ErrConflict)
	}
	return id, true, nil
}

func shouldEmitHeartbeatChange(ctx context.Context, q DBTX, current currentStage, reporterID int64, now string, timeout time.Duration) (bool, error) {
	var prior string
	err := q.QueryRowContext(ctx, `SELECT server_received_at FROM delivery_stage_events
		WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?
		 AND reporter_id=? AND event_type='heartbeat' ORDER BY event_sequence DESC LIMIT 1`, current.AttemptID,
		current.StageKey, current.ExecutionNumber, current.AuthorityEpoch, reporterID).Scan(&prior)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	priorAt, err := parseStoredTime(prior)
	if err != nil {
		return false, fmt.Errorf("%w: malformed prior heartbeat time", ErrInvariant)
	}
	nowAt, err := parseStoredTime(now)
	if err != nil {
		return false, fmt.Errorf("%w: malformed heartbeat time", ErrInvariant)
	}
	return nowAt.Sub(priorAt) >= timeout, nil
}

type blockerFact struct {
	Blocker
	Current   bool
	StartedAt string
}

func buildBlockerFacts(ctx context.Context, q DBTX, current currentStage, next []Blocker, now string) ([]blockerFact, error) {
	prior := map[string]blockerFact{}
	rows, err := q.QueryContext(ctx, `SELECT b.blocker_key,b.blocker_class,b.summary,b.is_human_wait,b.interval_started_at
		FROM delivery_stage_latest l JOIN delivery_stage_blockers b ON b.stage_event_id=l.semantic_stage_event_id
		WHERE l.attempt_id=? AND l.stage_key=? AND b.is_current=1 ORDER BY b.ordinal`, current.AttemptID, current.StageKey)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b blockerFact
		var human int
		if err := rows.Scan(&b.Key, &b.Class, &b.Summary, &human, &b.StartedAt); err != nil {
			rows.Close()
			return nil, err
		}
		b.HumanWait = human == 1
		prior[b.Key] = b
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]blockerFact, 0, len(next)+len(prior))
	for _, b := range next {
		start := now
		if old, ok := prior[b.Key]; ok {
			if old.Class != b.Class || old.HumanWait != b.HumanWait {
				return nil, fmt.Errorf("%w: unresolved blocker classification is immutable", ErrConflict)
			}
			start = old.StartedAt
		}
		out = append(out, blockerFact{Blocker: b, Current: true, StartedAt: start})
		seen[b.Key] = true
	}
	keys := make([]string, 0, len(prior))
	for key := range prior {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		b := prior[key]
		b.Current = false
		out = append(out, b)
	}
	if len(out) > maxChildFacts {
		return nil, fmt.Errorf("%w: blocker snapshot plus resolutions exceeds %d", ErrInvalid, maxChildFacts)
	}
	return out, nil
}

func validateStageReport(r StageReport) error {
	if r.IssueID <= 0 || r.AttemptNumber <= 0 || r.ExecutionNumber <= 0 || r.AuthorityEpoch <= 0 ||
		stageOrder(r.StageKey) == 0 || validateActor(r.Reporter) != nil || r.IdempotencyKey == "" {
		return fmt.Errorf("%w: invalid report target", ErrInvalid)
	}
	if r.Kind != "semantic" && r.Kind != "heartbeat" && r.Kind != "estimate" {
		return fmt.Errorf("%w: invalid report kind", ErrInvalid)
	}
	if r.Kind == "estimate" && r.SourceSequence == nil {
		return fmt.Errorf("%w: estimate requires a source sequence", ErrInvalid)
	}
	if r.Reporter.Type == "external" && r.SourceSequence == nil {
		return fmt.Errorf("%w: external reports require a source sequence", ErrInvalid)
	}
	if len(r.Blockers) > maxChildFacts || len(r.Evidence) > maxChildFacts {
		return fmt.Errorf("%w: too many child facts", ErrInvalid)
	}
	if validateBoundedText(r.Activity, maxActivityBytes, true) != nil || validateBoundedText(r.ReasonText, maxReasonBytes, true) != nil ||
		(r.ReasonCode != "" && validatePersistedKey(r.ReasonCode, safeReasonCode, 64) != nil) {
		return fmt.Errorf("%w: invalid report text", ErrInvalid)
	}
	if r.Kind == "semantic" {
		valid := map[string]bool{"pending": true, "active": true, "waiting": true, "succeeded": true, "failed": true, "cancelled": true, "draft_ready": true, "unknown": true}
		if !valid[r.State] {
			return fmt.Errorf("%w: invalid semantic state", ErrInvalid)
		}
	} else if r.State != "" || r.Activity != "" || len(r.Blockers) != 0 || len(r.Evidence) != 0 || r.NeedsInput {
		return fmt.Errorf("%w: liveness and estimate reports cannot carry semantic facts", ErrInvalid)
	}
	seenBlocker := map[string]bool{}
	hasHumanWait := false
	for _, b := range r.Blockers {
		if validatePersistedKey(b.Key, safeOpaqueKey, 96) != nil || seenBlocker[b.Key] ||
			!map[string]bool{"input": true, "dependency": true, "permission": true, "environment": true, "external": true, "unknown": true}[b.Class] ||
			validateBoundedText(b.Summary, maxReasonBytes, true) != nil {
			return fmt.Errorf("%w: invalid blocker", ErrInvalid)
		}
		seenBlocker[b.Key] = true
		hasHumanWait = hasHumanWait || b.HumanWait
	}
	if r.NeedsInput && !hasHumanWait {
		return fmt.Errorf("%w: needs_input requires a current human-wait blocker", ErrInvalid)
	}
	passedEvidence, failedEvidence := false, false
	for _, e := range r.Evidence {
		if !evidenceAllowed(r.StageKey, e.Type) || !map[string]bool{"passed": true, "failed": true, "unknown": true}[e.Outcome] ||
			!map[string]bool{"digest": true, "commit": true, "attachment": true, "external_ref": true, "none": true}[e.ReferenceKind] {
			return fmt.Errorf("%w: invalid evidence type", ErrInvalid)
		}
		if e.ReferenceValue != "" && validateReferenceValue(e.ReferenceValue, 192) != nil {
			return fmt.Errorf("%w: unsafe evidence reference", ErrInvalid)
		}
		if e.DigestSHA256 != "" && !hexDigest.MatchString(e.DigestSHA256) {
			return fmt.Errorf("%w: invalid evidence digest", ErrInvalid)
		}
		if e.ReferenceKind == "attachment" {
			if e.AttachmentID == nil || e.ReferenceValue != "" {
				return fmt.Errorf("%w: malformed attachment evidence", ErrInvalid)
			}
		} else if e.AttachmentID != nil {
			return fmt.Errorf("%w: attachment id on non-attachment evidence", ErrInvalid)
		}
		if e.ReferenceKind == "commit" && !commitDigest.MatchString(strings.ToLower(e.ReferenceValue)) {
			return fmt.Errorf("%w: malformed commit evidence", ErrInvalid)
		}
		if e.ReferenceKind == "digest" && e.DigestSHA256 == "" {
			return fmt.Errorf("%w: digest evidence lacks a digest", ErrInvalid)
		}
		if e.ReferenceKind == "none" && (e.ReferenceValue != "" || e.DigestSHA256 != "") {
			return fmt.Errorf("%w: none evidence carries a reference", ErrInvalid)
		}
		if e.ReferenceKind == "external_ref" && e.ReferenceValue == "" {
			return fmt.Errorf("%w: external evidence lacks a reference", ErrInvalid)
		}
		if e.Outcome == "passed" && e.ReferenceKind == "none" {
			return fmt.Errorf("%w: passed evidence must be proof-bearing", ErrInvalid)
		}
		passedEvidence = passedEvidence || e.Outcome == "passed"
		failedEvidence = failedEvidence || e.Outcome == "failed"
	}
	if passedEvidence && failedEvidence {
		return fmt.Errorf("%w: terminal evidence cannot be both passed and failed", ErrInvalid)
	}
	if r.Kind == "semantic" {
		switch r.State {
		case "succeeded":
			if !passedEvidence || failedEvidence {
				return fmt.Errorf("%w: succeeded requires passed terminal evidence", ErrInvalid)
			}
		case "failed":
			if passedEvidence {
				return fmt.Errorf("%w: failed cannot carry passed terminal evidence", ErrInvalid)
			}
		default:
			if passedEvidence {
				return fmt.Errorf("%w: non-success state cannot carry passed terminal evidence", ErrInvalid)
			}
		}
	}
	if err := validateEstimate(r.Kind, r.Estimate); err != nil {
		return err
	}
	return nil
}

func validateEstimate(kind string, e EstimateEvidence) error {
	if kind != "estimate" {
		if e.Revision != nil || e.Progress != nil || e.ETASeconds != nil || e.ETAMin != nil || e.ETAMax != nil || e.Source != "" || e.Confidence != nil || e.Basis != "" {
			return fmt.Errorf("%w: estimate fields require estimate kind", ErrInvalid)
		}
		return nil
	}
	if e.Revision == nil || *e.Revision <= 0 || !map[string]bool{"agent": true, "adapter": true, "provider": true, "tool": true, "external": true}[e.Source] ||
		e.Confidence == nil || *e.Confidence <= 0 || *e.Confidence > 1 ||
		validateBoundedText(e.Basis, maxBasisBytes, false) != nil || (e.Progress == nil && e.ETAMin == nil) {
		return fmt.Errorf("%w: invalid estimate evidence", ErrInvalid)
	}
	if e.Progress != nil && (*e.Progress < 0 || *e.Progress > 100) {
		return fmt.Errorf("%w: estimate value out of range", ErrInvalid)
	}
	for _, value := range []*int64{e.ETASeconds, e.ETAMin, e.ETAMax} {
		if value != nil && (*value < 0 || *value > 365*24*60*60) {
			return fmt.Errorf("%w: estimate duration out of range", ErrInvalid)
		}
	}
	anyETA := e.ETASeconds != nil || e.ETAMin != nil || e.ETAMax != nil
	if anyETA && (e.ETAMin == nil || e.ETAMax == nil) {
		return fmt.Errorf("%w: estimate range must include both bounds", ErrInvalid)
	}
	if e.ETAMin != nil && (*e.ETAMin > *e.ETAMax ||
		(e.ETASeconds != nil && (*e.ETASeconds < *e.ETAMin || *e.ETASeconds > *e.ETAMax))) {
		return fmt.Errorf("%w: invalid estimate range", ErrInvalid)
	}
	return nil
}

func canonicalStageReportPayload(r StageReport) any {
	return struct {
		AttemptNumber, ExecutionNumber, AuthorityEpoch             int64
		StageKey, ReporterType, ReporterKey, Kind, State, Activity string
		NeedsInput                                                 bool
		SourceSequence                                             *int64
		Blockers                                                   []Blocker
		Evidence                                                   []Evidence
		Estimate                                                   EstimateEvidence
		ReasonCode, ReasonText                                     string
	}{r.AttemptNumber, r.ExecutionNumber, r.AuthorityEpoch, r.StageKey, r.Reporter.Type,
		r.Reporter.OpaqueKey, r.Kind, r.State, r.Activity, r.NeedsInput, r.SourceSequence,
		r.Blockers, r.Evidence, r.Estimate, r.ReasonCode, r.ReasonText}
}

func nullableSemantic(r StageReport) any {
	if r.Kind != "semantic" {
		return nil
	}
	return r.State
}

func nullableDeliveryEventID(r StageReport, id int64) any {
	if r.Kind == "heartbeat" {
		return nil
	}
	return id
}

func nullableHeartbeatKey(r StageReport) any {
	if r.Kind != "heartbeat" {
		return nil
	}
	return r.IdempotencyKey
}

func nullableHeartbeatHash(r StageReport, hash []byte) any {
	if r.Kind != "heartbeat" {
		return nil
	}
	return hash
}

func nullableSpecRevision(r StageReport, revision int64) any {
	if r.Kind == "semantic" && r.StageKey == StageSpecification {
		return revision
	}
	return nil
}

func stageExecutionTerminal(ctx context.Context, q DBTX, attemptID int64, stage string, execution int64) (bool, string, error) {
	var state string
	err := q.QueryRowContext(ctx, `SELECT semantic_state FROM delivery_stage_events
		WHERE attempt_id=? AND stage_key=? AND execution_number=?
		  AND event_type IN ('semantic_report','lifecycle_normalized')
		  AND semantic_state IN ('succeeded','failed','cancelled','draft_ready')
		ORDER BY event_sequence DESC LIMIT 1`, attemptID, stage, execution).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	return err == nil, state, err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type durationInterval struct {
	start, end time.Time
	human      bool
}

func materializeDurationIfEligible(ctx context.Context, tx *sql.Tx, d deliveryRow, attempt Attempt, current currentStage, semanticID int64, completedAt string, projectAtCompletion *int64) error {
	eligibleID, eligible, err := eligibleSuccessEventID(ctx, tx, attempt, current.StageKey)
	if err != nil || !eligible || eligibleID != semanticID {
		return err
	}
	var startRaw string
	if err := tx.QueryRowContext(ctx, `SELECT server_received_at FROM delivery_stage_events WHERE id=?`, current.ExecutionStartEventID).Scan(&startRaw); err != nil {
		return err
	}
	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		return fmt.Errorf("%w: malformed execution start", ErrInvariant)
	}
	end, err := time.Parse(time.RFC3339Nano, completedAt)
	if err != nil {
		return fmt.Errorf("%w: malformed completion time", ErrInvariant)
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.interval_started_at,b.interval_ended_at,b.is_human_wait,b.is_current,
		COALESCE((SELECT MIN(later.interval_ended_at) FROM delivery_stage_blockers later
		 JOIN delivery_stage_events later_event ON later_event.id=later.stage_event_id
		 WHERE later_event.attempt_id=se.attempt_id AND later_event.stage_key=se.stage_key
		  AND later_event.execution_number=se.execution_number AND later.blocker_key=b.blocker_key
		  AND later.interval_started_at=b.interval_started_at AND later.is_current=0
		  AND later_event.event_sequence>se.event_sequence),''),
		COALESCE((SELECT MIN(handoff.server_received_at) FROM delivery_stage_events handoff
		 WHERE handoff.attempt_id=se.attempt_id AND handoff.stage_key=se.stage_key
		  AND handoff.execution_number=se.execution_number AND handoff.event_type='handoff'
		  AND handoff.event_sequence>se.event_sequence),'')
		FROM delivery_stage_blockers b JOIN delivery_stage_events se ON se.id=b.stage_event_id
		WHERE se.attempt_id=? AND se.stage_key=? AND se.execution_number=?`, attempt.ID, current.StageKey, current.ExecutionNumber)
	if err != nil {
		return err
	}
	var intervals []durationInterval
	for rows.Next() {
		var a, b, resolvedRaw, handoffRaw string
		var human, isCurrent int
		if err := rows.Scan(&a, &b, &human, &isCurrent, &resolvedRaw, &handoffRaw); err != nil {
			rows.Close()
			return err
		}
		ia, e1 := time.Parse(time.RFC3339Nano, a)
		ib, e2 := time.Parse(time.RFC3339Nano, b)
		if e1 != nil || e2 != nil {
			rows.Close()
			return fmt.Errorf("%w: malformed blocker interval", ErrInvariant)
		}
		if ia.Before(start) {
			ia = start
		}
		if isCurrent == 1 {
			for _, boundaryRaw := range []string{resolvedRaw, handoffRaw} {
				if boundaryRaw == "" {
					continue
				}
				boundary, parseErr := time.Parse(time.RFC3339Nano, boundaryRaw)
				if parseErr != nil {
					rows.Close()
					return fmt.Errorf("%w: malformed blocker closure boundary", ErrInvariant)
				}
				if boundary.After(ia) && (!ib.After(ia) || boundary.Before(ib)) {
					ib = boundary
				}
			}
		}
		if ib.After(end) {
			ib = end
		}
		if ib.After(ia) {
			intervals = append(intervals, durationInterval{ia, ib, human == 1})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	telemetryIntervals, err := loadAgentRunBlockerIntervals(ctx, tx, attempt.ID, current.StageKey,
		current.ExecutionNumber, start, end)
	if err != nil {
		return err
	}
	intervals = append(intervals, telemetryIntervals...)
	totalDuration := end.Sub(start)
	full := int64(totalDuration / time.Second)
	active, blocked, human, err := roundedDurationSeconds(totalDuration, intervals)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO delivery_stage_durations(
		stage_execution_id,terminal_stage_event_id,delivery_id,root_issue_id,attempt_id,stage_key,execution_number,
		project_id_at_completion,estimator_policy_version,full_lead_seconds,active_seconds,
		blocked_seconds,human_wait_seconds,completed_at) VALUES(?,?,?,?,?,?,?,?,1,?,?,?,?,?)`,
		current.ExecutionStartEventID, semanticID, d.ID, d.IssueID, attempt.ID, current.StageKey, current.ExecutionNumber,
		projectAtCompletion, full, active, blocked, human, completedAt)
	return err
}

type telemetryBlockerPoint struct {
	activationEpoch int64
	windowStart     time.Time
	windowEnd       time.Time
	receivedAt      time.Time
	human           bool
	blocked         bool
}

func loadAgentRunBlockerIntervals(ctx context.Context, q DBTX, attemptID int64, stage string, execution int64,
	executionStart, completedAt time.Time) ([]durationInterval, error) {
	rows, err := q.QueryContext(ctx, `SELECT act.authority_epoch,act.created_at,
		COALESCE((SELECT next.server_received_at FROM delivery_stage_events next
		 WHERE next.attempt_id=act.attempt_id AND next.stage_key=act.stage_key
		  AND next.execution_number=act.execution_number AND next.authority_epoch>act.authority_epoch
		  AND next.event_type='handoff' ORDER BY next.authority_epoch LIMIT 1),''),
		t.server_received_at,t.needs_input,t.blocker_state
		FROM delivery_agent_run_activations act JOIN agent_run_telemetry t ON t.run_id=act.agent_run_id
		WHERE act.attempt_id=? AND act.stage_key=? AND act.execution_number=?
		 AND t.sequence>act.telemetry_sequence_cutoff
		 AND (t.kind IN ('phase','needs_input','blocker') OR t.phase<>'unknown' OR
		      t.activity<>'' OR t.needs_input=1 OR t.blocker_state<>'none')
		ORDER BY act.authority_epoch,t.sequence`, attemptID, stage, execution)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []telemetryBlockerPoint
	for rows.Next() {
		var epoch int64
		var startRaw, endRaw, receivedRaw, blocker string
		var needs int
		if err := rows.Scan(&epoch, &startRaw, &endRaw, &receivedRaw, &needs, &blocker); err != nil {
			return nil, err
		}
		windowStart, err := parseStoredTime(startRaw)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed run activation time", ErrInvariant)
		}
		windowEnd := completedAt
		if endRaw != "" {
			windowEnd, err = parseStoredTime(endRaw)
			if err != nil {
				return nil, fmt.Errorf("%w: malformed run deactivation time", ErrInvariant)
			}
		}
		received, err := parseStoredTime(receivedRaw)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed telemetry blocker time", ErrInvariant)
		}
		if windowStart.Before(executionStart) {
			windowStart = executionStart
		}
		if windowEnd.After(completedAt) {
			windowEnd = completedAt
		}
		if received.Before(windowStart) || !received.Before(windowEnd) {
			continue
		}
		human := needs == 1 || blocker == "input"
		points = append(points, telemetryBlockerPoint{activationEpoch: epoch, windowStart: windowStart,
			windowEnd: windowEnd, receivedAt: received, human: human, blocked: human || blocker != "none"})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var intervals []durationInterval
	for i, point := range points {
		if !point.blocked {
			continue
		}
		intervalEnd := point.windowEnd
		if i+1 < len(points) && points[i+1].activationEpoch == point.activationEpoch && points[i+1].receivedAt.Before(intervalEnd) {
			intervalEnd = points[i+1].receivedAt
		}
		if intervalEnd.After(point.receivedAt) {
			intervals = append(intervals, durationInterval{start: point.receivedAt, end: intervalEnd, human: point.human})
		}
	}
	return intervals, nil
}

func disjointIntervalDurations(intervals []durationInterval) (human, blocked time.Duration) {
	if len(intervals) == 0 {
		return 0, 0
	}
	points := make([]time.Time, 0, len(intervals)*2)
	for _, in := range intervals {
		points = append(points, in.start, in.end)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })
	unique := points[:0]
	for _, point := range points {
		if len(unique) == 0 || !point.Equal(unique[len(unique)-1]) {
			unique = append(unique, point)
		}
	}
	for i := 0; i+1 < len(unique); i++ {
		a, b := unique[i], unique[i+1]
		if !b.After(a) {
			continue
		}
		covered, humanCovered := false, false
		for _, in := range intervals {
			if !in.start.After(a) && !in.end.Before(b) {
				covered = true
				humanCovered = humanCovered || in.human
			}
		}
		if !covered {
			continue
		}
		if humanCovered {
			human += b.Sub(a)
		} else {
			blocked += b.Sub(a)
		}
	}
	return human, blocked
}

func roundedDurationSeconds(total time.Duration, intervals []durationInterval) (active, blocked, human int64, err error) {
	if total < 0 {
		return 0, 0, 0, fmt.Errorf("%w: completion predates execution start", ErrInvariant)
	}
	humanDuration, blockedDuration := disjointIntervalDurations(intervals)
	if humanDuration < 0 || blockedDuration < 0 || humanDuration+blockedDuration > total {
		return 0, 0, 0, fmt.Errorf("%w: blocker intervals exceed lead time", ErrInvariant)
	}
	activeDuration := total - humanDuration - blockedDuration
	type component struct {
		value    *int64
		fraction time.Duration
		priority int
	}
	human, blocked, active = int64(humanDuration/time.Second), int64(blockedDuration/time.Second), int64(activeDuration/time.Second)
	components := []component{
		{value: &human, fraction: humanDuration % time.Second, priority: 0},
		{value: &blocked, fraction: blockedDuration % time.Second, priority: 1},
		{value: &active, fraction: activeDuration % time.Second, priority: 2},
	}
	remaining := int(int64(total/time.Second) - human - blocked - active)
	sort.SliceStable(components, func(i, j int) bool {
		if components[i].fraction == components[j].fraction {
			return components[i].priority < components[j].priority
		}
		return components[i].fraction > components[j].fraction
	})
	for i := 0; i < remaining; i++ {
		*components[i].value++
	}
	return active, blocked, human, nil
}

func sanitizeReasonText(v string) string {
	v = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(v, "\r", " "), "\n", " "))
	if len(v) > maxReasonBytes {
		v = v[:maxReasonBytes]
	}
	return v
}

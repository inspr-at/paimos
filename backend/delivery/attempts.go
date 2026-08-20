// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) StartAttempt(ctx context.Context, req AttemptRequest) (Attempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, err
	}
	defer tx.Rollback()
	effects := s.NewEffects()
	attempt, err := s.StartAttemptTx(ctx, tx, effects, req)
	if err != nil {
		return Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, err
	}
	effects.Dispatch(ctx)
	return attempt, nil
}

func (s *Store) StartAttemptTx(ctx context.Context, tx *sql.Tx, effects *Effects, req AttemptRequest) (Attempt, error) {
	if req.IssueID <= 0 || validateActor(req.Actor) != nil {
		return Attempt{}, fmt.Errorf("%w: invalid attempt actor or issue", ErrInvalid)
	}
	if len(req.Policies) == 0 {
		req.Policies = DefaultPolicy()
	}
	req.Policies = sortedPolicies(req.Policies)
	if err := validatePolicy(req.Policies); err != nil {
		return Attempt{}, err
	}
	if validatePersistedKey(req.ReasonCode, safeReasonCode, 64) != nil {
		return Attempt{}, fmt.Errorf("%w: attempt reason code is required", ErrInvalid)
	}
	if err := validateBoundedText(req.ReasonText, maxReasonBytes, true); err != nil {
		return Attempt{}, err
	}
	if req.IdempotencyKey == "" {
		return Attempt{}, fmt.Errorf("%w: idempotency key is required", ErrInvalid)
	}

	// N/A is never accepted from a caller assertion alone. Each override is
	// independently authorized against the issue's current project.
	currentProject, err := s.authorize(ctx, tx, req.IssueID, req.Actor, "delivery.attempt.start", nil)
	if err != nil {
		return Attempt{}, err
	}
	for i := range req.Policies {
		policy := &req.Policies[i]
		if policy.Applicability == "not_applicable" {
			if s.authorizer == nil {
				return Attempt{}, fmt.Errorf("%w: not-applicable policy requires an authorizer", ErrUnauthorized)
			}
			if validateReferenceValue(policy.PolicyReference, 160) != nil || policy.ReasonText == "" {
				return Attempt{}, fmt.Errorf("%w: malformed not-applicable policy evidence", ErrInvalid)
			}
			if _, err := s.authorize(ctx, tx, req.IssueID, req.Actor, "delivery.policy.not_applicable", policy); err != nil {
				return Attempt{}, err
			}
		}
	}

	d, err := s.ensureDeliveryTx(ctx, tx, effects, req.IssueID)
	if err != nil {
		return Attempt{}, err
	}
	now := formatTime(s.now())
	payload := struct {
		ActorType  string   `json:"actor_type"`
		ActorKey   string   `json:"actor_key"`
		Policies   []Policy `json:"policies"`
		ReasonCode string   `json:"reason_code"`
		ReasonText string   `json:"reason_text"`
	}{req.Actor.Type, req.Actor.OpaqueKey, req.Policies, req.ReasonCode, req.ReasonText}
	if prior, err := lookupEnvelopeDuplicateForActor(ctx, tx, d, req.Actor, "attempt_started", req.IdempotencyKey, payload); err != nil {
		return Attempt{}, err
	} else if prior.Duplicate {
		return loadAttemptByStartEvent(ctx, tx, d.ID, prior.ID)
	}
	reporterID, err := ensureReporterTx(ctx, tx, d.ID, req.Actor, now)
	if err != nil {
		return Attempt{}, err
	}

	var nextNumber, nextPlan int64
	var previous sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number),0)+1,COALESCE(MAX(plan_revision),0)+1 FROM delivery_attempts WHERE delivery_id=?`, d.ID).
		Scan(&nextNumber, &nextPlan); err != nil {
		return Attempt{}, err
	}
	_ = tx.QueryRowContext(ctx, `SELECT a.id FROM delivery_attempts a JOIN delivery_attempt_policy_seals seal
		ON seal.delivery_id=a.delivery_id AND seal.attempt_id=a.id
		WHERE a.delivery_id=? ORDER BY a.attempt_number DESC LIMIT 1`, d.ID).Scan(&previous)
	event, err := s.appendEnvelopeTx(ctx, tx, effects, d, reporterID, "attempt_started", req.IdempotencyKey,
		payload, req.ReasonCode, req.ReasonText, "attempt", "delivery_event", nil, nil, now)
	if err != nil {
		return Attempt{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_attempts(
		delivery_id,attempt_number,plan_revision,previous_attempt_id,start_delivery_event_id,
		project_id_at_start,reason_code,reason_text,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		d.ID, nextNumber, nextPlan, nullableNullInt64(previous), event.ID, currentProject,
		req.ReasonCode, req.ReasonText, now)
	if err != nil {
		return Attempt{}, err
	}
	attemptID, _ := res.LastInsertId()
	for i, policy := range req.Policies {
		var authorized any
		if policy.Applicability == "not_applicable" {
			authorized = reporterID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_attempt_stage_policy(
			delivery_id,attempt_id,stage_key,sort_order,applicability,weight,policy_reference,
			reason_code,reason_text,authorized_by_reporter_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			d.ID, attemptID, policy.StageKey, i+1, policy.Applicability, policy.Weight,
			policy.PolicyReference, policy.ReasonCode, policy.ReasonText, authorized, now); err != nil {
			return Attempt{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
		VALUES(?,?,?)`, d.ID, attemptID, now); err != nil {
		return Attempt{}, err
	}
	return Attempt{ID: attemptID, DeliveryID: d.ID, AttemptNumber: nextNumber, PlanRevision: nextPlan,
		PreviousAttemptID: nullInt64Ptr(previous), ProjectIDAtStart: currentProject, ReasonCode: req.ReasonCode,
		ReasonText: req.ReasonText, CreatedAt: now, Policies: append([]Policy(nil), req.Policies...)}, nil
}

func (s *Store) EnsureCurrentAttemptTx(ctx context.Context, tx *sql.Tx, effects *Effects, issueID int64, actor Actor, idempotencyKey string) (Attempt, error) {
	if d, err := loadDeliveryByIssue(ctx, tx, issueID); err == nil {
		if attempt, err := loadCurrentAttempt(ctx, tx, d.ID); err == nil {
			return attempt, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Attempt{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, err
	}
	return s.StartAttemptTx(ctx, tx, effects, AttemptRequest{IssueID: issueID, Actor: actor,
		Policies: DefaultPolicy(), ReasonCode: "instrumentation", IdempotencyKey: idempotencyKey})
}

func loadCurrentAttempt(ctx context.Context, q DBTX, deliveryID int64) (Attempt, error) {
	var a Attempt
	var previous, project sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT a.id,a.delivery_id,a.attempt_number,a.plan_revision,a.previous_attempt_id,
		a.project_id_at_start,a.reason_code,a.reason_text,a.created_at FROM delivery_attempts a
		JOIN delivery_attempt_policy_seals seal ON seal.delivery_id=a.delivery_id AND seal.attempt_id=a.id
		WHERE a.delivery_id=? ORDER BY a.attempt_number DESC LIMIT 1`, deliveryID).
		Scan(&a.ID, &a.DeliveryID, &a.AttemptNumber, &a.PlanRevision, &previous, &project,
			&a.ReasonCode, &a.ReasonText, &a.CreatedAt)
	if err != nil {
		return Attempt{}, err
	}
	a.PreviousAttemptID = nullInt64Ptr(previous)
	a.ProjectIDAtStart = nullInt64Ptr(project)
	a.Policies, err = loadPolicies(ctx, q, a.ID)
	return a, err
}

func loadAttemptByStartEvent(ctx context.Context, q DBTX, deliveryID, eventID int64) (Attempt, error) {
	var a Attempt
	var previous, project sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT a.id,a.delivery_id,a.attempt_number,a.plan_revision,a.previous_attempt_id,
		a.project_id_at_start,a.reason_code,a.reason_text,a.created_at FROM delivery_attempts a
		JOIN delivery_attempt_policy_seals seal ON seal.delivery_id=a.delivery_id AND seal.attempt_id=a.id
		WHERE a.delivery_id=? AND a.start_delivery_event_id=?`, deliveryID, eventID).
		Scan(&a.ID, &a.DeliveryID, &a.AttemptNumber, &a.PlanRevision, &previous, &project,
			&a.ReasonCode, &a.ReasonText, &a.CreatedAt)
	if err != nil {
		return Attempt{}, err
	}
	a.PreviousAttemptID = nullInt64Ptr(previous)
	a.ProjectIDAtStart = nullInt64Ptr(project)
	a.Policies, err = loadPolicies(ctx, q, a.ID)
	return a, err
}

func loadPolicies(ctx context.Context, q DBTX, attemptID int64) ([]Policy, error) {
	rows, err := q.QueryContext(ctx, `SELECT stage_key,applicability,weight,policy_reference,reason_code,reason_text
		FROM delivery_attempt_stage_policy WHERE attempt_id=? ORDER BY sort_order`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		var p Policy
		if err := rows.Scan(&p.StageKey, &p.Applicability, &p.Weight, &p.PolicyReference, &p.ReasonCode, &p.ReasonText); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func nullableNullInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

func nullFloat64Ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	out := v.Float64
	return &out
}

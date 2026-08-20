// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ProjectMoveTx performs the delivery-side authority cutover after the issue
// row has been re-homed in the same transaction. Uninstrumented issues remain
// untouched. Instrumented roots retain old attempts while a new all-required
// target-project attempt immediately becomes current.
func (s *Store) ProjectMoveTx(ctx context.Context, tx *sql.Tx, effects *Effects, issueID, sourceProjectID, targetProjectID int64, actor Actor, idempotencyKey string) error {
	if issueID <= 0 || sourceProjectID <= 0 || targetProjectID <= 0 || sourceProjectID == targetProjectID ||
		validateActor(actor) != nil || validatePersistedKey(idempotencyKey, safeOpaqueKey, 90) != nil {
		return fmt.Errorf("%w: invalid project move", ErrInvalid)
	}
	d, err := loadDeliveryByIssue(ctx, tx, issueID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	projectID, err := s.authorize(ctx, tx, issueID, actor, "delivery.project.move", nil)
	if err != nil {
		return err
	}
	if projectID == nil || *projectID != targetProjectID {
		return fmt.Errorf("%w: issue was not moved to the target project", ErrInvariant)
	}
	now := formatTime(s.now())
	payload := struct {
		SourceProjectID int64  `json:"source_project_id"`
		TargetProjectID int64  `json:"target_project_id"`
		ActorType       string `json:"actor_type"`
		ActorKey        string `json:"actor_key"`
	}{sourceProjectID, targetProjectID, actor.Type, actor.OpaqueKey}
	if prior, err := lookupEnvelopeDuplicateForActor(ctx, tx, d, actor, "project_moved", idempotencyKey, payload); err != nil {
		return err
	} else if prior.Duplicate {
		return nil
	}
	reporterID, err := ensureReporterTx(ctx, tx, d.ID, actor, now)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET project_id_hint=?,updated_at=? WHERE id=?`, targetProjectID, now, d.ID); err != nil {
		return err
	}
	d.ProjectID = &targetProjectID
	if _, err := s.appendEnvelopeTx(ctx, tx, effects, d, reporterID, "project_moved", idempotencyKey,
		payload, "project_move", "", "project_move", "issue", &issueID, nil, now); err != nil {
		return err
	}
	_, err = s.StartAttemptTx(ctx, tx, effects, AttemptRequest{IssueID: issueID, Actor: actor,
		Policies: DefaultPolicy(), ReasonCode: "project_move", ReasonText: "Delivery authority reset after project move",
		IdempotencyKey: idempotencyKey + ":attempt"})
	return err
}

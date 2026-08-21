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
	d.RevokedProjectID = &sourceProjectID
	moveHint, err := loadIssueMoveHint(ctx, tx, d.ID, issueID, sourceProjectID, targetProjectID)
	if err != nil {
		return err
	}
	if _, err := appendEnvelopeWithoutChangeTx(ctx, tx, d, reporterID, "project_moved", idempotencyKey,
		payload, "project_move", "", now); err != nil {
		return err
	}
	effects.add(moveHint)
	_, err = s.StartAttemptTx(ctx, tx, effects, AttemptRequest{IssueID: issueID, Actor: actor,
		Policies: DefaultPolicy(), ReasonCode: "project_move", ReasonText: "Delivery authority reset after project move",
		IdempotencyKey: idempotencyKey + ":attempt"})
	return err
}

func loadIssueMoveHint(ctx context.Context, tx DBTX, deliveryID, issueID, sourceProjectID, targetProjectID int64) (ChangeHint, error) {
	var hint ChangeHint
	var projectID, revokedProjectID, sourceID, sourceSequence sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT change.id,change.cursor_token,change.delivery_id,change.root_issue_id,
		change.delivery_key,change.project_id_hint,change.revoked_project_id,change.change_sequence,
		change.delivery_revision,change.kind,change.source_kind,change.source_id,change.source_sequence,
		change.server_received_at
		FROM delivery_change_log change JOIN deliveries delivery ON delivery.id=change.delivery_id
		WHERE change.delivery_id=? AND change.root_issue_id=? AND change.kind='project_move'
		 AND change.project_id_hint=? AND change.revoked_project_id=?
		 AND change.change_sequence=(SELECT MAX(candidate.change_sequence) FROM delivery_change_log candidate
		  WHERE candidate.delivery_id=delivery.id AND candidate.kind='project_move')
		ORDER BY change.change_sequence DESC LIMIT 1`, deliveryID, issueID, targetProjectID, sourceProjectID).
		Scan(&hint.InternalID, &hint.CursorToken, &hint.DeliveryID, &hint.RootIssueID, &hint.DeliveryKey,
			&projectID, &revokedProjectID, &hint.ChangeSequence, &hint.DeliveryRevision, &hint.Kind,
			&hint.SourceKind, &sourceID, &sourceSequence, &hint.ServerReceivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeHint{}, fmt.Errorf("%w: project move lacks the atomic issue change", ErrInvariant)
	}
	if err != nil {
		return ChangeHint{}, err
	}
	hint.ProjectIDHint = nullInt64Ptr(projectID)
	hint.RevokedProjectID = nullInt64Ptr(revokedProjectID)
	hint.SourceID = nullInt64Ptr(sourceID)
	hint.SourceSequence = nullInt64Ptr(sourceSequence)
	return hint, nil
}

func appendEnvelopeWithoutChangeTx(ctx context.Context, tx DBTX, d deliveryRow, reporterID int64, kind,
	idempotencyKey string, payload any, reasonCode, reasonText, now string,
) (envelopeResult, error) {
	hash, err := canonicalHash(payload)
	if err != nil {
		return envelopeResult{}, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_revision),0)+1
		FROM delivery_events WHERE delivery_id=?`, d.ID).Scan(&revision); err != nil {
		return envelopeResult{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO delivery_events(
		delivery_id,delivery_revision,idempotency_key,payload_hash,kind,reporter_id,reason_code,reason_text,server_received_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, d.ID, revision, idempotencyKey, hash, kind, reporterID, reasonCode, reasonText, now)
	if err != nil {
		return envelopeResult{}, err
	}
	id, _ := result.LastInsertId()
	return envelopeResult{ID: id, Revision: revision}, nil
}

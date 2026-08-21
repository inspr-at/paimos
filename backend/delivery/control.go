// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ControlChangeRequest names only the bounded provenance needed to bridge a
// committed supervisory-control effect onto the PAI-804 invalidation log. The
// command id is deliberately not persisted in that log; M147's append-only
// control_events ledger owns command identity.
type ControlChangeRequest struct {
	IssueID int64
	RunID   int64
	Action  string
}

// RecordControlChangeTx captures the one durable PAI-804 hint produced by an
// issue/run lifecycle mutation, or appends one for effects that do not mutate
// those legacy rows. The returned hint is inert until the caller commits and
// dispatches it.
func (s *Store) RecordControlChangeTx(ctx context.Context, tx *sql.Tx, request ControlChangeRequest) (ChangeHint, error) {
	if s == nil || tx == nil || request.IssueID <= 0 {
		return ChangeHint{}, ErrInvalid
	}
	switch request.Action {
	case "issue.priority.set":
		d, err := loadDeliveryByIssue(ctx, tx, request.IssueID)
		if errors.Is(err, sql.ErrNoRows) {
			return ChangeHint{}, fmt.Errorf("%w: controlled issue has no delivery", ErrInvariant)
		}
		if err != nil {
			return ChangeHint{}, err
		}
		var revision int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_revision),0)
			FROM delivery_events WHERE delivery_id=?`, d.ID).Scan(&revision); err != nil {
			return ChangeHint{}, err
		}
		issueID := request.IssueID
		return appendChangeTx(ctx, tx, d, revision, "issue", "issue", &issueID, nil, formatTime(s.now()))
	case "run.cancel.queued", "run.cancel.running":
		if request.RunID <= 0 {
			return ChangeHint{}, ErrInvalid
		}
		return loadLatestControlHintTx(ctx, tx, request.IssueID, request.RunID, "")
	case "input.respond", "run.pause", "run.resume":
		if request.RunID <= 0 {
			return ChangeHint{}, ErrInvalid
		}
		effects := s.NewEffects()
		if err := s.RecordRunMutationChangeTx(ctx, tx, effects, request.RunID); err != nil {
			return ChangeHint{}, err
		}
		hints := effects.Hints()
		if len(hints) != 1 {
			return ChangeHint{}, fmt.Errorf("%w: control mutation produced %d hints", ErrInvariant, len(hints))
		}
		return hints[0], nil
	default:
		return ChangeHint{}, ErrInvalid
	}
}

func loadLatestControlHintTx(ctx context.Context, tx *sql.Tx, issueID, runID int64, requiredKind string) (ChangeHint, error) {
	var hint ChangeHint
	var project, revoked, sourceID, sourceSequence sql.NullInt64
	query := `SELECT change.id,change.cursor_token,change.delivery_id,change.root_issue_id,change.delivery_key,
		change.project_id_hint,change.revoked_project_id,change.change_sequence,change.delivery_revision,
		change.kind,change.source_kind,change.source_id,change.source_sequence,change.server_received_at
		FROM delivery_change_log change JOIN deliveries delivery ON delivery.id=change.delivery_id
		WHERE change.root_issue_id=?`
	args := []any{issueID}
	if runID > 0 {
		query += ` AND change.source_kind='agent_run' AND change.source_id=?`
		args = append(args, runID)
	}
	if requiredKind != "" {
		query += ` AND change.kind=?`
		args = append(args, requiredKind)
	}
	query += ` ORDER BY change.id DESC LIMIT 1`
	err := tx.QueryRowContext(ctx, query, args...).Scan(&hint.InternalID, &hint.CursorToken, &hint.DeliveryID,
		&hint.RootIssueID, &hint.DeliveryKey, &project, &revoked, &hint.ChangeSequence,
		&hint.DeliveryRevision, &hint.Kind, &hint.SourceKind, &sourceID, &sourceSequence, &hint.ServerReceivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeHint{}, fmt.Errorf("%w: control change hint is missing", ErrInvariant)
	}
	if err != nil {
		return ChangeHint{}, err
	}
	hint.ProjectIDHint, hint.RevokedProjectID = nullInt64Ptr(project), nullInt64Ptr(revoked)
	hint.SourceID, hint.SourceSequence = nullInt64Ptr(sourceID), nullInt64Ptr(sourceSequence)
	return hint, nil
}

// LoadChangeHint reloads an opaque committed row for a post-commit wake. It
// exposes only the existing bounded hint projection, never a control payload.
func (s *Store) LoadChangeHint(ctx context.Context, id int64) (ChangeHint, error) {
	if s == nil || s.db == nil || id <= 0 {
		return ChangeHint{}, ErrInvalid
	}
	var hint ChangeHint
	var project, revoked, sourceID, sourceSequence sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,cursor_token,delivery_id,root_issue_id,delivery_key,
		project_id_hint,revoked_project_id,change_sequence,delivery_revision,kind,source_kind,
		source_id,source_sequence,server_received_at FROM delivery_change_log WHERE id=?`, id).
		Scan(&hint.InternalID, &hint.CursorToken, &hint.DeliveryID, &hint.RootIssueID, &hint.DeliveryKey,
			&project, &revoked, &hint.ChangeSequence, &hint.DeliveryRevision, &hint.Kind, &hint.SourceKind,
			&sourceID, &sourceSequence, &hint.ServerReceivedAt)
	if err != nil {
		return ChangeHint{}, err
	}
	hint.ProjectIDHint, hint.RevokedProjectID = nullInt64Ptr(project), nullInt64Ptr(revoked)
	hint.SourceID, hint.SourceSequence = nullInt64Ptr(sourceID), nullInt64Ptr(sourceSequence)
	return hint, nil
}

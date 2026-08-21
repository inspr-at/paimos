// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RecordExternalDependencyChangeTx appends the single PAI-804 invalidation
// hint for a dependency-only external report. It deliberately does not append
// a delivery_stage_event or move delivery_stage_latest: Janus never owns
// canonical stage truth.
func (s *Store) RecordExternalDependencyChangeTx(ctx context.Context, tx *sql.Tx, issueID, sourceSequence int64) (ChangeHint, error) {
	if s == nil || tx == nil || issueID <= 0 || sourceSequence <= 0 {
		return ChangeHint{}, ErrInvalid
	}
	d, err := loadDeliveryByIssue(ctx, tx, issueID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeHint{}, fmt.Errorf("%w: dependency report has no delivery", ErrInvariant)
	}
	if err != nil {
		return ChangeHint{}, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_revision),0)
		FROM delivery_events WHERE delivery_id=?`, d.ID).Scan(&revision); err != nil {
		return ChangeHint{}, err
	}
	return appendChangeTx(ctx, tx, d, revision, "stage", "system", nil, &sourceSequence, formatTime(s.now()))
}

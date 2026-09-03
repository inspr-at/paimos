// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const DefaultActivityHeartbeatTimeout = 90 * time.Second

// ReconcileStaleActivity turns previously reported activity into unknown when
// its authenticated heartbeat expires. Each candidate uses a revision CAS, so
// concurrent evaluators append at most one timeout transition and can never
// overwrite a heartbeat that won the race.
func (s *Service) ReconcileStaleActivity(ctx context.Context, now time.Time, timeout time.Duration) (int, error) {
	if s == nil || s.db == nil || timeout <= 0 {
		return 0, errors.New("harness activity reconciler configuration is invalid")
	}
	cutoff := now.UTC().Add(-timeout).Format("2006-01-02T15:04:05.000Z")
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM harness_sessions
		WHERE phase<>'stopped' AND heartbeat_at IS NOT NULL AND julianday(heartbeat_at)<=julianday(?)
		AND NOT(activity_state='unknown' AND activity_reason='heartbeat_stale') ORDER BY id`, cutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	updated := 0
	for _, id := range ids {
		changed, err := s.reconcileOneStaleActivity(ctx, id, cutoff)
		if err != nil {
			return updated, err
		}
		if changed {
			updated++
		}
	}
	return updated, nil
}

func (s *Service) reconcileOneStaleActivity(ctx context.Context, id, cutoff string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	current, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) || current.Phase == PhaseStopped || current.HeartbeatAt == "" ||
		(current.ActivityState == ActivityUnknown && current.ActivityReason == ActivityStale) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE harness_sessions
		SET activity_state='unknown',activity_reason='heartbeat_stale',closed_reason='',
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1
		WHERE id=? AND revision=? AND phase<>'stopped' AND heartbeat_at IS NOT NULL
		AND julianday(heartbeat_at)<=julianday(?)
		AND NOT(activity_state='unknown' AND activity_reason='heartbeat_stale')`, id, current.Revision, cutoff)
	if err != nil {
		return false, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return false, nil
	}
	out, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, id))
	if err != nil {
		return false, err
	}
	if err := appendSessionEventTx(ctx, tx, out, "activity_timeout"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"context"
	"database/sql"

	"github.com/inspr-at/paimos/backend/auth"
)

// Reconcile terminalizes time- and runner-lost state in small, deterministic
// batches. Each invocation is one audited transaction; concurrent invocations
// serialize on the conditional updates, so exactly one owns each terminal fact.
func (s *Service) Reconcile(ctx context.Context, principal auth.Principal, request ReconcileRequest) (ReconcileProjection, error) {
	limit := request.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return ReconcileProjection{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	authz, err := s.beginAuthorized(ctx, principal, false, ScopeRunner)
	if err != nil {
		return ReconcileProjection{}, err
	}
	defer authz.tx.Rollback()
	var projection ReconcileProjection

	rows, err := authz.tx.QueryContext(ctx, `SELECT command_id FROM control_commands
		WHERE status='pending_confirmation' AND expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		ORDER BY expires_at,command_id LIMIT ?`, limit)
	if err != nil {
		return projection, storageError(ctx, err)
	}
	commandIDs, err := scanStrings(rows)
	if err != nil {
		return projection, storageError(ctx, err)
	}
	for _, id := range commandIDs {
		result, err := authz.tx.ExecContext(ctx, `UPDATE control_commands SET status='expired',status_revision=status_revision+1,
			safe_reason='confirmation_expired',terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=? AND status='pending_confirmation'
			AND expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, id)
		if err != nil {
			return projection, sqliteConflict(err)
		}
		if changed(result) {
			if err := insertCommandEvent(ctx, authz.tx, id, "command_expired"); err != nil {
				return projection, err
			}
			projection.ExpiredCommands++
		}
	}

	rows, err = authz.tx.QueryContext(ctx, `SELECT grant_id,revision FROM control_capability_grants
		WHERE revoked_at IS NULL AND expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		ORDER BY expires_at,grant_id LIMIT ?`, limit)
	if err != nil {
		return projection, storageError(ctx, err)
	}
	grantRows := make([]struct {
		id       string
		revision int64
	}, 0)
	for rows.Next() {
		var row struct {
			id       string
			revision int64
		}
		if err := rows.Scan(&row.id, &row.revision); err != nil {
			rows.Close()
			return projection, storageError(ctx, err)
		}
		grantRows = append(grantRows, row)
	}
	rows.Close()
	for _, row := range grantRows {
		result, err := authz.tx.ExecContext(ctx, `UPDATE control_capability_grants SET
			revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE grant_id=? AND revision=? AND revoked_at IS NULL
			AND expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, row.id, row.revision)
		if err != nil {
			return projection, sqliteConflict(err)
		}
		if changed(result) {
			if err := insertGrantEvent(ctx, authz.tx, row.id, row.revision, "grant_expired", "capability_expired"); err != nil {
				return projection, err
			}
			projection.ExpiredGrants++
		}
	}

	rows, err = authz.tx.QueryContext(ctx, `SELECT lease_id,revision FROM control_capability_leases
		WHERE revoked_at IS NULL AND expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		ORDER BY expires_at,lease_id LIMIT ?`, limit)
	if err != nil {
		return projection, storageError(ctx, err)
	}
	leaseRows := make([]struct {
		id       string
		revision int64
	}, 0)
	for rows.Next() {
		var row struct {
			id       string
			revision int64
		}
		if err := rows.Scan(&row.id, &row.revision); err != nil {
			rows.Close()
			return projection, storageError(ctx, err)
		}
		leaseRows = append(leaseRows, row)
	}
	rows.Close()
	for _, row := range leaseRows {
		result, err := authz.tx.ExecContext(ctx, `UPDATE control_capability_leases SET
			revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE lease_id=? AND revision=? AND revoked_at IS NULL
			AND expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, row.id, row.revision)
		if err != nil {
			return projection, sqliteConflict(err)
		}
		if changed(result) {
			if err := insertLeaseEvent(ctx, authz.tx, row.id, row.revision, "lease_expired", "lease_expired"); err != nil {
				return projection, err
			}
			projection.ExpiredLeases++
		}
	}

	rows, err = authz.tx.QueryContext(ctx, `SELECT request.request_id,request.revision FROM control_input_requests request
		JOIN control_input_request_states state ON state.request_id=request.request_id
		 AND state.current_revision=request.revision AND state.terminal_event_id IS NULL
		WHERE request.expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		ORDER BY request.expires_at,request.request_id LIMIT ?`, limit)
	if err != nil {
		return projection, storageError(ctx, err)
	}
	inputRows := make([]struct {
		id       string
		revision int64
	}, 0)
	for rows.Next() {
		var row struct {
			id       string
			revision int64
		}
		if err := rows.Scan(&row.id, &row.revision); err != nil {
			rows.Close()
			return projection, storageError(ctx, err)
		}
		inputRows = append(inputRows, row)
	}
	rows.Close()
	for _, row := range inputRows {
		digest := canonicalDigest("input.expired", stringField("request_id", row.id), intField("revision", row.revision))
		result, err := authz.tx.ExecContext(ctx, `INSERT INTO control_input_resolution_events(request_id,
			request_revision,sequence,event_kind,event_digest,safe_reason)
			SELECT ?,?,?,'expired',?,'input_expired' WHERE EXISTS(SELECT 1 FROM control_input_request_states
			WHERE request_id=? AND current_revision=? AND terminal_event_id IS NULL)`, row.id, row.revision,
			row.revision, digest[:], row.id, row.revision)
		if err != nil {
			return projection, sqliteConflict(err)
		}
		if !changed(result) {
			continue
		}
		terminalID, err := result.LastInsertId()
		if err != nil {
			return projection, storageError(ctx, err)
		}
		if _, err := authz.tx.ExecContext(ctx, `UPDATE control_input_request_states SET
			state_revision=state_revision+1,terminal_event_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE request_id=? AND current_revision=? AND terminal_event_id IS NULL`, terminalID, row.id, row.revision); err != nil {
			return projection, sqliteConflict(err)
		}
		if err := insertInputEvent(ctx, authz.tx, row.id, row.revision, "input_expired"); err != nil {
			return projection, err
		}
		projection.ExpiredInputs++
	}

	if err := s.reconcileOutboxTx(ctx, authz.tx, limit, &projection); err != nil {
		return projection, err
	}
	if err := commitWithWake(ctx, authz, nil, nil); err != nil {
		return projection, err
	}
	return projection, nil
}

func (s *Service) reconcileOutboxTx(ctx context.Context, tx *sql.Tx, limit int, projection *ReconcileProjection) error {
	rows, err := tx.QueryContext(ctx, `SELECT outbox.command_id,outbox.delivery_state,
		CASE WHEN outbox.delivery_state='claimed' THEN 'runner_lost'
		 WHEN command.grant_expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN 'capability_expired'
		 ELSE 'lease_expired' END
		FROM control_outbox outbox JOIN control_commands command ON command.command_id=outbox.command_id
		WHERE (outbox.delivery_state='queued' AND (command.grant_expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 OR command.lease_expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now')))
		 OR (outbox.delivery_state='claimed' AND outbox.safe_reason IS NULL AND command.outcome IS NULL
		 AND outbox.claimed_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now','-90 seconds'))
		ORDER BY outbox.id LIMIT ?`, limit)
	if err != nil {
		return storageError(ctx, err)
	}
	type candidate struct{ id, state, reason string }
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.state, &item.reason); err != nil {
			rows.Close()
			return storageError(ctx, err)
		}
		candidates = append(candidates, item)
	}
	rows.Close()
	for _, item := range candidates {
		if item.state == "claimed" {
			if _, err := tx.ExecContext(ctx, `UPDATE control_commands SET status_revision=status_revision+1,
				outcome='outcome_unknown',safe_reason='runner_lost',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
				WHERE command_id=? AND status='accepted' AND outcome IS NULL`, item.id); err != nil {
				return sqliteConflict(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE control_outbox SET safe_reason='runner_lost',
				updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=? AND delivery_state='claimed'
				AND safe_reason IS NULL`, item.id); err != nil {
				return sqliteConflict(err)
			}
			if err := insertCommandEvent(ctx, tx, item.id, "effect_outcome_unknown"); err != nil {
				return err
			}
			projection.UnknownOutcomes++
			continue
		}
		digest := canonicalDigest("effect.abandoned", stringField("command_id", item.id), stringField("reason", item.reason))
		if _, err := tx.ExecContext(ctx, `UPDATE control_commands SET status='rejected',status_revision=status_revision+1,
			outcome='rejected',safe_reason=?,result_digest=?,terminal_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE command_id=? AND status='accepted'`,
			item.reason, digest[:], item.id); err != nil {
			return sqliteConflict(err)
		}
		if err := insertCommandEvent(ctx, tx, item.id, "command_rejected"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE control_outbox SET delivery_state='abandoned',safe_reason=?,
			acknowledged_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE command_id=? AND delivery_state='queued'`, item.reason, item.id); err != nil {
			return sqliteConflict(err)
		}
		if err := insertCommandEvent(ctx, tx, item.id, "effect_abandoned"); err != nil {
			return err
		}
		projection.AbandonedEffects++
	}
	return nil
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func changed(result sql.Result) bool {
	count, err := result.RowsAffected()
	return err == nil && count == 1
}

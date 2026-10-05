// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// UndoHandlers is passed by the coordinator to events.WithUndoHandlers.
// AEON-291: a one-click removal from Agents answers its event_id so the undo
// toast can reverse it through POST /api/events/{id}/undo.
func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{"harness.removed": undoRemoval, "harness.reparented": undoReparent}
}

// undoRemoval puts the record back exactly as it was before removal: the same
// phase, stop time and reason, and no tombstone, so the worker's lease is proof
// again. Leases released and reply obligations closed by the removal stay
// closed. A removed reference is revoked, so no newer generation can hold it.
// A silent unmanaged session that comes back live is swept again as lost.
func undoRemoval(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if p.Kind != tenant.Person {
		return events.Change{}, events.ErrForbidden
	}
	var previous Session
	var removed struct {
		Session Session `json:"session"`
	}
	if len(e.Before) == 0 || json.Unmarshal(e.Before, &previous) != nil || json.Unmarshal(e.After, &removed) != nil ||
		!workorders.UUID(previous.ID) || previous.ID != removed.Session.ID || !workorders.UUID(previous.ProjectID) {
		return events.Change{}, events.ErrConflict
	}
	if err := db.LockWorkTreeTx(ctx, tx); err != nil {
		return events.Change{}, err
	}
	err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{ProjectID: previous.ProjectID})
	if errors.Is(err, authz.ErrForbidden) {
		return events.Change{}, events.ErrForbidden
	}
	if err != nil {
		return events.Change{}, err
	}
	current, err := load(ctx, tx, previous.ProjectID, previous.ID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Change{}, events.ErrNotFound
	}
	if err != nil {
		return events.Change{}, err
	}
	if current.ArchivedAt == nil || current.Revision != removed.Session.Revision {
		return events.Change{}, events.ErrConflict
	}
	restored, err := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET archived_at=NULL,recovery_process_state=NULL,recovery_request_id=NULL,
		recovery_request_digest=NULL,recovery_actor_id=NULL,recovery_reason=NULL,phase=$2,stopped_at=$3,stop_reason=$4,revision=revision+1
		WHERE id=$1 RETURNING `+sessionColumns, current.ID, previous.Phase, previous.StoppedAt, previous.StopReason))
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: &restored.ProjectID, Type: "harness.restored", Before: current, After: restored}, nil
}

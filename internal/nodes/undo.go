// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// UndoHandlers lets the event module reverse a move while retaining both key
// histories, and a bulk change as a whole. A later edit or move closes the
// stale undo path.
func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{evNodeMoved: undoMove, evNodeProjectMoved: undoProjectMove, evNodeBulkChanged: undoBulk, evNodeKindChanged: undoKindChange}
}

func undoMove(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	var before, after movedNodeSnapshot
	if e.NodeID == nil || json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil ||
		before.ID != *e.NodeID || after.ID != *e.NodeID || before.ID != after.ID {
		return events.Change{}, events.ErrConflict
	}
	if len(before.JourneyTickets) != len(after.JourneyTickets) {
		return events.Change{}, events.ErrConflict
	}
	if err := lockTree(ctx, tx); err != nil {
		return events.Change{}, err
	}
	restored, err := restoreMovedNode(ctx, tx, p, before.nodeJSON, after.nodeJSON)
	if err != nil {
		return events.Change{}, err
	}
	if len(before.ShipsIn) > 0 {
		restored.Warnings = append(restored.Warnings, "Undo returns released-model backlog items to their original project's unranked tail.")
	}
	for i, ticket := range before.JourneyTickets {
		if ticket.TicketID != after.JourneyTickets[i].TicketID {
			return events.Change{}, events.ErrConflict
		}
		// A descendant may have been moved out of the subtree since this
		// event. Restoring its old projection would then describe the wrong
		// project even though the ancestor itself is still undoable.
		var projectID *string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1::uuid AND deleted_at IS NULL`, ticket.TicketID).Scan(&projectID); err != nil {
			return events.Change{}, events.ErrConflict
		}
		if ticket.Membership == nil || deref(projectID) != ticket.Membership.ProjectID {
			return events.Change{}, events.ErrConflict
		}
		if err := restoreJourneyMembership(ctx, tx, p, ticket.TicketID, ticket.Membership, after.JourneyTickets[i].Membership); err != nil {
			return events.Change{}, err
		}
	}
	if len(before.JourneyTickets) == 0 {
		return events.Change{NodeID: e.NodeID, Type: evNodeMoved, Before: after.nodeJSON, After: restored}, nil
	}
	return events.Change{NodeID: e.NodeID, Type: evNodeMoved, Before: after,
		After: movedNodeSnapshot{nodeJSON: restored, JourneyTickets: before.JourneyTickets}}, nil
}

func undoProjectMove(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	var before, after projectMoveSnapshot
	if e.NodeID == nil || json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil ||
		before.Node.ID != *e.NodeID || after.Node.ID != *e.NodeID || before.Node.ID != after.Node.ID {
		return events.Change{}, events.ErrConflict
	}
	if err := lockTree(ctx, tx); err != nil {
		return events.Change{}, err
	}
	restored, err := restoreMovedNode(ctx, tx, p, before.Node, after.Node)
	if err != nil {
		return events.Change{}, err
	}
	if len(before.ShipsIn) > 0 {
		restored.Warnings = append(restored.Warnings, "Undo returns released-model backlog items to their original project's unranked tail.")
	}
	if err := restoreJourneyMembership(ctx, tx, p, after.Node.ID, before.Journey, after.Journey); err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: e.NodeID, Type: evNodeProjectMoved,
		Before: projectMoveSnapshot{Node: after.Node, Journey: after.Journey},
		After:  projectMoveSnapshot{Node: restored, Journey: before.Journey}}, nil
}

// restoreMovedNode moves a node back to its former parent. Like a move, the
// undo needs nodes.move in every project it changes: where the node is now and
// where it returns to (ADR-003 P2). An event's author or a project's admin
// cannot reverse a move into a project where they hold less.
func restoreMovedNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, before, after nodeJSON) (nodeJSON, error) {
	if err := armPortalModeration(ctx, tx, p); err != nil {
		return nodeJSON{}, err
	}
	if err := refuseReleaseNode(ctx, tx, after.ID); err != nil {
		return nodeJSON{}, events.ErrConflict
	}
	current, err := loadNode(ctx, tx, after.ID, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nodeJSON{}, events.ErrConflict
		}
		return nodeJSON{}, err
	}
	if err := requireMove(ctx, tx, p, after.ID, before.ParentID); err != nil {
		if errors.Is(err, errMoveForbidden) {
			return nodeJSON{}, events.ErrForbidden
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nodeJSON{}, events.ErrConflict
		}
		return nodeJSON{}, err
	}
	if current.Key != after.Key || current.Position != after.Position || !sameString(current.ParentID, after.ParentID) ||
		!current.UpdatedAt.Equal(after.UpdatedAt) {
		return nodeJSON{}, events.ErrConflict
	}
	shipsBefore, err := shipsInBeforeMove(ctx, tx, after.ID, before.ParentID)
	if err != nil {
		return nodeJSON{}, events.ErrConflict
	}
	if before.Key != after.Key {
		command, err := tx.Exec(ctx, `DELETE FROM node_key_aliases WHERE key=$1 AND node_id=$2::uuid`, before.Key, after.ID)
		if err != nil || command.RowsAffected() != 1 {
			return nodeJSON{}, events.ErrConflict
		}
	}
	restored, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET key=$1,parent_id=$2::uuid,position=$3::numeric,
	 updated_at=greatest(clock_timestamp(),date_trunc('second',updated_at)+interval '1 second')
	 WHERE id=$4::uuid RETURNING `+nodeReturning, before.Key, before.ParentID, before.Position, before.ID))
	if err != nil {
		return nodeJSON{}, undoWriteErr(err)
	}
	if before.Key != after.Key {
		if _, err := tx.Exec(ctx, `INSERT INTO node_key_aliases(tenant_id,key,node_id) VALUES($1::uuid,$2,$3::uuid)`, p.TenantID, after.Key, after.ID); err != nil {
			return nodeJSON{}, events.ErrConflict
		}
	}
	if len(shipsBefore) > 0 {
		restored.Warnings = append(restored.Warnings, "Backlog rank was not restored; the item returns to its original project tail.")
	}
	return restored, nil
}

// undoWriteErr keeps a stale undo as a conflict and a portal-catalog refusal
// as forbidden, so the events API does not report that refusal as 409.
func undoWriteErr(err error) error {
	if events.PortalCatalogDenied(err) {
		return events.ErrForbidden
	}
	return events.ErrConflict
}

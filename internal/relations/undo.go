// SPDX-License-Identifier: AGPL-3.0-only

package relations

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// UndoOption registers the reversible relation operations on events.New.
func UndoOption() events.Option {
	return events.WithUndoHandlers(map[string]events.UndoFunc{
		"relation.created": undoCreated,
		"relation.deleted": undoDeleted,
	})
}

func decodeSnapshot(b json.RawMessage) (Relation, error) {
	var v Relation
	if json.Unmarshal(b, &v) != nil {
		return v, events.ErrConflict
	}
	id, idOK := uuid(v.ID)
	source, sourceOK := uuid(v.SourceNodeID)
	target, targetOK := uuid(v.TargetNodeID)
	if !idOK || !sourceOK || !targetOK || source == target || !validType(v.Type) || v.CreatedAt.IsZero() || (v.Type == "relates" && source > target) {
		return v, events.ErrConflict
	}
	v.ID, v.SourceNodeID, v.TargetNodeID = id, source, target
	return v, nil
}

func undoCreated(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	original, err := decodeSnapshot(e.After)
	if err != nil {
		return events.Change{}, err
	}
	if err := lockGraph(ctx, tx, p.TenantID, original.Type); err != nil {
		return events.Change{}, err
	}
	current, err := scanRelation(tx.QueryRow(ctx, `SELECT id::text,source_node_id::text,target_node_id::text,type,created_at
  FROM node_relations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, original.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Change{}, events.ErrConflict
	}
	if err != nil {
		return events.Change{}, err
	}
	if current.SourceNodeID != original.SourceNodeID || current.TargetNodeID != original.TargetNodeID || current.Type != original.Type || !current.CreatedAt.Equal(original.CreatedAt) {
		return events.Change{}, events.ErrConflict
	}
	// Undoing a link unlinks: relations.delete in both items' projects, as
	// DELETE /api/relations/{id} needs (ADR-003 P2).
	if err := requireOnEnds(ctx, tx, p, "relations.delete", current.SourceNodeID, current.TargetNodeID); err != nil {
		return events.Change{}, undoAuthError(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM node_relations WHERE tenant_id=$1 AND id=$2`, p.TenantID, current.ID); err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: &current.SourceNodeID, Type: "relation.undone", Before: current}, nil
}

func undoDeleted(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	original, err := decodeSnapshot(e.Before)
	if err != nil {
		return events.Change{}, err
	}
	if err := lockGraph(ctx, tx, p.TenantID, original.Type); err != nil {
		return events.Change{}, err
	}
	if err := lockNodes(ctx, tx, p, original.SourceNodeID, original.TargetNodeID); err != nil {
		if errors.Is(err, events.ErrNotFound) {
			return events.Change{}, events.ErrConflict
		}
		return events.Change{}, err
	}
	// Undoing an unlink links again: relations.write in both items' projects,
	// as POST /api/relations needs (ADR-003 P2).
	if err := requireOnEnds(ctx, tx, p, "relations.write", original.SourceNodeID, original.TargetNodeID); err != nil {
		return events.Change{}, undoAuthError(err)
	}
	if err := enforceGraph(ctx, tx, p.TenantID, original.SourceNodeID, original.TargetNodeID, original.Type); err != nil {
		if errors.Is(err, events.ErrNotFound) || errors.Is(err, errGraph) {
			return events.Change{}, events.ErrConflict
		}
		return events.Change{}, err
	}
	// A link added since the deletion may make the restored one close a loop,
	// or occupy its tuple; both leave the deletion standing.
	if err := refuse(ctx, tx, p.TenantID, original.SourceNodeID, original.TargetNodeID, original.Type); err != nil {
		if errors.As(err, new(refusal)) {
			return events.Change{}, events.ErrConflict
		}
		return events.Change{}, err
	}
	restored, err := scanRelation(tx.QueryRow(ctx, `INSERT INTO node_relations(tenant_id,id,source_node_id,target_node_id,type,created_at)
  VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text,source_node_id::text,target_node_id::text,type,created_at`,
		p.TenantID, original.ID, original.SourceNodeID, original.TargetNodeID, original.Type, original.CreatedAt))
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: &restored.SourceNodeID, Type: "relation.undone", After: restored}, nil
}

func undoAuthError(err error) error {
	switch {
	case errors.Is(err, errForbiddenRelation):
		return events.ErrForbidden
	case errors.Is(err, pgx.ErrNoRows):
		return events.ErrConflict
	}
	return err
}

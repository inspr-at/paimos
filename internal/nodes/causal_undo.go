// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// CausalUndoHandlers reverses a leaf edit, creation or deletion only through
// the event module's confirmed parent-cause flow. Existing bulk/move handlers
// retain their own stronger key, journey and whole-batch checks.
func CausalUndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{evNodeUpdated: undoWorkChild, evNodeCreated: undoWorkChild, evNodeDeleted: undoWorkChild,
		"import.node_updated": undoWorkChild, "import.node_created": undoWorkChild}
}
func undoWorkChild(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	switch e.Type {
	case "import.node_updated":
		e.Type = evNodeUpdated
	case "import.node_created":
		e.Type = evNodeCreated
	}
	var before, after nodeJSON
	if e.NodeID == nil || json.Unmarshal(e.After, &after) != nil || after.ID != *e.NodeID {
		return events.Change{}, events.ErrConflict
	}
	if e.Type != evNodeCreated && (json.Unmarshal(e.Before, &before) != nil || before.ID != after.ID) {
		return events.Change{}, events.ErrConflict
	}
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return events.Change{}, err
	}
	current, err := scanNode(tx.QueryRow(ctx, `SELECT `+nodeReturning+` FROM nodes WHERE id=$1::uuid FOR NO KEY UPDATE`, after.ID))
	if err != nil {
		return events.Change{}, events.ErrConflict
	}
	var project *string
	if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1::uuid`, after.ID).Scan(&project); err != nil {
		return events.Change{}, err
	}
	scope := authz.Scope{}
	if project != nil {
		scope.ProjectID = *project
	}
	permission := "nodes.write"
	if e.Type == evNodeCreated {
		permission = "nodes.delete"
	}
	if authz.RequireTx(ctx, tx, p, permission, scope) != nil {
		return events.Change{}, events.ErrForbidden
	}
	if !current.UpdatedAt.Equal(after.UpdatedAt) || current.State != after.State || !sameString(current.ParentID, after.ParentID) || current.Key != after.Key || current.KindID != after.KindID || !sameTime(current.DeletedAt, after.DeletedAt) {
		return events.Change{}, events.ErrConflict
	}
	kind, schema, err := loadKind(ctx, tx, current.KindID)
	if err != nil {
		return events.Change{}, err
	}
	if kind.Slug != "work" {
		return events.Change{}, events.ErrConflict
	}
	if e.Type == evNodeCreated {
		// Deletion's normal tree trigger refuses live children; active work is
		// refused here rather than bypassed by this compensating operation.
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE ticket_node_id=$1 AND stopped_at IS NULL)
   OR EXISTS(SELECT 1 FROM agent_runs WHERE queue_node_id=$1 AND status IN ('queued','starting','running','waiting'))
 OR EXISTS(SELECT 1 FROM work_orders w JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id WHERE n.parent_id=$1 AND w.status='running')`, current.ID).Scan(&busy); err != nil {
			return events.Change{}, err
		}
		if busy {
			return events.Change{}, events.ErrConflict
		}
		restored, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1::uuid RETURNING `+nodeReturning, current.ID))
		if err != nil {
			return events.Change{}, undoWriteErr(err)
		}
		return events.Change{NodeID: e.NodeID, Type: evNodeDeleted, Before: current, After: restored}, nil
	}
	if before.KindID != current.KindID || before.Key != current.Key || !sameString(before.ParentID, current.ParentID) {
		return events.Change{}, events.ErrConflict
	}
	fields, err := validateFields(schema, before.Fields)
	if err != nil {
		return events.Change{}, events.ErrConflict
	}
	restored, err := scanNode(tx.QueryRow(ctx, `UPDATE nodes SET title=$2,body=$3,fields=$4::jsonb,state=$5,human_check=$6,deleted_at=$7,
 updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1::uuid RETURNING `+nodeReturning,
		current.ID, before.Title, before.Body, fields, before.State, before.HumanCheck, before.DeletedAt))
	if err != nil {
		return events.Change{}, undoWriteErr(err)
	}
	return events.Change{NodeID: e.NodeID, Type: evNodeUpdated, Before: current, After: restored}, nil
}

func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

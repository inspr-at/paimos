// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// UndoHandlers makes knowledge writes reversible through the events module.
// Each handler locks the entry, requires it to be exactly as the event left
// it (a later edit closes the undo path), and keeps slugs unique.
func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{
		evCreated: undoCreate, evUpdated: undoUpdate, evDeleted: undoDelete,
		evLearningAccepted: undoLearningAccepted, evLearningDismissed: undoLearningDismissed,
		evLearningDrafted: undoLearningDrafted,
	}
}

func snapshots(e events.Event) (before, after nodeSnap, err error) {
	if e.NodeID == nil {
		return before, after, events.ErrConflict
	}
	if len(e.Before) > 0 && string(e.Before) != "null" {
		if json.Unmarshal(e.Before, &before) != nil || before.ID != *e.NodeID {
			return before, after, events.ErrConflict
		}
	}
	if json.Unmarshal(e.After, &after) != nil || after.ID != *e.NodeID {
		return before, after, events.ErrConflict
	}
	return before, after, nil
}

// unchanged: the stored entry is still the event's after snapshot.
func unchanged(current, after nodeSnap) bool {
	return current.UpdatedAt.Equal(after.UpdatedAt) && current.Title == after.Title && current.Body == after.Body &&
		current.State == after.State && jsonEqual(current.Fields, after.Fields) && (current.DeletedAt == nil) == (after.DeletedAt == nil)
}

func guard(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event, deleted bool) (nodeSnap, nodeSnap, nodeSnap, error) {
	if err := lockKnowledgeTree(ctx, tx); err != nil {
		return nodeSnap{}, nodeSnap{}, nodeSnap{}, err
	}
	if !canWrite(ctx, tx, p, "knowledge.write") {
		return nodeSnap{}, nodeSnap{}, nodeSnap{}, events.ErrForbidden
	}
	before, after, err := snapshots(e)
	if err != nil {
		return before, after, nodeSnap{}, err
	}
	current, _, err := lockNode(ctx, tx, p.TenantID, after.ID, deleted)
	if errors.Is(err, errNotFound) {
		return before, after, current, events.ErrConflict
	}
	if err != nil {
		return before, after, current, err
	}
	if !unchanged(current, after) {
		return before, after, current, events.ErrConflict
	}
	return before, after, current, nil
}

// Match generic node writers and their triggers: tenant, tree, pairing, then rows and
// slug reservations. Restores and field edits can also invoke tree guards.
func lockKnowledgeTree(ctx context.Context, tx pgx.Tx) error {
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`)
	return err
}

// slugFree re-checks uniqueness for a restored slug under the project lock.
func slugFree(ctx context.Context, tx pgx.Tx, tenantID string, n nodeSnap) error {
	slug := n.slug()
	if slug == "" {
		return nil
	}
	project, err := projectOf(ctx, tx, tenantID, n.ID)
	if err != nil {
		return err
	}
	if err := lockSlugs(ctx, tx, tenantID, project, n.KindID); err != nil {
		return err
	}
	taken, err := slugTaken(ctx, tx, tenantID, project, n.KindID, slug, n.ID)
	if err != nil {
		return err
	}
	if taken != nil {
		return events.ErrConflict
	}
	return nil
}

// undoCreate removes an entry nobody has changed since it was created.
func undoCreate(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	_, _, current, err := guard(ctx, tx, p, e, false)
	if err != nil {
		return events.Change{}, err
	}
	var children bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND parent_id=$2::uuid AND deleted_at IS NULL)`, p.TenantID, current.ID).Scan(&children); err != nil {
		return events.Change{}, err
	}
	if children {
		return events.Change{}, events.ErrConflict
	}
	removed, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(), updated_at=`+bumpUpdated+`
	  WHERE tenant_id=$1 AND id=$2::uuid RETURNING `+nodeReturning, p.TenantID, current.ID))
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: e.NodeID, Type: evCreated, Before: current, After: removed}, nil
}

// undoUpdate restores title, body, status, slug and metadata as they were.
func undoUpdate(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	before, _, current, err := guard(ctx, tx, p, e, false)
	if err != nil {
		return events.Change{}, err
	}
	if before.ID == "" {
		return events.Change{}, events.ErrConflict
	}
	if before.slug() != current.slug() {
		if err := slugFree(ctx, tx, p.TenantID, before); err != nil {
			return events.Change{}, err
		}
	}
	restored, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET title=$3, body=$4, state=$5, fields=$6::jsonb, updated_at=`+bumpUpdated+`
	  WHERE tenant_id=$1 AND id=$2::uuid RETURNING `+nodeReturning, p.TenantID, current.ID, before.Title, before.Body, before.State, string(before.Fields)))
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: e.NodeID, Type: evUpdated, Before: current, After: restored}, nil
}

// undoDelete brings a deleted entry back when its slug and parent allow it.
func undoDelete(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	_, _, current, err := guard(ctx, tx, p, e, true)
	if err != nil {
		return events.Change{}, err
	}
	if current.DeletedAt == nil {
		return events.Change{}, events.ErrConflict
	}
	if err := slugFree(ctx, tx, p.TenantID, current); err != nil {
		return events.Change{}, err
	}
	restored, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET deleted_at=NULL, updated_at=`+bumpUpdated+`
	  WHERE tenant_id=$1 AND id=$2::uuid RETURNING `+nodeReturning, p.TenantID, current.ID))
	if err != nil {
		// The tree guard refuses a restore under a deleted parent.
		return events.Change{}, events.ErrConflict
	}
	return events.Change{NodeID: e.NodeID, Type: evDeleted, Before: current, After: restored}, nil
}

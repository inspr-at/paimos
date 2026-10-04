// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// UndoHandlers restores a membership batch only while its revision-fenced
// project and release state are still current.
func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{"journey.release_membership_changed": undoMembership}
}

func undoMembership(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	var before, after membershipSnapshot
	if e.NodeID == nil || json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil ||
		before.ProjectID == "" || before.ProjectID != after.ProjectID || before.ReleaseID != after.ReleaseID || before.ReleaseID != *e.NodeID || len(before.Members) != len(after.Members) || len(before.Members) == 0 {
		return events.Change{}, events.ErrConflict
	}
	if err := lockMembership(ctx, tx, p, before.ProjectID); err != nil {
		var f *failure
		if errors.As(err, &f) && f.code == 403 {
			return events.Change{}, events.ErrForbidden
		}
		return events.Change{}, err
	}
	var current *string
	var projectRevision int64
	if err := tx.QueryRow(ctx, `SELECT current_release_node_id::text,revision FROM journey_projects WHERE project_node_id=$1 FOR UPDATE`, before.ProjectID).Scan(&current, &projectRevision); err != nil {
		return events.Change{}, events.ErrConflict
	}
	var state string
	var releaseRevision int64
	if err := tx.QueryRow(ctx, `SELECT state,revision FROM journey_releases WHERE project_node_id=$1 AND release_node_id=$2 FOR UPDATE`, before.ProjectID, before.ReleaseID).Scan(&state, &releaseRevision); err != nil {
		return events.Change{}, events.ErrConflict
	}
	if current == nil || *current != before.ReleaseID || state != "planning" || projectRevision != after.ProjectRevision || releaseRevision != after.ReleaseRevision {
		return events.Change{}, events.ErrConflict
	}
	oldReleases := map[string]bool{}
	if len(before.Parents) != len(after.Parents) {
		return events.Change{}, events.ErrConflict
	}
	for i, want := range after.Parents {
		old := before.Parents[i]
		var actual string
		if want.ParentID != old.ParentID || want.ReleaseID == nil {
			return events.Change{}, events.ErrConflict
		}
		if err := tx.QueryRow(ctx, `SELECT p.release_node_id::text FROM work_parent_releases p JOIN nodes n ON n.id=p.parent_node_id AND n.tenant_id=p.tenant_id
	 WHERE p.parent_node_id=$1 AND p.project_node_id=$2 AND n.project_id=$2 AND n.deleted_at IS NULL FOR UPDATE OF p`, want.ParentID, before.ProjectID).Scan(&actual); err != nil || actual != *want.ReleaseID {
			return events.Change{}, events.ErrConflict
		}
	}
	for i, want := range after.Members {
		old := before.Members[i]
		if want.TicketID != old.TicketID {
			return events.Change{}, events.ErrConflict
		}
		var found memberState
		found.TicketID = want.TicketID
		err := tx.QueryRow(ctx, `SELECT release_node_id::text,walker_position,scope_revision_required FROM journey_tickets WHERE project_node_id=$1 AND ticket_node_id=$2 FOR UPDATE`, before.ProjectID, want.TicketID).Scan(&found.ReleaseID, &found.Position, &found.ScopeRequired)
		if err != nil {
			return events.Change{}, events.ErrConflict
		}
		if !want.Exists || !sameRelease(found.ReleaseID, want.ReleaseID) || found.Position != want.Position || found.ScopeRequired != want.ScopeRequired {
			return events.Change{}, events.ErrConflict
		}
		var nodeProject string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL`, want.TicketID).Scan(&nodeProject); err != nil || nodeProject != before.ProjectID {
			return events.Change{}, events.ErrConflict
		}
		if old.ReleaseID != nil && *old.ReleaseID != before.ReleaseID {
			oldReleases[*old.ReleaseID] = true
		}
	}
	for id := range oldReleases {
		var oldState string
		if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE project_node_id=$1 AND release_node_id=$2 FOR UPDATE`, before.ProjectID, id).Scan(&oldState); err != nil || oldState != "planning" {
			return events.Change{}, events.ErrConflict
		}
	}
	for _, old := range before.Parents {
		if old.ReleaseID == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM work_parent_releases WHERE parent_node_id=$1`, old.ParentID); err != nil {
				return events.Change{}, err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE work_parent_releases SET release_node_id=$2 WHERE parent_node_id=$1`, old.ParentID, old.ReleaseID); err != nil {
			return events.Change{}, err
		}
	}
	for _, old := range before.Members {
		if !old.Exists {
			if _, err := tx.Exec(ctx, `DELETE FROM journey_tickets WHERE project_node_id=$1 AND ticket_node_id=$2`, before.ProjectID, old.TicketID); err != nil {
				return events.Change{}, err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE journey_tickets SET release_node_id=$2,walker_position=$3,scope_revision_required=$4 WHERE ticket_node_id=$1`, old.TicketID, old.ReleaseID, old.Position, old.ScopeRequired); err != nil {
				return events.Change{}, err
			}
		}
	}
	for id := range oldReleases {
		if _, err := tx.Exec(ctx, `UPDATE journey_releases SET revision=revision+1,access_required=EXISTS(SELECT 1 FROM journey_tickets WHERE release_node_id=$1 AND access_change) WHERE release_node_id=$1`, id); err != nil {
			return events.Change{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_releases SET revision=revision+1,access_required=EXISTS(SELECT 1 FROM journey_tickets WHERE release_node_id=$1 AND access_change) WHERE release_node_id=$1`, before.ReleaseID); err != nil {
		return events.Change{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_projects SET revision=revision+1,updated_at=now() WHERE project_node_id=$1`, before.ProjectID); err != nil {
		return events.Change{}, err
	}
	restored := before
	restored.ProjectRevision = projectRevision + 1
	restored.ReleaseRevision = releaseRevision + 1
	return events.Change{NodeID: e.NodeID, Type: e.Type, Before: after, After: restored}, nil
}

func sameRelease(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Use the canonical person and live role bindings, never legacy roles or a
// client-supplied owner. A key creator is recorded at registration time.
func ownsMove(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) (bool, error) {
	if p.Kind != tenant.Person {
		return false, nil
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.write", authz.Scope{ProjectID: s.ProjectID}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals p WHERE p.id=$1 AND
  (coalesce(p.linked_to,p.id)=$2::uuid OR EXISTS(SELECT 1 FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
   WHERE b.principal_id=coalesce(p.linked_to,p.id) AND r.builtin AND r.key IN ('owner','admin') AND
   (b.scope_type='workspace' OR b.scope_type='project' AND b.scope_id=$3::uuid))))`, p.ID, s.ownerID, s.ProjectID).Scan(&allowed)
	return allowed, err
}

func stampMoveRights(ctx context.Context, tx pgx.Tx, sessions []*Session) error {
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	// Cache decisions for rows sharing the project and registration owner.
	rights := map[string]bool{}
	for _, s := range sessions {
		allowed := false
		if s.StoppedAt == nil && s.ArchivedAt == nil {
			owner := ""
			if s.ownerID != nil {
				owner = *s.ownerID
			}
			key := s.ProjectID + ":" + owner
			var seen bool
			allowed, seen = rights[key]
			if !seen {
				var err error
				allowed, err = ownsMove(ctx, tx, p, *s)
				if err != nil {
					return err
				}
				rights[key] = allowed
			}
		}
		s.CanReparent = &allowed
	}
	return nil
}

func authorizeMove(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, parentID *string) error {
	allowed, err := ownsMove(ctx, tx, p, s)
	if err != nil {
		return err
	}
	if !allowed {
		return workorders.Fail(403, "owner or admin of both sessions required")
	}
	if s.StoppedAt != nil || s.ArchivedAt != nil {
		return workorders.Fail(409, "live worker required")
	}
	if parentID == nil {
		return nil
	}
	parent, err := load(ctx, tx, s.ProjectID, *parentID, true)
	if err != nil {
		return err
	}
	allowed, err = ownsMove(ctx, tx, p, parent)
	if err != nil {
		return err
	}
	if !allowed {
		return workorders.Fail(403, "owner or admin of both sessions required")
	}
	if parent.Role != "coordinator" || parent.StoppedAt != nil || parent.ArchivedAt != nil {
		return workorders.Fail(409, "live coordinator required")
	}
	return validateParent(ctx, tx, s.ProjectID, parent.ID, s.ID)
}

func (m *Module) reparent(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Revision int64  `json:"expected_revision"`
		Parent   string `json:"parent_harness_session_id"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision < 1 || !workorders.UUID(in.Parent) {
		return nil, workorders.Fail(400, "revision and parent session required")
	}
	ctx := r.Context()
	if err := lockHierarchy(ctx, tx, r.PathValue("projectId")); err != nil {
		return nil, err
	}
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if err = authorizeMove(ctx, tx, p, s, &in.Parent); err != nil {
		return nil, err
	}
	if s.Role != "worker" || s.Revision != in.Revision || same(s.ParentID, &in.Parent) {
		return nil, workorders.Fail(409, "worker revision or parent changed")
	}
	moved, err := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET parent_id=$2,adopted_from_id=NULL,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.Parent))
	if err != nil {
		return nil, err
	}
	event, err := events.Append(ctx, tx, p, events.Change{NodeID: &s.ProjectID, Type: "harness.reparented", Before: s, After: moved})
	if err != nil {
		return nil, err
	}
	undo := authz.RequireTx(ctx, tx, p, "events.undo", authz.Scope{ProjectID: s.ProjectID})
	if undo != nil && !errors.Is(undo, authz.ErrForbidden) {
		return nil, undo
	}
	if err = stampMoveRights(ctx, tx, []*Session{&moved}); err != nil {
		return nil, err
	}
	return struct {
		Session  Session `json:"session"`
		EventID  int64   `json:"event_id"`
		Undoable bool    `json:"undoable"`
	}{moved, event.ID, undo == nil}, nil
}

func undoReparent(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	var before, after Session
	if json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil || before.ID != after.ID || !workorders.UUID(before.ID) {
		return events.Change{}, events.ErrConflict
	}
	if err := lockHierarchy(ctx, tx, before.ProjectID); err != nil {
		return events.Change{}, err
	}
	current, err := load(ctx, tx, before.ProjectID, before.ID, true)
	if err != nil {
		return events.Change{}, err
	}
	if err = authorizeMove(ctx, tx, p, current, current.ParentID); err != nil {
		return events.Change{}, events.ErrForbidden
	}
	if err = authorizeMove(ctx, tx, p, current, before.ParentID); err != nil {
		return events.Change{}, events.ErrForbidden
	}
	var changed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE id>$1 AND type IN ('harness.bound','harness.reparented','harness.adopted','harness.reparent_undone') AND after->>'id'=$2)`, e.ID, current.ID).Scan(&changed)
	if err != nil {
		return events.Change{}, err
	}
	if changed || !same(current.ParentID, after.ParentID) {
		return events.Change{}, events.ErrConflict
	}
	restored, err := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET parent_id=$2,adopted_from_id=$3,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, current.ID, before.ParentID, before.AdoptedFromID))
	if err != nil {
		return events.Change{}, err
	}
	if err = stampMoveRights(ctx, tx, []*Session{&restored}); err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: &current.ProjectID, Type: "harness.reparent_undone", Before: current, After: restored}, nil
}

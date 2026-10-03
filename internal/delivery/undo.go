// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// UndoHandlers is mounted by the coordinator with events.WithUndoHandlers.
// Lifecycle/rollover events intentionally have no handler.
func (s *Store) UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{"ships_in.changed": s.undoPlacements, "release.reranked": s.undoRank}
}

func undoFailure(err error) error {
	if errors.Is(err, authz.ErrForbidden) {
		return errors.Join(events.ErrForbidden, err)
	}
	var conflict *Conflict
	if errors.As(err, &conflict) || errors.Is(err, ErrNotFound) {
		return errors.Join(events.ErrConflict, err)
	}
	return err
}

func (s *Store) undoPlacements(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if len(e.Before) > 1<<20 || len(e.After) > 1<<20 {
		return events.Change{}, events.ErrConflict
	}
	var before, after placementSnapshot
	if json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil || !uuid(before.ProjectID) || before.ProjectID != after.ProjectID || e.NodeID == nil || *e.NodeID != before.ProjectID || len(before.Members) == 0 || len(before.Members) > 101 || len(before.Members) != len(after.Members) {
		return events.Change{}, events.ErrConflict
	}
	requests := make([]PlacementRequest, 0, len(before.Members))
	restore := map[string]Placement{}
	for i, old := range before.Members {
		current := after.Members[i]
		if old.ItemID != current.ItemID || old.ProjectID != before.ProjectID || current.ProjectID != before.ProjectID || old.Revision < 0 || current.Revision < 1 || old.Rank != "" && !ValidRank(old.Rank) || old.Rank == "" && old.Revision != 0 {
			return events.Change{}, events.ErrConflict
		}
		requests = append(requests, PlacementRequest{ItemID: old.ItemID, ExpectedProjectID: before.ProjectID, ExpectedRevision: current.Revision, ReleaseID: old.ReleaseID, ExpectedReleaseRevision: 1, Expedite: old.Expedite, DueOn: old.DueOn})
		restore[old.ItemID] = old
	}
	if err := validatePlacementBound(before.ProjectID, requests, 101); err != nil {
		return events.Change{}, events.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// place selects history-correction or ordinary move authority from the
	// current locked containers, just as it does for a forward placement.
	if err := fence(ctx, tx, p, before.ProjectID, ""); err != nil {
		return events.Change{}, undoFailure(err)
	}
	// The event module's earlier check can become stale while waiting for
	// these fences. Other-actor authority is part of the final write too.
	if e.ActorPrincipalID != p.ID {
		if err := authz.RequireTx(ctx, tx, p, "events.undo_other", authz.Scope{ProjectID: before.ProjectID}); err != nil {
			return events.Change{}, undoFailure(err)
		}
	}
	w, err := s.projectWrite(ctx, tx, p, before.ProjectID)
	if err != nil {
		return events.Change{}, undoFailure(err)
	}
	actualBefore, actualAfter, err := w.place(requests, restore)
	if err != nil {
		return events.Change{}, undoFailure(err)
	}
	return events.Change{NodeID: e.NodeID, Type: e.Type, Before: placementSnapshot{before.ProjectID, actualBefore}, After: placementSnapshot{before.ProjectID, actualAfter}}, nil
}

type rankSnapshot struct {
	Release
	BeforeID string `json:"before_id,omitempty"`
	AfterID  string `json:"after_id,omitempty"`
}

func (w *write) rankSnapshot(r Release) (rankSnapshot, error) {
	n, err := ReleaseNeighbours(w.ctx, w.tx, w.p.TenantID, w.project, r.Rank, r.ID)
	out := rankSnapshot{Release: r}
	if n.Previous != nil {
		out.AfterID = n.Previous.ID
	}
	if n.Next != nil {
		out.BeforeID = n.Next.ID
	}
	return out, err
}

func (s *Store) undoRank(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if len(e.Before) > 8192 || len(e.After) > 8192 {
		return events.Change{}, events.ErrConflict
	}
	var before, after rankSnapshot
	if json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil || !uuid(before.ID) || !uuid(before.ProjectID) || before.ID != after.ID || before.ProjectID != after.ProjectID || e.NodeID == nil || *e.NodeID != before.ID || !ValidRank(before.Rank) || after.Revision < 1 {
		return events.Change{}, events.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := fence(ctx, tx, p, before.ProjectID, "releases.write"); err != nil {
		return events.Change{}, undoFailure(err)
	}
	if e.ActorPrincipalID != p.ID {
		if err := authz.RequireTx(ctx, tx, p, "events.undo_other", authz.Scope{ProjectID: before.ProjectID}); err != nil {
			return events.Change{}, undoFailure(err)
		}
	}
	if p.Kind != tenant.Person {
		return events.Change{}, events.ErrForbidden
	}
	w, err := s.projectWrite(ctx, tx, p, before.ProjectID)
	if err != nil {
		return events.Change{}, undoFailure(err)
	}
	releases, err := w.lockReleases([]string{before.ID})
	if err != nil {
		return events.Change{}, undoFailure(err)
	}
	old := releases[before.ID]
	if old.Revision != after.Revision || old.Rank != after.Rank {
		return events.Change{}, events.ErrConflict
	}
	rank, err := w.restoreRank(Placement{ItemID: old.ID, ProjectID: old.ProjectID, Rank: before.Rank, BeforeID: before.BeforeID, AfterID: before.AfterID}, false)
	if err != nil {
		return events.Change{}, undoFailure(err)
	}
	r, err := w.setRank(old, rank)
	if err != nil {
		return events.Change{}, undoFailure(err)
	}
	result, err := w.rankSnapshot(r)
	if err != nil {
		return events.Change{}, fmt.Errorf("restored rank snapshot: %w", err)
	}
	return events.Change{NodeID: e.NodeID, Type: e.Type, Before: after, After: result}, nil
}

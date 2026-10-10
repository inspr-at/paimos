// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// UndoHandlers reverses view changes through the events module. Each handler
// locks the view, refuses when it changed after the event (409), and lets only
// its owner or an admin undo. The events module appends the compensating event.
func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{
		"view.created":  undoCreated,
		"view.updated":  undoUpdated,
		"view.deleted":  undoDeleted,
		"view.restored": undoRestored,
	}
}

func undoCreated(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	return undoWith(ctx, tx, p, e, func(ctx context.Context, current, _ savedView) (savedView, error) {
		if current.DeletedAt != nil {
			return savedView{}, events.ErrConflict
		}
		return setDeleted(ctx, tx, current.ID, true)
	}, "view.deleted")
}

func undoUpdated(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	return undoWith(ctx, tx, p, e, func(ctx context.Context, current, before savedView) (savedView, error) {
		if current.DeletedAt != nil {
			return savedView{}, events.ErrConflict
		}
		// Events from older clients predate the mode field.
		if before.Mode == "" {
			before.Mode = "list"
		}
		return writeView(ctx, tx, before)
	}, "view.updated")
}

func undoDeleted(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	return undoWith(ctx, tx, p, e, func(ctx context.Context, current, _ savedView) (savedView, error) {
		if current.DeletedAt == nil {
			return savedView{}, events.ErrConflict
		}
		return setDeleted(ctx, tx, current.ID, false)
	}, "view.restored")
}

func undoRestored(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	return undoWith(ctx, tx, p, e, func(ctx context.Context, current, _ savedView) (savedView, error) {
		if current.DeletedAt != nil {
			return savedView{}, events.ErrConflict
		}
		return setDeleted(ctx, tx, current.ID, true)
	}, "view.deleted")
}

// undoWith loads the view the event is about, checks it is still as the event
// left it, and applies restore. before is the event's before snapshot (empty
// for a creation).
func undoWith(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event,
	restore func(context.Context, savedView, savedView) (savedView, error), resultType string) (events.Change, error) {
	var before, after savedView
	if len(e.After) == 0 || json.Unmarshal(e.After, &after) != nil || after.ID == "" {
		return events.Change{}, events.ErrConflict
	}
	if len(e.Before) > 0 && (json.Unmarshal(e.Before, &before) != nil || before.ID != after.ID) {
		return events.Change{}, events.ErrConflict
	}
	current, err := selectView(ctx, tx, `id = $1::uuid FOR UPDATE`, after.ID)
	if errors.Is(err, errNotFound) {
		return events.Change{}, events.ErrNotFound
	}
	if err != nil {
		return events.Change{}, err
	}
	if current.OwnerPrincipal != p.ID && authz.RequireTx(ctx, tx, p, "views.share", authz.Scope{}) != nil {
		return events.Change{}, events.ErrForbidden
	}
	if !current.UpdatedAt.Equal(after.UpdatedAt) {
		return events.Change{}, events.ErrConflict
	}
	restored, err := restore(ctx, current, before)
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{Type: resultType, Before: current, After: restored}, nil
}

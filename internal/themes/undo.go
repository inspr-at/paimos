// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func UndoHandlers() map[string]events.UndoFunc {
	return map[string]events.UndoFunc{
		"theme.created": undoTheme, "theme.updated": undoTheme,
		"theme.deleted": undoTheme, "theme.selected": undoSelection,
	}
}
func undoTheme(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return events.Change{}, err
	}
	var expected Theme
	if json.Unmarshal(e.After, &expected) != nil || !validUUID(expected.ID) || expected.TenantID != p.TenantID {
		return events.Change{}, events.ErrConflict
	}
	current, err := read(ctx, tx, p.TenantID, expected.ID, true, true)
	if err != nil {
		return events.Change{}, err
	}
	if err := writable(ctx, tx, p, current.Scope, current.OwnerPrincipalID); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return events.Change{}, events.ErrForbidden
		}
		return events.Change{}, err
	}
	if !same(current, expected) {
		return events.Change{}, events.ErrConflict
	}
	var restored Theme
	typ := "theme.updated"
	if e.Type == "theme.created" {
		if current.Scope == "default" {
			return events.Change{}, events.ErrConflict
		}
		restored = current
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&restored.DeletedAt); err != nil {
			return events.Change{}, err
		}
		typ = "theme.deleted"
	} else {
		if json.Unmarshal(e.Before, &restored) != nil || restored.ID != current.ID || restored.TenantID != p.TenantID ||
			restored.Scope != current.Scope || !same(restored.OwnerPrincipalID, current.OwnerPrincipalID) ||
			restored.Values.validate() != nil || !validName(restored.Name) {
			return events.Change{}, events.ErrConflict
		}
	}
	restored.Revision = current.Revision + 1
	restored, err = save(ctx, tx, restored)
	if err != nil {
		return events.Change{}, err
	}
	return themeChange(typ, &current, &restored), nil
}
func undoSelection(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return events.Change{}, err
	}
	if p.Kind != tenant.Person {
		return events.Change{}, events.ErrForbidden
	}
	if err := selfPermission(ctx, tx, p, "write"); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return events.Change{}, events.ErrForbidden
		}
		return events.Change{}, err
	}
	id, err := canonical(ctx, tx, p)
	if err != nil {
		return events.Change{}, err
	}
	var expected, restored Selection
	if json.Unmarshal(e.After, &expected) != nil || json.Unmarshal(e.Before, &restored) != nil || expected.PrincipalID != id || restored.PrincipalID != id {
		return events.Change{}, events.ErrForbidden
	}
	if restored.ThemeID != nil {
		if !validUUID(*restored.ThemeID) {
			return events.Change{}, events.ErrConflict
		}
		if _, err := read(ctx, tx, p.TenantID, *restored.ThemeID, true, true); err != nil {
			return events.Change{}, events.ErrConflict
		}
	}
	current, err := selection(ctx, tx, p.TenantID, id, true)
	if err != nil {
		return events.Change{}, err
	}
	if !same(current, expected) {
		return events.Change{}, events.ErrConflict
	}
	restored.Revision = current.Revision + 1
	if err := saveSelection(ctx, tx, p.TenantID, restored); err != nil {
		return events.Change{}, err
	}
	return events.Change{Type: "theme.selected", Before: current, After: restored, Metadata: audience(&id)}, nil
}

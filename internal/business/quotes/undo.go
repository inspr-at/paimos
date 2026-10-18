// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

// UndoHandlers makes the Quotes list's quick actions reversible through the event
// log (QL1/AEON-109): deleting a never-issued draft, archiving or restoring, and
// duplicating (which deletes the untouched copy). The coordinator registers them
// with events.WithUndoHandlers(quotes.UndoHandlers(registry)). Issuing, accepting
// and receipts stay immutable; public links keep their own handler.
func UndoHandlers(registry *plugins.Registry) map[string]events.UndoFunc {
	m := &Module{registry: registry}
	return map[string]events.UndoFunc{
		"quote.deleted":            m.undoDeleted,
		"quote.visibility_changed": m.undoVisibility,
		"quote.duplicated":         m.undoDuplicated,
	}
}

func (m *Module) undoGate(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if authz.RequireTx(ctx, tx, p, "quotes.delete", authz.Scope{}) != nil {
		return events.ErrForbidden
	}
	if m.registry == nil || m.enabled(ctx, tx, p.TenantID, fence.PermNodesContribute, true) != nil {
		return events.ErrForbidden
	}
	return nil
}
func undoNode(e events.Event) (string, error) {
	if e.NodeID == nil || !uuidRe.MatchString(*e.NodeID) {
		return "", events.ErrConflict
	}
	return *e.NodeID, nil
}
func conflictOr(err error) error {
	var f failure
	if errors.As(err, &f) {
		return events.ErrConflict
	}
	return err
}

// Deleting is undone while the quote is still exactly as it was deleted.
func (m *Module) undoDeleted(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if err := m.undoGate(ctx, tx, p); err != nil {
		return events.Change{}, err
	}
	id, err := undoNode(e)
	if err != nil {
		return events.Change{}, err
	}
	var after struct {
		Revision int64 `json:"revision"`
	}
	if json.Unmarshal(e.After, &after) != nil || after.Revision < 1 {
		return events.Change{}, events.ErrConflict
	}
	q, err := readDeleted(ctx, tx, id)
	if err != nil {
		return events.Change{}, conflictOr(err)
	}
	if q.Revision != after.Revision {
		return events.Change{}, events.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL,updated_at=clock_timestamp() WHERE id=$1::uuid AND deleted_at IS NOT NULL`, id); err != nil {
		return events.Change{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE business_quotes SET deleted_at=NULL,deleted_by_principal_id=NULL,revision=revision+1 WHERE quote_node_id=$1::uuid`, id); err != nil {
		return events.Change{}, err
	}
	restored, err := readQuote(ctx, tx, id, false)
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: &id, Type: "quote.restored", Before: map[string]any{"deleted": true}, After: restored}, nil
}

// Archiving or restoring is undone while nothing else changed the quote since.
func (m *Module) undoVisibility(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	// Acquire the tenant/tree fence before the plugin gate takes share locks.
	if err := lockQuoteTree(ctx, tx); err != nil {
		return events.Change{}, err
	}
	if err := m.undoGate(ctx, tx, p); err != nil {
		return events.Change{}, err
	}
	id, err := undoNode(e)
	if err != nil {
		return events.Change{}, err
	}
	var before, after struct {
		Archived *bool `json:"archived"`
		Revision int64 `json:"revision"`
	}
	if json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil || before.Archived == nil || after.Archived == nil {
		return events.Change{}, events.ErrConflict
	}
	q, err := readQuote(ctx, tx, id, true)
	if err != nil {
		return events.Change{}, conflictOr(err)
	}
	// Historical events without a revision cannot establish freshness. They
	// remain audit evidence; an explicit visibility edit is the safe alternative.
	if after.Revision < 1 || q.Revision != after.Revision || q.Archived != *after.Archived {
		return events.Change{}, events.ErrConflict
	}
	if err := m.undoGate(ctx, tx, p); err != nil {
		return events.Change{}, err
	}
	if *before.Archived {
		_, err = tx.Exec(ctx, `UPDATE business_quotes SET archived_at=clock_timestamp(),archived_by_principal_id=$2::uuid,revision=revision+1 WHERE quote_node_id=$1::uuid`, id, p.ID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE business_quotes SET archived_at=NULL,archived_by_principal_id=NULL,revision=revision+1 WHERE quote_node_id=$1::uuid`, id)
	}
	if err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: &id, Type: "quote.visibility_changed", Before: map[string]any{"archived": q.Archived, "revision": q.Revision}, After: map[string]any{"archived": *before.Archived, "revision": q.Revision + 1}}, nil
}

// A duplicate is undone by deleting the copy, but only while nobody touched it.
func (m *Module) undoDuplicated(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	if err := m.undoGate(ctx, tx, p); err != nil {
		return events.Change{}, err
	}
	id, err := undoNode(e)
	if err != nil {
		return events.Change{}, err
	}
	var after struct {
		Quote quote `json:"quote"`
	}
	if json.Unmarshal(e.After, &after) != nil || after.Quote.QuoteNodeID != id {
		return events.Change{}, events.ErrConflict
	}
	q, err := readQuote(ctx, tx, id, true)
	if err != nil {
		return events.Change{}, conflictOr(err)
	}
	if q.Revision != after.Quote.Revision || deletable(ctx, tx, q) != nil {
		return events.Change{}, events.ErrConflict
	}
	var saves int64
	if err := tx.QueryRow(ctx, `SELECT draft_revision FROM quote_drafts WHERE quote_node_id=$1::uuid`, id).Scan(&saves); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return events.Change{}, err
	}
	if saves > 1 {
		return events.Change{}, events.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE business_quotes SET deleted_at=clock_timestamp(),deleted_by_principal_id=$2::uuid,revision=revision+1 WHERE quote_node_id=$1::uuid`, id, p.ID); err != nil {
		return events.Change{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, id); err != nil {
		return events.Change{}, err
	}
	return events.Change{NodeID: &id, Type: "quote.deleted", Before: q, After: map[string]any{"deleted": true, "revision": q.Revision + 1}}, nil
}

// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Event is the public durable event representation. IDs are tenant-local.
type Event struct {
	ID               int64           `json:"id"`
	ActorPrincipalID string          `json:"actor_principal_id"`
	NodeID           *string         `json:"node_id"`
	Type             string          `json:"type"`
	Before           json.RawMessage `json:"before"`
	After            json.RawMessage `json:"after"`
	At               time.Time       `json:"at"`
	UndoOf           *int64          `json:"undo_of"`
}

// Change describes complete resource snapshots. A nil snapshot is SQL NULL.
// UndoOf is reserved for a compensating change produced by the undo module.
type Change struct {
	NodeID *string
	Type   string
	Before any
	After  any
	// At preserves a source timestamp during import; nil uses the database clock.
	At     *time.Time
	UndoOf *int64
}

// Writer can be injected behind an interface with the Append method below.
type Writer struct{}

func (Writer) Append(ctx context.Context, tx pgx.Tx, p tenant.Principal, c Change) (Event, error) {
	return Append(ctx, tx, p, c)
}

// Append writes in the caller's db.InTenant transaction; it never commits it.
func Append(ctx context.Context, tx pgx.Tx, p tenant.Principal, c Change) (Event, error) {
	before, err := snapshot(c.Before)
	if err != nil {
		return Event{}, fmt.Errorf("event before: %w", err)
	}
	after, err := snapshot(c.After)
	if err != nil {
		return Event{}, fmt.Errorf("event after: %w", err)
	}
	after, err = annotateOperator(ctx, after)
	if err != nil {
		return Event{}, err
	}
	if before == nil && after == nil {
		return Event{}, fmt.Errorf("event requires a snapshot")
	}
	return scanEvent(tx.QueryRow(ctx, `INSERT INTO events
	  (tenant_id, actor_principal_id, node_id, type, before, after, at, undo_of)
	  VALUES ($1,$2,$3,$4,$5,$6,coalesce($7::timestamptz,clock_timestamp()),$8)
	  RETURNING id, actor_principal_id::text, node_id::text, type, before, after, at, undo_of`,
		p.TenantID, p.ID, c.NodeID, c.Type, before, after, c.At, c.UndoOf))
}

func snapshot(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if string(b) == "null" {
		return nil, err
	}
	return b, err
}

func scanEvent(row pgx.Row) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.ActorPrincipalID, &e.NodeID, &e.Type, &e.Before, &e.After, &e.At, &e.UndoOf)
	return e, err
}

// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
	// NodeChanges is set when the event is read (history and stream), never
	// stored: the nodes a node.* event changed, for live views.
	NodeChanges []NodeChange `json:"node_changes,omitempty"`
	Derivation  *Derivation  `json:"derivation,omitempty"`
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
	// Metadata is optional job context stored beside the snapshots. Nil stores
	// SQL NULL. A value must be a JSON object, for example a background job
	// name and the reason it wrote the event.
	Metadata json.RawMessage
}

// Writer can be injected behind an interface with the Append method below.
type Writer struct{}

func (Writer) Append(ctx context.Context, tx pgx.Tx, p tenant.Principal, c Change) (Event, error) {
	return Append(ctx, tx, p, c)
}

// MutationGuard inspects one change before it is inserted. The nodes package
// registers the portal catalog check so every later writer is covered. A
// non-nil error aborts the caller's transaction.
type MutationGuard func(context.Context, pgx.Tx, tenant.Principal, Change) error

var mutationGuard MutationGuard

// SetMutationGuard installs the process-wide check. Nil leaves Append unchanged.
func SetMutationGuard(fn MutationGuard) { mutationGuard = fn }

// PortalCatalogDenied reports the nodes trigger that blocks a portal catalog
// update or delete unless the transaction armed aeon.portal_moderation.
func PortalCatalogDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501" && pgErr.Message == "portal moderation required"
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
	if mutationGuard != nil {
		if err := mutationGuard(ctx, tx, p, c); err != nil {
			return Event{}, err
		}
	}
	meta, err := objectMetadata(c.Metadata)
	if err != nil {
		return Event{}, err
	}
	return scanEvent(tx.QueryRow(ctx, `INSERT INTO events
	  (tenant_id, actor_principal_id, node_id, type, before, after, at, undo_of, metadata)
	  VALUES ($1,$2,$3,$4,$5,$6,coalesce($7::timestamptz,clock_timestamp()),$8,$9::jsonb)
	  RETURNING id, actor_principal_id::text, node_id::text, type, before, after, at, undo_of`,
		p.TenantID, p.ID, c.NodeID, c.Type, before, after, c.At, c.UndoOf, meta))
}

// objectMetadata accepts only a JSON object. An empty value stores NULL.
func objectMetadata(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("event metadata: object required")
	}
	return []byte(raw), nil
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

// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"encoding/json"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Deferring importer events keeps the event counter last in the lock order.
// In-transaction baselines and refs give later parent/relation passes the same
// view they had when events were appended eagerly.
type importEventBatchKey struct{}
type importEventBatch struct {
	pending []func() error
	nodes   map[string][]byte
	refs    map[string]bool
}

func newImportEventBatch(ctx context.Context) (context.Context, *importEventBatch) {
	b := &importEventBatch{nodes: map[string][]byte{}, refs: map[string]bool{}}
	return context.WithValue(ctx, importEventBatchKey{}, b), b
}

func appendImportEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, c events.Change) (events.Event, error) {
	batch, _ := ctx.Value(importEventBatchKey{}).(*importEventBatch)
	if batch == nil {
		return events.Append(ctx, tx, p, c)
	}
	after, err := json.Marshal(c.After)
	if err != nil {
		return events.Event{}, err
	}
	if c.NodeID != nil && (c.Type == "import.node_created" || c.Type == "import.node_updated" || c.Type == "import.parent_changed") {
		batch.nodes[*c.NodeID] = after
	}
	if payload, ok := c.After.(Record); ok {
		if ref, ok := payload["classic_ref"].(string); ok {
			batch.refs[c.Type+":"+ref] = true
		}
	}
	batch.pending = append(batch.pending, func() error { _, err := events.Append(ctx, tx, p, c); return err })
	return events.Event{NodeID: c.NodeID, Type: c.Type, After: after}, nil
}

func (b *importEventBatch) flush() error {
	for _, fn := range b.pending {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

func pendingNodeBaseline(ctx context.Context, id string) []byte {
	if batch, ok := ctx.Value(importEventBatchKey{}).(*importEventBatch); ok {
		return batch.nodes[id]
	}
	return nil
}

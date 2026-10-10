// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// eventBatch holds the events of a multi-target mutation until every target
// row is locked and changed: the event counter is the last lock taken.
type eventBatch struct {
	pending []batchedEvent
}
type batchedEvent struct {
	principal tenant.Principal
	change    events.Change
}
type eventBatchKey struct{}

func withEventBatch(ctx context.Context) (context.Context, *eventBatch) {
	b := &eventBatch{}
	return context.WithValue(ctx, eventBatchKey{}, b), b
}

// appendEvent writes now, or into the open batch of a multi-target mutation.
func appendEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, c events.Change) error {
	if b, ok := ctx.Value(eventBatchKey{}).(*eventBatch); ok && b != nil {
		b.pending = append(b.pending, batchedEvent{principal: p, change: c})
		return nil
	}
	_, err := events.Append(ctx, tx, p, c)
	return err
}

// flush appends the collected events in their original order. Call it once,
// after the final row lock of the transaction.
func (b *eventBatch) flush(ctx context.Context, tx pgx.Tx) error {
	pending := b.pending
	b.pending = nil
	for _, e := range pending {
		if _, err := events.Append(ctx, tx, e.principal, e.change); err != nil {
			return err
		}
	}
	return nil
}

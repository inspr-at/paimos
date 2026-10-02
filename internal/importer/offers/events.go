// SPDX-License-Identifier: AGPL-3.0-only
package offers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Import completes every resource write and lock acquisition before taking the
// event counter. The final flush only appends events. Nothing is committed
// until the batch and the deferred issue evidence checks succeed.
type importEventsKey struct{}
type importEvents struct {
	pending  []func() error
	flushing bool
	firstID  int64
}

// nextImportEventID predicts an identity without acquiring the event counter.
// The tenant fence serializes conforming writers. Every issuance append checks
// the prediction too, so an unfenced writer causes rollback rather than a link
// to another event. The issue FK and evidence trigger are deferred to commit.
func nextImportEventID(ctx context.Context, tx pgx.Tx) (int64, error) {
	batch, ok := ctx.Value(importEventsKey{}).(*importEvents)
	if !ok || batch.flushing {
		return 0, errors.New("issuance requires an unflushed import batch")
	}
	if batch.firstID == 0 {
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT last_id FROM event_counters WHERE tenant_id=current_setting('aeon.tenant_id')::uuid),0)+1`).Scan(&batch.firstID); err != nil {
			return 0, err
		}
	}
	return batch.firstID + int64(len(batch.pending)), nil
}

func queueImportEvent(ctx context.Context, fn func() error) error {
	if batch, ok := ctx.Value(importEventsKey{}).(*importEvents); ok && !batch.flushing {
		batch.pending = append(batch.pending, fn)
		return nil
	}
	return fn()
}

func (b *importEvents) flush() error {
	b.flushing = true
	for _, fn := range b.pending {
		if err := fn(); err != nil {
			return err
		}
	}
	b.pending = nil
	return nil
}

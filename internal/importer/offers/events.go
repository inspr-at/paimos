// SPDX-License-Identifier: AGPL-3.0-only
package offers

import "context"

// Import completes every resource write and lock acquisition before taking the
// event counter. Issuance closures insert their immutable evidence using those
// already-held quote locks. Nothing is committed until the batch succeeds.
type importEventsKey struct{}
type importEvents struct {
	pending  []func() error
	flushing bool
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

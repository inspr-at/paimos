// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"sync"
)

// contextMutex has a usable zero value and lets queued operations abandon
// their turn without leaving a goroutine that could acquire the lock later.
type contextMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *contextMutex) TryLock() bool {
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
	select {
	case m.gate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *contextMutex) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
	select {
	case m.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *contextMutex) Lock()   { _ = m.LockContext(context.Background()) }
func (m *contextMutex) Unlock() { <-m.gate }

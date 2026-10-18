// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewWithQueueClock replaces only the timer source, per fixture. Route budgets,
// authorization, locks, writes and cancellation all follow the production path.
func NewWithQueueClock(pool *pgxpool.Pool, timeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)) httpapi.Module {
	return &module{pool: pool, queueTimeout: timeout}
}

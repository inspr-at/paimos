// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewWithOwnershipClock injects a per-module clock without changing other tests.
func NewWithOwnershipClock(pool *pgxpool.Pool, now func() time.Time) httpapi.Module {
	return &Module{pool: pool, ownershipClock: func(context.Context, pgx.Tx) (time.Time, error) {
		return now(), nil
	}}
}

// LiveQuery is the live read's statement, for plan checks.
const LiveQuery = liveQuery

// SetMaxLive lowers one answer's bound for a test and returns the restore.
func SetMaxLive(n int) (restore func()) {
	old := maxLive
	maxLive = n
	return func() { maxLive = old }
}

// SetRemoveStaleBatch lowers the batch removal bound for a test and returns the restore.
func SetRemoveStaleBatch(n int) (restore func()) {
	old := removeStaleBatch
	removeStaleBatch = n
	return func() { removeStaleBatch = old }
}

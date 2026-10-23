// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewWithQueueClock replaces only the timer source, per fixture. Route budgets,
// authorization, locks, writes and cancellation all follow the production path.
func NewWithQueueClock(pool *pgxpool.Pool, timeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)) httpapi.Module {
	return &module{pool: pool, queueTimeout: timeout}
}

// Risk: the same caller UUID collides with another tenant's global run key.
func TestRunOnceRequestIDIsTenantScoped(t *testing.T) {
	request := "a0000000-0000-4000-8000-000000000001"
	first := *requestedRunID("tenant-one", &request)
	if first == request || first != *requestedRunID("tenant-one", &request) || first == *requestedRunID("tenant-two", &request) {
		t.Fatal("request UUID is not a stable tenant-scoped run key")
	}
}

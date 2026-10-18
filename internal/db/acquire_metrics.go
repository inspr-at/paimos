// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"errors"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AcquireErrorClass deliberately has a fixed label set. Never label metrics
// with driver error text, SQL, connection strings or caller-supplied SQLSTATE.
func AcquireErrorClass(err error) string {
	var pg *pgconn.PgError
	var network net.Error
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline"
	case errors.Is(err, pgconn.ErrConnClosed):
		return "connection_closed"
	case errors.As(err, &pg):
		return "postgres"
	case errors.As(err, &network):
		if network.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	default:
		return "internal"
	}
}

// AcquireErrors snapshots process-lifetime failed acquisition counters. PHAROS
// can also count the content-free 'pool acquire failed' structured log entries.
func AcquireErrors(pool *pgxpool.Pool) map[string]uint64 {
	if pool == nil {
		return map[string]uint64{}
	}
	m := monitor(pool)
	if m == nil {
		return map[string]uint64{}
	}
	return m.acquireErrorSnapshot()
}
func (m *PoolMonitor) acquireErrorSnapshot() map[string]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]uint64, len(m.acquireErrors))
	for class, total := range m.acquireErrors {
		out[class] = total
	}
	return out
}

// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: failed acquisitions disappear from telemetry or label it with secrets.
func TestPoolAcquireErrorCounters(t *testing.T) {
	m := &PoolMonitor{}
	cases := []struct {
		err   error
		class string
	}{
		{context.Canceled, "context_canceled"}, {fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "context_deadline"},
		{pgconn.ErrConnClosed, "connection_closed"}, {&pgconn.PgError{Code: "private SQLSTATE", Message: "private"}, "postgres"},
		{&net.DNSError{IsTimeout: true, Err: "private"}, "network_timeout"}, {&net.DNSError{Err: "private"}, "network_error"}, {errors.New("private"), "internal"},
	}
	for _, tc := range cases {
		ctx := m.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
		m.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{Err: tc.err})
		if got := AcquireErrorClass(tc.err); got != tc.class {
			t.Fatalf("class %s want %s", got, tc.class)
		}
	}
	ctx := m.TraceAcquireStart(context.Background(), nil, pgxpool.TraceAcquireStartData{})
	m.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{})
	rows := m.acquireErrorSnapshot()
	if len(rows) != len(cases) {
		t.Fatalf("wrong label count: %v", rows)
	}
	for _, tc := range cases {
		if rows[tc.class] != 1 {
			t.Fatalf("lost acquisition: %v", rows)
		}
	}
	rows["internal"] = 100
	if m.acquireErrorSnapshot()["internal"] != 1 {
		t.Fatal("snapshot aliases counter map")
	}
	if m.waiting.Load() != 0 {
		t.Fatal("error counter changed acquisition ownership")
	}
}

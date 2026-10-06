// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
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

// FreezeLeadReportingClock executes the real freshness predicates against the
// real database with an injected clock. All locks and authorization stay intact.
func FreezeLeadReportingClock(tx pgx.Tx, now time.Time) pgx.Tx {
	return leadReportingClockTx{Tx: tx, now: now}
}

type leadReportingClockTx struct {
	pgx.Tx
	now time.Time
}

func (tx leadReportingClockTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "coalesce(heartbeat_at,created_at)") {
		args = append(args, tx.now)
		sql = strings.ReplaceAll(sql, "clock_timestamp()", fmt.Sprintf("$%d::timestamptz", len(args)))
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}
func ClaimLeadInTx(pool *pgxpool.Pool, admission LeadAdmission, r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return (&Module{pool: pool, leadAdmission: admission}).claimLead(r, tx, p)
}

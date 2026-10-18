// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type readLimitKey struct{}
type readLimit struct {
	duration time.Duration
	timedOut atomic.Bool
}

// WithReadStatementTimeout limits statements in tenant transactions opened
// during this request. The setting is LOCAL and disappears on commit/rollback.
func WithReadStatementTimeout(ctx context.Context, duration time.Duration) context.Context {
	return context.WithValue(ctx, readLimitKey{}, &readLimit{duration: duration})
}

// ReadStatementTimedOut reports a PostgreSQL statement_timeout seen in ctx.
// API middleware uses it to replace a module's generic database error with 503.
func ReadStatementTimedOut(ctx context.Context) bool {
	limit, ok := ctx.Value(readLimitKey{}).(*readLimit)
	return ok && limit.timedOut.Load()
}

// SetLocalStatementTimeout applies a positive timeout inside an open transaction.
// Longer jobs may override the backstop only for justified SQL work, with all
// network/vendor waits outside the transaction. COMMIT/ROLLBACK removes it.
// Integer GUC values are milliseconds; no caller text enters SQL.
func SetLocalStatementTimeout(ctx context.Context, tx pgx.Tx, duration time.Duration) error {
	if duration <= 0 {
		return errors.New("statement timeout must be positive")
	}
	ms := duration / time.Millisecond
	if duration%time.Millisecond != 0 {
		ms++
	}
	_, err := tx.Exec(ctx, `SET LOCAL statement_timeout = `+strconv.FormatInt(int64(ms), 10))
	return err
}

// IsStatementTimeout distinguishes the server's statement timeout from a
// client-cancelled request, which must not be advertised as retryable.
func IsStatementTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "57014" && strings.Contains(pgErr.Message, "statement timeout")
}

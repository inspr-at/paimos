// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultStatementTimeout       = 30 * time.Second
	DefaultLockTimeout            = 5 * time.Second
	DefaultIdleTransactionTimeout = 30 * time.Second
	MigrationStatementTimeout     = 5 * time.Minute
	IndexBuildStatementTimeout    = 30 * time.Minute
)

// installBackstops scopes defaults to this login role and database. Ordinary
// roles may configure their own defaults without CREATEROLE or BYPASSRLS.
// Preserve explicit server, database, role and connection settings, including
// zero: deployment tuning is authoritative. Reset startup connections because
// ALTER ROLE settings take effect only at login, including dedicated LISTENs.
func installBackstops(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := func() error {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// Startup precedes the migration lock. Serialize catalogue writes for
		// this role; PostgreSQL advisory locks are already database-scoped.
		// Transaction scope releases the fence on commit, rollback or disconnect,
		// and the installation deadline also bounds waiting for another start.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-db-backstops:'||current_user,0))`); err != nil {
			return fmt.Errorf("installation lock: %w", err)
		}
		var database string
		if err := tx.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
			return err
		}
		defaults := []struct {
			name     string
			duration time.Duration
		}{
			{"statement_timeout", DefaultStatementTimeout},
			{"lock_timeout", DefaultLockTimeout},
			{"idle_in_transaction_session_timeout", DefaultIdleTransactionTimeout},
		}
		for _, setting := range defaults {
			var source string
			if err := tx.QueryRow(ctx, `SELECT source FROM pg_settings WHERE name=$1`, setting.name).Scan(&source); err != nil {
				return err
			}
			if source != "default" {
				continue
			}
			stmt := `ALTER ROLE CURRENT_USER IN DATABASE ` + pgx.Identifier{database}.Sanitize() +
				` SET ` + setting.name + ` = '` + fmt.Sprint(setting.duration.Milliseconds()) + `ms'`
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("default %s: %w", setting.name, err)
			}
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		return fmt.Errorf("database backstops: %w", err)
	}
	pool.Reset()
	return nil
}

// migrationTimeouts is a temporary session override on the existing migration
// connection, never a separate pool. Session scope is necessary for concurrent
// DDL; application jobs use SetLocalStatementTimeout inside their SQL-only tx.
// A failed restoration destroys the connection rather than leaking a budget.
func migrationTimeouts(ctx context.Context, conn *pgxpool.Conn, statement time.Duration) (func(), error) {
	var priorStatement, priorLock, priorIdle string
	if err := conn.QueryRow(ctx, `SELECT current_setting('statement_timeout'), current_setting('lock_timeout'),
		current_setting('idle_in_transaction_session_timeout')`).Scan(&priorStatement, &priorLock, &priorIdle); err != nil {
		return nil, err
	}
	restore := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT set_config('statement_timeout',$1,false), set_config('lock_timeout',$2,false),
			set_config('idle_in_transaction_session_timeout',$3,false)`, priorStatement, priorLock, priorIdle); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('statement_timeout',$1,false), set_config('lock_timeout','5s',false),
		set_config('idle_in_transaction_session_timeout','1min',false)`, fmt.Sprint(statement.Milliseconds())); err != nil {
		restore()
		return nil, err
	}
	return restore, nil
}

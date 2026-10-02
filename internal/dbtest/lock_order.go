// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// WaitForLock observes a real database wait, without timing-based barriers.
// The caller supplies a deadline so a missing wait cannot hang the suite.
func WaitForLock(t testing.TB, ctx context.Context, d *DB, blocker uint32, want string) uint32 {
	t.Helper()
	for {
		var pid uint32
		var kind string
		err := d.Admin.QueryRow(ctx, `SELECT a.pid,l.locktype
		 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid
		 WHERE $1::int=ANY(pg_blocking_pids(a.pid)) AND NOT l.granted
		 ORDER BY a.pid LIMIT 1`, blocker).Scan(&pid, &kind)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			t.Fatalf("observe %s wait: %v", want, err)
		}
		if kind != want {
			t.Fatalf("waited on %s before %s; tenant fence must precede tree lock", kind, want)
		}
		return pid
	}
}

// TenantBeforeTree holds both barriers on one connection. Releasing the
// tenant transaction while retaining a session tree lock exposes the second
// wait separately. A tree-first writer fails at the first observed wait.
func TenantBeforeTree(t *testing.T, d *DB, tenantID string, write func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	holder, err := pgx.ConnectConfig(ctx, d.Admin.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(context.Background()) // Also releases the session lock on failure.
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, tenantID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- write(ctx) }()
	defer cancel()
	pid := WaitForLock(t, ctx, d, holder.PgConn().PID(), "transactionid")
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if next := WaitForLock(t, ctx, d, holder.PgConn().PID(), "advisory"); next != pid {
		t.Fatalf("different writers at the two barriers: %d then %d", pid, next)
	}
	// While the writer holds its tenant fence and waits for the tree, foreign
	// key readers must still be able to acquire KEY SHARE on that tenant.
	if _, err = holder.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR KEY SHARE NOWAIT`, tenantID); err != nil {
		t.Fatalf("tenant fence blocks foreign-key readers: %v", err)
	}
	if _, err = holder.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, tenantID); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

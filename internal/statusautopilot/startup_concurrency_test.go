// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// The first principal insert owns the principal-link lock before its first
// node takes the tree lock. The autopilot must resolve its System actor before
// taking tree, even when that actor has already been created by bootstrap.
func TestStartupPrincipalWriterDoesNotDeadlock(t *testing.T) {
	d := dbtest.Open(t)
	ctx, cancel := context.WithTimeout(dbtest.Seed(t.Context()), 10*time.Second)
	defer cancel()
	const tid = "53500000-0000-4000-8000-000000000001"
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,name) VALUES($1,'startup-locks','Startup locks')`, tid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Writer')`, tid); err != nil {
			return err
		}
		// Pin the real actor-resolution lock behind this writer. The seed's
		// System principal and its append-only events remain intact.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-system-actor:' || $1::text,0))`, tid); err != nil {
			return err
		}
		go func() { done <- New(d.App).RunTenant(ctx, tid, time.Now().UTC()) }()
		// Observe the real blocked actor resolution, not a scheduler-dependent
		// pause. pg_blocking_pids identifies this writer as the blocker.
		for {
			var blocked bool
			if err := d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND query LIKE '%aeon_authz_system_actor%'
 AND $1::int=ANY(pg_blocking_pids(pid)))`, int(tx.Conn().PgConn().PID())).Scan(&blocked); err != nil {
				return err
			}
			if blocked {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		var treeAvailable bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, tid).Scan(&treeAvailable); err != nil {
			return err
		}
		if !treeAvailable {
			t.Error("autopilot owns tree while actor resolution waits for the principal writer")
			return nil // release the writer so the background transaction can finish
		}
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title)
 SELECT $1,id,'PA-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, tid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

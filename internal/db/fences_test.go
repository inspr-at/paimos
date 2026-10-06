// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

// The literal is deliberate: lock-order proofs (AEON-735 handoff pickup) wait
// for this exact statement in pg_stat_activity. A fence spelled differently is
// never observed and the competitor dies on lock_timeout instead.
const canonicalTenantFence = `SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE`

func TestLockTenantBlocksOnTheCanonicalFenceStatement(t *testing.T) {
	f := newStatusFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	writer, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	if _, err = writer.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.tid); err != nil {
		t.Fatal(err)
	}
	if err = db.LockTenant(ctx, writer, f.tid); err != nil {
		t.Fatal(err)
	}
	var writerPID int
	if err = writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(ctx, f.d.App, f.tid, func(tx pgx.Tx) error {
			// The production endpoint bound; a fence that is never observed
			// fails here instead of hanging the test.
			if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true)`); err != nil {
				return err
			}
			return db.LockTenant(ctx, tx, f.tid)
		})
	}()
	for {
		var waiting bool
		if err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query=$2)`, writerPID, canonicalTenantFence).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("competitor finished before it was seen waiting on the canonical fence: %v", err)
		case <-ctx.Done():
			t.Fatal("competitor never waited on the canonical tenant fence statement")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("released competitor: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("competitor did not finish after the fence was released")
	}
}

func TestLockTenantRefusesACallerTenantOtherThanTheTransactionTenant(t *testing.T) {
	f := newStatusFixture(t)
	err := db.InTenant(t.Context(), f.d.App, f.tid, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, f.tid); err != nil {
			return err
		}
		if err := db.LockTenant(t.Context(), tx, "00000000-0000-4000-8000-000000000001"); err == nil {
			t.Fatal("foreign caller tenant accepted by the tenant fence")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

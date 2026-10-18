// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// Operator commands, importers and test holders fence a tenant from admin
// transactions that never entered tenant visibility. That fence must take the
// caller tenant's row with NO KEY UPDATE: conflicting with the canonical
// statement inside tenant visibility while leaving FK KEY SHARE locks free.
func TestLockTenantFencesTheCallerTenantWithoutTenantVisibility(t *testing.T) {
	f := newStatusFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	// A separate transaction keeps the holder clean: a refused statement
	// would otherwise abort it and hide the fence's own failure reason.
	blind, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = db.LockCurrentTree(ctx, blind)
	_ = blind.Rollback(ctx)
	if err == nil {
		t.Fatal("LockCurrentTree accepted a transaction without tenant visibility")
	}
	holder, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if err = db.LockTenant(ctx, holder, f.tid); err != nil {
		t.Fatalf("tenant fence without tenant visibility: %v", err)
	}
	if err = db.LockTree(ctx, holder, f.tid); err != nil {
		t.Fatalf("tenant/tree re-entry without tenant visibility: %v", err)
	}
	for _, mode := range []struct {
		lock      string
		conflicts bool
	}{{"KEY SHARE", false}, {"NO KEY UPDATE", true}} {
		probe, err := f.d.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR `+mode.lock+` NOWAIT`, f.tid)
		_ = probe.Rollback(ctx)
		var pgErr *pgconn.PgError
		locked := errors.As(err, &pgErr) && pgErr.Code == "55P03"
		if mode.conflicts && !locked {
			t.Fatalf("%s probe did not conflict with the operator tenant fence: %v", mode.lock, err)
		}
		if !mode.conflicts && err != nil {
			t.Fatalf("%s probe blocked by the operator tenant fence: %v", mode.lock, err)
		}
	}
	var holderPID int
	if err = holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(ctx, f.d.App, f.tid, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true)`); err != nil {
				return err
			}
			return db.LockTenant(ctx, tx, f.tid)
		})
	}()
	for {
		var waiting bool
		if err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query=$2)`, holderPID, canonicalTenantFence).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("canonical fence finished before waiting on the operator fence: %v", err)
		case <-ctx.Done():
			t.Fatal("canonical fence never waited on the operator tenant fence")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("released canonical fence: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("canonical fence did not finish after the operator fence was released")
	}
}

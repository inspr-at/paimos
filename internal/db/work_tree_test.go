// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
)

func TestWorkTreeTenantBeforePairingWithDerivationDisabled(t *testing.T) {
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
	var active bool
	if err = writer.QueryRow(ctx, `SELECT aeon_work_status_begin()`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("regression requires parent derivation disabled")
	}
	var writerPID int
	if err = writer.QueryRow(ctx, `SELECT pg_backend_pid() FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.tid).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	worker, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.tid); err != nil {
		t.Fatal(err)
	}
	var workerPID int
	if err = worker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&workerPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer worker.Rollback(context.Background())
		err := db.LockWorkTreeTx(ctx, worker)
		if err == nil {
			err = worker.Commit(ctx)
		}
		done <- err
	}()
	completed := false
	defer func() {
		cancel()
		writer.Rollback(context.Background())
		if !completed {
			<-done
		}
	}()
	// The database wait graph is the barrier: the lifecycle/worker fence has
	// reached the tenant row held by the access writer. No timing assumption.
	for {
		var blocked bool
		if err = f.d.Admin.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2))`, writerPID, workerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		runtime.Gosched()
	}
	var acquired bool
	if err = writer.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0))`).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("work fence held pairing while waiting for tenant: access writer would deadlock")
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		completed = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestNodeInsertSkipsEmptyReleaseReconciliation(t *testing.T) {
	// Risk: skipping release work must avoid the expensive function entirely
	// while retaining explicit backlog, structural updates and tenant isolation.
	f := newStatusFixture(t)
	var definition string
	if err := f.d.App.QueryRow(t.Context(), `SELECT pg_get_functiondef('aeon_work_reconcile_release_nodes(uuid[])'::regprocedure)`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "BEGIN\n") {
		t.Fatal("cannot instrument release reconciliation")
	}
	definition = strings.Replace(definition, "BEGIN\n", "BEGIN\n PERFORM nextval('aeon_test_release_reconcile_calls');\n", 1)
	if _, err := f.d.App.Exec(t.Context(), `CREATE SEQUENCE aeon_test_release_reconcile_calls MINVALUE 0 START 0;`+definition); err != nil {
		t.Fatal(err)
	}
	checkCalls := func(want int) {
		t.Helper()
		var got int
		if err := f.d.App.QueryRow(t.Context(), `SELECT last_value+is_called::integer FROM aeon_test_release_reconcile_calls`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("release reconciliation calls=%d, want %d", got, want)
		}
	}
	f.node("WK-1", "PRJ-1", "open")
	f.node("WK-2", "WK-1", "open")
	checkCalls(0)
	// A null release is real backlog intent, not an empty release projection.
	f.exec(`INSERT INTO work_parent_releases(tenant_id,parent_node_id,project_node_id,release_node_id)
 SELECT $1,id,project_id,NULL FROM nodes WHERE key='WK-1'`, f.tid)
	f.node("WK-3", "WK-1", "open")
	checkCalls(1)
	f.exec(`DELETE FROM work_parent_releases WHERE tenant_id=$1`, f.tid)
	f.exec(`UPDATE nodes SET parent_id=(SELECT id FROM nodes WHERE key='PRJ-1') WHERE key='WK-2'`)
	checkCalls(2)
	// Placements belonging to another tenant must not defeat the fast path.
	f.exec(`INSERT INTO work_parent_releases(tenant_id,parent_node_id,project_node_id,release_node_id)
 SELECT $1,id,project_id,NULL FROM nodes WHERE key='WK-1'`, f.tid)
	var other string
	if err := f.d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('other-release-tree','Other') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, other, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
 SELECT $1,id,'PRJ-1','Other project' FROM node_kinds WHERE slug='project'`, other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checkCalls(2)
}

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

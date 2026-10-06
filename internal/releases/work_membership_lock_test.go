// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const membershipTenantFence = "SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE"

type membershipFenceTrace struct{ ready chan uint32 }

func (m membershipFenceTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if q.SQL == membershipTenantFence {
		m.ready <- conn.PgConn().PID()
	}
	return ctx
}

func (membershipFenceTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestReleaseMembershipTenantBeforePairingWithDerivationDisabled(t *testing.T) {
	f := ticketSetup(t)
	leaf := f.existing("work", f.project, "Concurrent membership", "open")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	writer, err := f.db.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err = writer.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.person.TenantID); err != nil {
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
	if err = writer.QueryRow(ctx, `SELECT pg_backend_pid() FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.person.TenantID).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}

	ready := make(chan uint32, 1)
	cfg := f.db.App.Config()
	cfg.ConnConfig.Tracer = membershipFenceTrace{ready: ready}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); pool.Close() })
	mux := http.NewServeMux()
	New(pool).Mount(mux)
	r := httptest.NewRequest(http.MethodPost, f.membershipPath(), strings.NewReader(fmt.Sprintf(`{"expected_revision":1,"ticket_node_ids":[%q]}`, leaf)))
	r = r.WithContext(tenant.WithPrincipal(ctx, f.person))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		done <- w
	}()
	completed := false
	defer func() {
		cancel()
		_ = writer.Rollback(context.Background())
		if !completed {
			<-done
		}
	}()
	workerPID := dbtest.Await(t, ctx, ready)
	// The wait graph, including the exact tenant query, proves the membership
	// request overlaps the tenant-first writer. Timeouts only guard hangs.
	if err = dbtest.WaitForBlocked(ctx, f.db.Admin, int(workerPID), writerPID, membershipTenantFence); err != nil {
		t.Fatal(err)
	}
	var advisoryLocks int
	if err = f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted`, workerPID).Scan(&advisoryLocks); err != nil {
		t.Fatal(err)
	}
	if advisoryLocks != 0 {
		t.Errorf("membership holds %d advisory locks while waiting for tenant fence", advisoryLocks)
	}
	for _, key := range []string{"aeon-pairing:" + f.person.TenantID, f.person.TenantID} {
		var acquired bool
		if err = writer.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil {
			t.Fatal(err)
		}
		if !acquired {
			t.Fatalf("membership held %q while waiting for tenant: tenant-first writer would deadlock", key)
		}
	}
	if err = authz.LockProjectMutation(ctx, writer, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(ctx, `UPDATE nodes SET title='Tenant-first edit' WHERE id=$1`, leaf); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := dbtest.Await(t, ctx, done)
	completed = true
	out := membershipOK(t, w)
	if out.EventID < 1 || out.Walker.Revision != 2 || len(out.Walker.Tickets) != 1 || out.Walker.Tickets[0].NodeID != leaf || !out.Walker.Tickets[0].Included {
		t.Fatalf("membership did not commit after tenant-first writer: %+v", out)
	}
	f.tx(func(tx pgx.Tx) error {
		var title, release string
		err := tx.QueryRow(t.Context(), `SELECT n.title,j.release_node_id::text FROM nodes n JOIN journey_tickets j ON j.tenant_id=n.tenant_id AND j.ticket_node_id=n.id WHERE n.id=$1`, leaf).Scan(&title, &release)
		if err == nil && (title != "Tenant-first edit" || release != f.release) {
			t.Errorf("competing writes were not both retained: title=%q release=%q", title, release)
		}
		return err
	})
}

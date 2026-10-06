// SPDX-License-Identifier: AGPL-3.0-only
package journey_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type journeyTenantTrace struct{ ready chan uint32 }

func (b journeyTenantTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "FROM tenants") && strings.Contains(q.SQL, "FOR NO KEY UPDATE") {
		select {
		case b.ready <- conn.PgConn().PID():
		default:
		}
	}
	return ctx
}
func (journeyTenantTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A tenant-first writer must still be able to acquire pairing and tree while
// ordinary journey writes and permit renewal wait behind its tenant fence.
func TestJourneyWritesTenantBeforePairingWithDerivationDisabled(t *testing.T) {
	for _, operation := range []string{"profile", "renew_candidate", "renew_permit"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			project, release, candidate, _, view := renewalRelease(t, f)
			method, path, body := http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"personal","expected_revision":`+strconv.FormatInt(view.Revision, 10)+`}`
			if operation != "profile" {
				scope, old := journey.ScopeCandidate, candidate
				if operation == "renew_permit" {
					scope = journey.ScopeAccess
					f.setReleaseState(t, release, "access")
					old = f.grant(t, f.agent.ID, f.person.ID, scope, release)
					renewalChange(t, f, `INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1,$2,$3,'access',$4)`, f.tenant, project, release, old)
				}
				f.expire(t, old)
				fresh := f.grant(t, f.agent.ID, f.person.ID, scope, release)
				method, path, body = http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON(operation, view.Revision, "tenant-order", fresh, release, "")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			writer, err := f.db.App.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Rollback(context.Background()) }()
			if _, err = writer.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.tenant); err != nil {
				t.Fatal(err)
			}
			var active bool
			if err = writer.QueryRow(ctx, `SELECT aeon_work_status_begin()`).Scan(&active); err != nil || active {
				t.Fatalf("requires disabled parent derivation: active=%v err=%v", active, err)
			}
			var holder int
			if err = writer.QueryRow(ctx, `SELECT pg_backend_pid() FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.tenant).Scan(&holder); err != nil {
				t.Fatal(err)
			}
			ready := make(chan uint32, 2)
			cfg := f.db.App.Config()
			cfg.ConnConfig.Tracer = journeyTenantTrace{ready: ready}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			mux := http.NewServeMux()
			journey.New(pool).Mount(mux)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithPrincipal(ctx, f.person))
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
			waiter := dbtest.Await(t, ctx, ready)
			if err = dbtest.WaitForBlocked(ctx, f.db.Admin, int(waiter), holder, "FROM tenants"); err != nil {
				t.Fatal(err)
			}
			var locks int
			if err = f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted`, waiter).Scan(&locks); err != nil {
				t.Fatal(err)
			}
			if locks != 0 {
				t.Errorf("journey holds %d advisory locks before tenant fence", locks)
			}
			for _, key := range []string{"aeon-pairing:" + f.tenant, f.tenant} {
				var acquired bool
				if err = writer.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil {
					t.Fatal(err)
				}
				if !acquired {
					t.Errorf("journey took %q before tenant: concurrent permit renewal/completion would deadlock", key)
				}
			}
			if err = writer.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := dbtest.Await(t, ctx, done)
			completed = true
			if w.Code != http.StatusOK {
				t.Fatalf("valid write after fence: %d %s", w.Code, w.Body.String())
			}
			if operation != "profile" && f.events(t, "journey."+strings.TrimPrefix(operation, "renew_")+"_renewed") != 1 {
				t.Fatal("renewal did not commit exactly once")
			}
		})
	}
}

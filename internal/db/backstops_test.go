// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: simultaneous first starts insert or update the same role/database
// catalogue row before the migration lock, aborting startup. An uncommitted
// catalogue write and PostgreSQL's blocking graph establish both installers'
// overlap before release, for both an absent row and an existing tuning row.
func TestDatabaseBackstopsConcurrentFirstStarts(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "insert"
		if existing {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			d := dbtest.Open(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			setting := `ALTER ROLE ` + pgx.Identifier{d.Role}.Sanitize() + ` IN DATABASE ` +
				pgx.Identifier{d.Name}.Sanitize() + ` SET jit = 'off'`
			if existing {
				if _, err := d.Admin.Exec(ctx, setting); err != nil {
					t.Fatal(err)
				}
			}
			holder, err := d.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			type opened struct {
				pool *pgxpool.Pool
				err  error
			}
			done := make(chan opened, 2)
			pending := 0
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = holder.Rollback(cleanup)
				for pending > 0 {
					result := <-done
					pending--
					if result.pool != nil {
						result.pool.Close()
					}
				}
			}()
			if _, err := holder.Exec(ctx, setting); err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			start := func() {
				pending++
				go func() {
					pool, err := db.Open(ctx, d.AppURL)
					done <- opened{pool, err}
				}()
			}
			poll := time.NewTicker(10 * time.Millisecond)
			defer poll.Stop()
			waitForBlocked := func(want int) {
				t.Helper()
				for {
					var blocked int
					// The second start waits behind either the catalogue writer
					// or the first installer's advisory fence. Follow both paths.
					if err := d.Admin.QueryRow(ctx, `WITH RECURSIVE waiting(pid) AS (
						SELECT pid FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid))
						UNION
						SELECT a.pid FROM pg_stat_activity a JOIN waiting w ON w.pid=ANY(pg_blocking_pids(a.pid))
					) SELECT count(*) FROM waiting w JOIN pg_stat_activity a USING(pid)
					WHERE a.datname=current_database() AND a.usename=$2 AND a.wait_event_type='Lock'`, holderPID, d.Role).Scan(&blocked); err != nil {
						t.Fatalf("observe concurrent starts: %v", err)
					}
					if blocked == want {
						return
					}
					select {
					case result := <-done:
						pending--
						if result.pool != nil {
							result.pool.Close()
						}
						t.Fatalf("start finished before %d installers overlapped at the catalogue barrier: %v", want, result.err)
					case <-ctx.Done():
						t.Fatalf("catalogue barrier saw %d blocked starts, want %d: %v", blocked, want, ctx.Err())
					case <-poll.C: // Pacing only; the blocking graph proves overlap.
					}
				}
			}
			start()
			waitForBlocked(1)
			start()
			waitForBlocked(2)
			if err := holder.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			for pending > 0 {
				var result opened
				select {
				case result = <-done:
					pending--
				case <-ctx.Done():
					t.Fatalf("concurrent starts did not finish after catalogue release: %v", ctx.Err())
				}
				if result.err != nil {
					t.Errorf("concurrent first start: %v", result.err)
					continue
				}
				defer result.pool.Close()
				var statement, lock, idle string
				if err := result.pool.QueryRow(ctx, `SELECT current_setting('statement_timeout'), current_setting('lock_timeout'),
					current_setting('idle_in_transaction_session_timeout')`).Scan(&statement, &lock, &idle); err != nil {
					t.Fatal(err)
				}
				if statement != "30s" || lock != "5s" || idle != "30s" {
					t.Errorf("concurrent start budgets = %s/%s/%s, want 30s/5s/30s", statement, lock, idle)
				}
			}
			var settings []string
			if err := d.Admin.QueryRow(ctx, `SELECT s.setconfig FROM pg_db_role_setting s
				JOIN pg_database d ON d.oid=s.setdatabase JOIN pg_roles r ON r.oid=s.setrole
				WHERE d.datname=current_database() AND r.rolname=$1`, d.Role).Scan(&settings); err != nil {
				t.Fatal(err)
			}
			if existing && !slices.Contains(settings, "jit=off") {
				t.Errorf("concurrent starts lost existing role/database tuning: %v", settings)
			}
		})
	}
}

// Risk: SQL or abandoned transactions pin connections indefinitely, while a
// global timeout kills SSE or prevents delivery history from committing after
// vendor I/O. An idle backend's actual server timeout establishes the wait
// boundary, without sleeps or elapsed-time assertions.
func TestDatabaseBackstopsPreserveStreamsAndBackfill(t *testing.T) {
	d := dbtest.Open(t)
	pool, err := db.Open(t.Context(), d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	checkSettings := func(q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}, wantStatement, wantLock, wantIdle string) {
		t.Helper()
		var statement, lock, idle string
		if err := q.QueryRow(t.Context(), `SELECT current_setting('statement_timeout'), current_setting('lock_timeout'),
			current_setting('idle_in_transaction_session_timeout')`).Scan(&statement, &lock, &idle); err != nil {
			t.Fatal(err)
		}
		if statement != wantStatement || lock != wantLock || idle != wantIdle {
			t.Fatalf("SQL budgets = %s/%s/%s, want %s/%s/%s", statement, lock, idle, wantStatement, wantLock, wantIdle)
		}
	}
	checkSettings(pool, "30s", "5s", "30s")
	pool.Close()
	// A fresh ordinary-role connection inherits the database-specific defaults
	// without the Aeon pool. Explicit operator tuning survives the next Open.
	raw, err := pgx.Connect(t.Context(), d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	checkSettings(raw, "30s", "5s", "30s")
	raw.Close(t.Context())
	for _, setting := range []struct{ name, value string }{
		{"statement_timeout", "1s"}, {"lock_timeout", "100ms"}, {"idle_in_transaction_session_timeout", "1s"},
	} {
		if _, err := d.Admin.Exec(t.Context(), `ALTER ROLE `+pgx.Identifier{d.Role}.Sanitize()+` IN DATABASE `+
			pgx.Identifier{d.Name}.Sanitize()+` SET `+setting.name+` = '`+setting.value+`'`); err != nil {
			t.Fatal(err)
		}
	}
	pool, err = db.Open(t.Context(), d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	checkSettings(pool, "1s", "100ms", "1s")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	p := tenant.Principal{Kind: tenant.Person}
	if err := d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('backstops','Backstops') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	var project string
	if err := db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Reader') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'BACK-1','History' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, p.TenantID, p.ID, "admin")
	// A real competing row lock establishes contention before either timeout.
	holder, err := d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error {
		return db.LockTenant(ctx, tx, p.TenantID)
	})
	requirePGCode(t, err, "55P03", "lock timeout")
	err = db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
			return err
		}
		if err := db.SetLocalStatementTimeout(ctx, tx, 100*time.Millisecond); err != nil {
			return err
		}
		return db.LockTenant(ctx, tx, p.TenantID)
	})
	requirePGCode(t, err, "57014", "statement timeout")
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, rollback := range []bool{false, true} {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		retained := db.WithConnection(dbtest.Seed(ctx), pool, conn)
		sentinel := errors.New("rollback long SQL budget")
		err = db.InTenant(retained, pool, p.TenantID, func(tx pgx.Tx) error {
			if err := db.SetLocalStatementTimeout(ctx, tx, 2*time.Minute); err != nil {
				return err
			}
			checkSettings(tx, "2min", "100ms", "1s")
			if rollback {
				return sentinel
			}
			return nil
		})
		if rollback && !errors.Is(err, sentinel) || !rollback && err != nil {
			conn.Release()
			t.Fatalf("local budget transaction (rollback=%v): %v", rollback, err)
		}
		checkSettings(conn, "1s", "100ms", "1s")
		conn.Release()
	}

	// Exercise the actual HTTP stream, whose dedicated LISTEN copies this
	// pool's connection config. Establish connection before the idle timer.
	mux := http.NewServeMux()
	events.New(pool).Mount(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}))
	defer server.Close()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events/stream?after=latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	for range 6 {
		if !scanner.Scan() {
			t.Fatalf("stream did not become ready: %v", scanner.Err())
		}
	}
	waitForIdleBackstop(t, ctx, pool.Config().ConnConfig.Copy())
	if err := db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error {
		_, err := events.Append(ctx, tx, p, events.Change{Type: "test.backstop", After: map[string]bool{"alive": true}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	alive := false
	for scanner.Scan() {
		if scanner.Text() == "event: test.backstop" {
			alive = true
			break
		}
	}
	if !alive {
		t.Fatalf("stream died during an out-of-transaction wait: %v", scanner.Err())
	}
	resp.Body.Close()

	// Delay the vendor read past the same SQL/idle budgets. The observer rejects
	// a hidden auth transaction before the final revision-checked write proceeds.
	reader := backstopMetricsReader{wait: func() {
		var idle int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE usename=$1 AND state LIKE 'idle in transaction%'`, d.Role).Scan(&idle); err != nil {
			t.Fatal(err)
		}
		if idle != 0 {
			t.Fatalf("vendor I/O retained %d transactions", idle)
		}
		waitForIdleBackstop(t, ctx, pool.Config().ConnConfig.Copy())
	}}
	deliveryMux := http.NewServeMux()
	delivery.New(pool, crossreview.AppConfig{ID: "123", InstallationID: "456", TenantID: p.TenantID,
		Repository: "example/backstops", KeyFile: filepath.Join(t.TempDir(), "unused-fixture.pem")}, nil, reader, nil).Mount(deliveryMux)
	call := func(path, body string, out any) {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		if strings.HasSuffix(path, "/source") {
			r.Method = "PUT"
		}
		w := httptest.NewRecorder()
		deliveryMux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(ctx, p)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/projects/" + project + "/delivery/metrics"
	var source delivery.MetricSource
	call(base+"/source", `{"repository":"example/backstops","days":1}`, &source)
	if !source.AppConnected {
		t.Fatal("fixture does not configure the fake GitHub reader")
	}
	var step delivery.BackfillResult
	call(base+"/backfill", `{}`, &step)
	if step.State != "done" || step.Phase != "done" || step.Calls == 0 || step.Truncated {
		t.Fatalf("backfill did not finish after vendor wait: %+v", step)
	}
	var done bool
	if err := db.InTenant(dbtest.Seed(ctx), pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT backfill_done_at IS NOT NULL AND backfill_cursor->>'phase'='done'
			FROM delivery_metric_sources WHERE project_id=$1`, project).Scan(&done)
	}); err != nil || !done {
		t.Fatalf("backfill progress was not committed: done=%v err=%v", done, err)
	}
}

func requirePGCode(t *testing.T, err error, code, message string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code || !strings.Contains(pg.Message, message) {
		t.Fatalf("expected %s (%s), got %v", code, message, err)
	}
}

func waitForIdleBackstop(t *testing.T, ctx context.Context, cfg *pgx.ConnConfig) {
	t.Helper()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	_, err = conn.WaitForNotification(ctx)
	requirePGCode(t, err, "25P03", "idle-in-transaction timeout")
}

type backstopMetricsReader struct{ wait func() }

func (backstopMetricsReader) Pull(context.Context, int64) (delivery.Pull, error) {
	return delivery.Pull{}, errors.New("unexpected pull read")
}
func (backstopMetricsReader) OpenPulls(context.Context) ([]delivery.Pull, error) {
	return nil, errors.New("unexpected inventory read")
}
func (backstopMetricsReader) Group(context.Context, string, string, string) ([]delivery.Pull, error) {
	return nil, errors.New("unexpected group read")
}
func (g backstopMetricsReader) MetricsRead(_ context.Context, _ string, read func(func(string, any) error) error) error {
	g.wait()
	return read(func(path string, out any) error {
		var body any
		switch {
		case path == "":
			body = map[string]any{"full_name": "example/backstops", "default_branch": "main"}
		case strings.HasPrefix(path, "/actions/workflows/"):
			body = map[string]any{"total_count": 0, "workflow_runs": []any{}}
		case strings.HasPrefix(path, "/pulls?"):
			body = []any{}
		default:
			return fmt.Errorf("unexpected history read %s", path)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	})
}

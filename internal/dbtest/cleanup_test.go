// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestParallelCleanupRetries(t *testing.T) {
	// A neighbor and the cached template must survive all forced drops.
	neighbor := Open(t)
	t.Run("clones", func(t *testing.T) {
		for i := range 12 {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				d := Open(t)
				// A connection outside our pools must also be terminated by FORCE.
				lingering, err := pgx.Connect(t.Context(), d.AppURL)
				if err != nil {
					t.Fatal(err)
				}
				defer lingering.Close(context.Background())
				d.App.Close()
				d.Admin.Close()

				attempts := 0
				var first *pgx.Conn
				connect := func(ctx context.Context, maint string) (*pgx.Conn, error) {
					attempts++
					cfg, err := pgx.ParseConfig(maint)
					if err != nil {
						return nil, err
					}
					if attempts == 1 && i%3 != 0 {
						statement := "DROP DATABASE"
						if i%3 == 2 {
							statement = "DROP ROLE"
							// Keep this case focused on the role retry.
							if err := lingering.Close(ctx); err != nil {
								return nil, err
							}
						}
						cfg.Tracer = slowCleanupTracer{statement: statement}
					}
					conn, err := pgx.ConnectConfig(ctx, cfg)
					if err == nil && attempts == 1 {
						first = conn
						if i%3 == 0 {
							// Reproduce the original retry on a closed connection.
							err = conn.Close(ctx)
						}
					}
					return conn, err
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				if err := d.dropResources(ctx, 10*time.Second, connect); err != nil {
					t.Fatal(err)
				}
				if attempts < 2 || first == nil || !first.IsClosed() {
					t.Fatalf("attempts=%d, first connection was not discarded", attempts)
				}
				conn, err := pgx.Connect(ctx, d.maint)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close(context.Background())
				var database, role bool
				if err := conn.QueryRow(ctx, `SELECT
					EXISTS(SELECT 1 FROM pg_database WHERE datname=$1),
					EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$2)`,
					d.Name, d.Role).Scan(&database, &role); err != nil {
					t.Fatal(err)
				}
				if database || role {
					t.Fatalf("cleanup left database=%v role=%v", database, role)
				}
				// Close remains idempotent after a retry removed the resources.
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	if err := neighbor.App.Ping(t.Context()); err != nil {
		t.Fatalf("cleanup affected another database: %v", err)
	}
	// Another clone proves the template and its role were preserved too.
	Open(t)
}

// Give the first DROP an artificially short deadline without reducing later
// attempts' real server budget. pgx executes with that expired context, closing
// the connection just as a slow server does.
type slowCleanupTracer struct{ statement string }

func (s slowCleanupTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, s.statement) {
		slowCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		<-slowCtx.Done()
		return slowCtx
	}
	return ctx
}

func (slowCleanupTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestCleanupBudgetAndReporting(t *testing.T) {
	d := &DB{Name: "unused"}
	injected := errors.New("injected connection failure")
	ctx, cancel := context.WithTimeout(t.Context(), 350*time.Millisecond)
	defer cancel()
	attempts := 0
	err := d.dropResources(ctx, 50*time.Millisecond, func(attemptCtx context.Context, _ string) (*pgx.Conn, error) {
		attempts++
		deadline, ok := attemptCtx.Deadline()
		if !ok || time.Until(deadline) > 50*time.Millisecond {
			t.Error("attempt has no independent deadline")
		}
		return nil, injected
	})
	if attempts < 2 || !errors.Is(err, injected) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
	for _, ci := range []string{"true", ""} {
		t.Run("CI="+ci, func(t *testing.T) {
			t.Setenv("CI", ci)
			reporter := &cleanupRecorder{}
			before := cleanupFailures.Load()
			reportCleanup(reporter, nil)
			if cleanupFailures.Load() != before {
				t.Fatal("successful cleanup counted as a failure")
			}
			reportCleanup(reporter, err)
			if cleanupFailures.Load() != before+1 {
				t.Fatal("cleanup failure was not counted")
			}
			if ci != "" && (reporter.logs != 1 || reporter.errors != 0) {
				t.Fatalf("CI cleanup logs=%d errors=%d", reporter.logs, reporter.errors)
			}
			if ci == "" && (reporter.logs != 0 || reporter.errors != 1) {
				t.Fatalf("local cleanup logs=%d errors=%d", reporter.logs, reporter.errors)
			}
		})
	}
}

type cleanupRecorder struct{ logs, errors int }

func (r *cleanupRecorder) Logf(string, ...any)   { r.logs++ }
func (r *cleanupRecorder) Errorf(string, ...any) { r.errors++ }

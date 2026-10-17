// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run only on the approved rehearsal runner, never on the daily-driver Mac:
// remote-test.sh <worktree> ./internal/authz -run '^$' -bench '^BenchmarkKeyScopeLock$' -benchtime=1x -count=1 -v
// Latencies are observations, not pass/fail thresholds. The legacy arm uses the
// old exclusive admission mode and exact old upsert SQL, without a product flag.
func BenchmarkKeyScopeLock(b *testing.B) {
	d, p, node := scopeLockRehearsal(b)
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(b.Context(), p), 5*time.Minute)
	defer cancel()
	scopeLockExplain(b, ctx, d, p, node)
	for _, mode := range []string{"legacy_exclusive", "shared_only", "shared_debounced"} {
		b.Run(mode, func(b *testing.B) {
			const workers, requests = 10, 30
			usectx := ctx
			if mode == "legacy_exclusive" {
				usectx = db.WithKeyScopeChange(ctx, p.TenantID, p.KeyID)
			}
			use := func() error {
				return db.InTenant(usectx, d.App, p.TenantID, func(tx pgx.Tx) error {
					if mode != "shared_debounced" {
						tx = legacyScopeUsageTx{tx}
					}
					if err := RequireTx(usectx, tx, p, "nodes.read", Scope{}); err != nil {
						return err
					}
					// Ordinary bounded, tenant-scoped work while the key is in use.
					var n int
					return tx.QueryRow(usectx, `SELECT count(*) FROM (SELECT id FROM events
					 WHERE tenant_id=$1 AND node_id=$2 AND id>90000 AND type=ANY($3::text[])
					 ORDER BY id LIMIT 100) e`, p.TenantID, node, []string{"delivery.step", "delivery.item", "delivery.incident"}).Scan(&n)
				})
			}
			// Establish all ten pool connections and warm the initial statements
			// and first usage write outside the measurement.
			conns := make([]*pgxpool.Conn, 0, workers)
			for range workers {
				conn, err := d.App.Acquire(ctx)
				if err != nil {
					b.Fatal(err)
				}
				conns = append(conns, conn)
			}
			for _, conn := range conns {
				conn.Release()
			}
			for range workers {
				if err := use(); err != nil {
					b.Fatal(err)
				}
			}
			monitorCtx, stop := context.WithCancel(ctx)
			monitor := make(chan scopeWaitSample, 1)
			go func() {
				sample := scopeWaitSample{}
				for monitorCtx.Err() == nil {
					var advisory, row int
					if err := d.Admin.QueryRow(monitorCtx, `SELECT
					 count(*) FILTER (WHERE wait_event_type='Lock' AND wait_event='advisory'),
					 count(*) FILTER (WHERE wait_event_type='Lock' AND wait_event IN ('transactionid','tuple'))
					 FROM pg_stat_activity WHERE datname=current_database() AND usename=$1`, d.Role).Scan(&advisory, &row); err != nil {
						if monitorCtx.Err() == nil {
							sample.err = err
						}
						break
					}
					sample.samples++
					sample.advisory += advisory
					sample.row += row
					sample.maxAdvisory = max(sample.maxAdvisory, advisory)
					sample.maxRow = max(sample.maxRow, row)
				}
				monitor <- sample
			}()
			start := make(chan struct{})
			latencies, errs := make(chan time.Duration, workers*requests), make(chan error, workers)
			var group sync.WaitGroup
			b.ResetTimer()
			for range workers {
				group.Go(func() {
					<-start
					for range requests {
						at := time.Now()
						if err := use(); err != nil {
							errs <- err
							return
						}
						latencies <- time.Since(at)
					}
				})
			}
			close(start)
			group.Wait()
			b.StopTimer()
			stop()
			waits := <-monitor
			if waits.err != nil {
				b.Fatal(waits.err)
			}
			close(errs)
			for err := range errs {
				b.Fatal(err)
			}
			close(latencies)
			values := make([]time.Duration, 0, workers*requests)
			for v := range latencies {
				values = append(values, v)
			}
			slices.Sort(values)
			if len(values) != workers*requests || waits.samples == 0 {
				b.Fatal("incomplete rehearsal workload or lock sampling")
			}
			b.ReportMetric(float64(values[len(values)/2])/float64(time.Millisecond), "p50-ms")
			b.ReportMetric(float64(values[(len(values)*95-1)/100])/float64(time.Millisecond), "p95-ms")
			b.Logf("workers=%d requests=%d pg_stat_activity_samples=%d advisory_mean=%.3f advisory_max=%d row_mean=%.3f row_max=%d",
				workers, len(values), waits.samples, float64(waits.advisory)/float64(waits.samples), waits.maxAdvisory,
				float64(waits.row)/float64(waits.samples), waits.maxRow)
		})
	}
}

type scopeWaitSample struct {
	samples, advisory, row, maxAdvisory, maxRow int
	err                                         error
}

type legacyScopeUsageTx struct{ pgx.Tx }

func (tx legacyScopeUsageTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.HasPrefix(sql, "INSERT INTO agent_key_scope_usage") {
		sql = `INSERT INTO agent_key_scope_usage(tenant_id,key_id,scope,last_used_at)
	 VALUES($1::uuid,$2::uuid,$3,$4)
	 ON CONFLICT(tenant_id,key_id,scope) DO UPDATE SET last_used_at=EXCLUDED.last_used_at
	 WHERE agent_key_scope_usage.last_used_at<=EXCLUDED.last_used_at-interval '1 minute'`
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

func scopeLockRehearsal(b *testing.B) (*dbtest.DB, tenant.Principal, string) {
	b.Helper()
	d := dbtest.Open(b)
	ctx, cancel := context.WithTimeout(dbtest.Seed(b.Context()), 5*time.Minute)
	defer cancel()
	p := tenant.Principal{Kind: tenant.Agent, Scopes: []string{"nodes.read"}}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('key-lock-perf','Key lock rehearsal') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		b.Fatal(err)
	}
	p.KeyCreatorID = dbtest.KeyPerson(b, d.App, p.TenantID)
	node := "10000000-0000-4000-8000-000000000001"
	if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent','Rehearsal','{admin}') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
		 SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='admin'`, p.TenantID, p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id)
		 VALUES($1,$2,'Rehearsal','key-lock-rehearsal',decode(repeat('00',32),'hex'),ARRAY['nodes.read'],$3) RETURNING id::text`, p.TenantID, p.ID, p.KeyCreatorID).Scan(&p.KeyID); err != nil {
			return err
		}
		// One busy project has 40,000 events; 1,000 other identities share the
		// remainder. Delivery hints are sparse among ordinary activity, as in ppm.
		_, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after)
		 SELECT $1,$2,CASE WHEN g%5<2 THEN $3::uuid ELSE md5('rehearsal-node-'||(g%1000)::text)::uuid END,
		 CASE WHEN g%997=0 THEN 'delivery.step' WHEN g%991=0 THEN 'delivery.item'
		 WHEN g%983=0 THEN 'delivery.incident' WHEN g%3=0 THEN 'node.updated'
		 WHEN g%3=1 THEN 'comment.created' ELSE 'harness.heartbeat' END,
		 jsonb_build_object('fixture',true,'sequence',g,'padding',repeat('x',256))
		 FROM generate_series(1,100000) g`, p.TenantID, p.ID, node)
		return err
	}); err != nil {
		b.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `ANALYZE events`); err != nil {
		b.Fatal(err)
	}
	return d, p, node
}

func scopeLockExplain(b *testing.B, ctx context.Context, d *dbtest.DB, p tenant.Principal, node string) {
	b.Helper()
	var version string
	if err := d.Admin.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil {
		b.Fatal(err)
	}
	b.Logf("PostgreSQL=%s fixture_events=100000 hot_node_events=40000", version)
	// Exact flowHints query and service visibility, without a superuser's
	// shortcut around FORCE RLS. Explain both old cursor and near-tail cursor.
	for _, after := range []int64{0, 90000} {
		if err := db.InTenant(db.AllProjects(ctx, "AEON-1471 rehearsal EXPLAIN"), d.App, p.TenantID, func(tx pgx.Tx) error {
			var raw []byte
			if err := tx.QueryRow(ctx, `EXPLAIN(ANALYZE,BUFFERS,FORMAT JSON)
			 SELECT id,type,at,after FROM events WHERE type=ANY($1::text[]) AND node_id=$2 AND id>$3 ORDER BY id LIMIT $4`,
				[]string{"delivery.step", "delivery.item", "delivery.incident"}, node, after, 100).Scan(&raw); err != nil {
				return err
			}
			var result []map[string]any
			if err := json.Unmarshal(raw, &result); err != nil {
				return err
			}
			if len(result) != 1 {
				return fmt.Errorf("expected one EXPLAIN plan, got %d", len(result))
			}
			result[0]["Plan"] = scopeLockPlanSummary(result[0]["Plan"].(map[string]any))
			delete(result[0], "Triggers")
			summary, err := json.Marshal(result[0])
			if err != nil {
				return err
			}
			b.Logf("flowHints after=%d EXPLAIN=%s", after, summary)
			return nil
		}); err != nil {
			b.Fatal(fmt.Errorf("flow hints explain: %w", err))
		}
	}
}

func scopeLockPlanSummary(plan map[string]any) map[string]any {
	out := map[string]any{}
	for _, field := range []string{"Node Type", "Index Name", "Relation Name", "Actual Rows", "Actual Total Time", "Rows Removed by Filter", "Shared Hit Blocks", "Shared Read Blocks", "Sort Method"} {
		if value, ok := plan[field]; ok {
			out[field] = value
		}
	}
	if plans, ok := plan["Plans"].([]any); ok {
		children := make([]map[string]any, 0, len(plans))
		for _, child := range plans {
			children = append(children, scopeLockPlanSummary(child.(map[string]any)))
		}
		out["Plans"] = children
	}
	return out
}

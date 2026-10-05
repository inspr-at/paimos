// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Diagnose a failed request budget with one additional untimed request. Report
// statement labels only, never arguments or connection configuration.
func tracePlanningListRequest(t *testing.T, p tenant.Principal, path string) {
	t.Helper()
	cfg := appPool.Config()
	cfg.ConnConfig.Tracer = planningQueryTrace{t}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	status, body := callAs(t, New(pool, nil), &p, http.MethodGet, path, "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if !planningBulkPage(page) {
		t.Fatal("diagnostic request lost bulk usage totals")
	}
}

type planningQueryTrace struct{ t *testing.T }
type planningQueryTimeKey struct{}
type planningQueryTime struct {
	at    time.Time
	label string
}

func (q planningQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	label := strings.TrimSpace(strings.SplitN(strings.TrimSpace(data.SQL), "\n", 2)[0])
	if len(label) > 100 {
		label = label[:100]
	}
	return context.WithValue(ctx, planningQueryTimeKey{}, planningQueryTime{time.Now(), label})
}
func (q planningQueryTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if started, ok := ctx.Value(planningQueryTimeKey{}).(planningQueryTime); ok {
		q.t.Logf("planning diagnostic SQL %q: %s", started.label, time.Since(started.at))
	}
}

// Explain the actual installed scope function's facts query for this synthetic
// leaf-only fixture. Its SQL comes from pg_proc, rather than a second copy.
func tracePlanningWorkScope(t *testing.T, p tenant.Principal, project string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), p), 12*time.Second)
	defer cancel()
	err := db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			return err
		}
		var definition string
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN octet_length(prosrc)<=16384 THEN prosrc END FROM pg_proc WHERE oid='aeon_work_scope(uuid[])'::regprocedure`).Scan(&definition); err != nil {
			return err
		}
		_, query, ok := strings.Cut(definition, "RETURN QUERY")
		if !ok {
			return fmt.Errorf("scope query missing")
		}
		query, _, ok = strings.Cut(query, "END;")
		if !ok {
			return fmt.Errorf("scope end missing")
		}
		query = strings.ReplaceAll(query, "scope_roots", "$1::uuid[]")
		query = strings.ReplaceAll(query, "scope_nodes", "$2::uuid[]")
		query = strings.TrimSuffix(strings.TrimSpace(query), ";")
		var ids []string
		if err := tx.QueryRow(ctx, `SELECT array_agg(id::text) FROM (SELECT n.id FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.parent_id=$1 AND k.slug='work' AND n.deleted_at IS NULL ORDER BY n.id LIMIT 1001) bounded`, project).Scan(&ids); err != nil {
			return err
		}
		if len(ids) != 1000 {
			return fmt.Errorf("unexpected diagnostic fixture size")
		}
		var raw []byte
		if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, ids, ids).Scan(&raw); err != nil {
			return err
		}
		var documents []map[string]any
		if err := json.Unmarshal(raw, &documents); err != nil {
			return err
		}
		var visit func(map[string]any)
		visit = func(node map[string]any) {
			if node["Node Type"] == "Seq Scan" && node["Relation Name"] == "nodes" && node["Actual Loops"].(float64) > 1 {
				t.Errorf("scope facts repeated a full nodes scan %v times", node["Actual Loops"])
			}
			for _, child := range planChildren(node) {
				visit(child)
			}
		}
		if len(documents) != 1 {
			return fmt.Errorf("invalid scope plan")
		}
		visit(documents[0]["Plan"].(map[string]any))
		if os.Getenv("AEON_PLANNING_DIAGNOSTICS") != "1" && !t.Failed() {
			return nil
		}
		safe, err := json.Marshal(safePerformancePlan(documents))
		if err != nil {
			return err
		}
		t.Logf("scope facts diagnostic: %s", safe)
		return nil
	})
	if err != nil {
		t.Fatalf("scope diagnostic failed: %T", err)
	}
}

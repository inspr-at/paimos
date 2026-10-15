// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/activity"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/search"
)

// Risk (R11): a cache-default change can save little backend memory while
// increasing request latency. This explicit experiment reuses the compat E2E
// server, authentication, project and agent fixtures, with real forced RLS.
// Run through remote-test.sh with:
// ./internal/cli -run '^$' -bench '^BenchmarkBackendMemory$' -benchtime=1x -count=1
// This is a workload measurement, not a latency-threshold regression test.
func BenchmarkBackendMemory(b *testing.B) {
	if b.N != 1 {
		b.Fatal("use -benchtime=1x; the experiment already runs each configuration three times")
	}
	u, err := url.Parse(os.Getenv(dbtest.EnvDatabaseURL))
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != "55433" || !strings.HasPrefix(u.Path, "/aeon_run_") {
		b.Fatal("requires the approved remote-test.sh disposable database on loopback port 55433")
	}
	ctx, cancel := context.WithTimeout(b.Context(), 15*time.Minute)
	defer cancel()
	priorLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(priorLog) })
	d := dbtest.Open(b)
	if err := db.EnsureTenant(ctx, d.App, "aeon", "Aeon"); err != nil {
		b.Fatal(err)
	}
	var policies int
	var version, migration string
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_policies WHERE schemaname='public'`).Scan(&policies); err != nil {
		b.Fatal(err)
	}
	if policies < 462 {
		b.Fatalf("incomplete schema: %d policies", policies)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT version(),(SELECT max(version) FROM schema_migrations)`).Scan(&version, &migration); err != nil {
		b.Fatal(err)
	}
	// The helper lives only in this disposable database. SECURITY DEFINER lets
	// app connections inspect their own backend without granting a cluster-wide
	// monitoring role or bypassing RLS during the actual workload. Omit idents
	// (SQL text), credentials and payloads from all recorded output.
	if _, err := d.Admin.Exec(ctx, backendMemorySnapshotSQL); err != nil {
		b.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `GRANT EXECUTE ON FUNCTION public.aeon1382_memory() TO `+pgx.Identifier{d.Role}.Sanitize()); err != nil {
		b.Fatal(err)
	}
	seed := backendMemoryServer(b, d.App)
	worker := mintAgent(b, seed.URL, "memory-worker")
	dbtest.BindRole(b, d, worker.TenantID, worker.PrincipalID, "owner")
	seedProject(b, seed.URL, worker.Token)
	hc := &http.Client{Timeout: 10 * time.Second}
	jar, err := cookiejar.New(nil)
	if err != nil {
		b.Fatal(err)
	}
	person := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	backendMemoryRequest(b, ctx, person, seed.URL, "", "POST", "/api/auth/dev-login", `{"email":"admin@example.com","tenant":"aeon"}`, 200)
	request := func(method, path, body string, status int) []byte {
		return backendMemoryRequest(b, ctx, hc, seed.URL, worker.Token, method, path, body, status)
	}
	var kinds struct {
		Items []struct{ ID, Slug string }
	}
	if err := json.Unmarshal(request("GET", "/api/kinds", "", 200), &kinds); err != nil {
		b.Fatal(err)
	}
	var workKind string
	for _, kind := range kinds.Items {
		if kind.Slug == "work" {
			workKind = kind.ID
		}
	}
	var project struct{ ID string }
	if err := json.Unmarshal(request("GET", "/api/projects/lookup?ref=AEON", "", 200), &project); err != nil || project.ID == "" || workKind == "" {
		b.Fatal("missing project or work kind fixture")
	}
	var workIDs []string
	for i := range 80 {
		body := fmt.Sprintf(`{"kind_id":%q,"parent_id":%q,"title":"Memory workload %d","body":"Backend memory measurement fixture","fields":{"priority":"high"}}`, workKind, project.ID, i)
		var node struct{ ID string }
		if err := json.Unmarshal(request("POST", "/api/nodes", body, 201), &node); err != nil || node.ID == "" {
			b.Fatal("missing created work fixture")
		}
		workIDs = append(workIDs, node.ID)
	}
	seed.Close()
	d.App.Close()

	// Pin a serial stream to each of three app backends by retaining the other
	// two pool slots. Every sampled backend sees exactly the same request mix;
	// unused cold connections cannot dilute the per-backend comparison.
	variants := []struct{ name, params string }{
		{"cache_statement_512", ""},
		{"cache_describe", "default_query_exec_mode=cache_describe"},
		{"cache_statement_32", "statement_cache_capacity=32"},
	}
	for run := range 3 {
		// Rotate order to reduce systematic first-run/shared-buffer bias.
		for offset := range variants {
			variant := variants[(run+offset)%len(variants)]
			b.Run(fmt.Sprintf("%s/run_%d", variant.name, run+1), func(b *testing.B) {
				cfg, err := db.ParsePoolConfig(d.AppURL)
				if variant.params != "" {
					separator := "?"
					if strings.Contains(d.AppURL, "?") {
						separator = "&"
					}
					cfg, err = db.ParsePoolConfig(d.AppURL + separator + variant.params)
				}
				if err != nil {
					b.Fatal(err)
				}
				cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 3, 0, 0
				// Match db.Open's production OLTP session setting. This pool
				// must start fresh without running migrations on a sampled backend.
				cfg.ConnConfig.RuntimeParams["jit"] = "off"
				pool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					b.Fatal(err)
				}
				defer pool.Close()
				srv := backendMemoryServer(b, pool)
				defer srv.Close()
				held := make([]*pgxpool.Conn, 3)
				defer func() {
					for _, conn := range held {
						if conn != nil {
							conn.Release()
						}
					}
				}()
				for i := range held {
					held[i], err = pool.Acquire(ctx)
					if err != nil {
						b.Fatal(err)
					}
				}
				for slot := range held {
					pid := held[slot].Conn().PgConn().PID()
					cold := backendMemorySnapshot(b, ctx, held[slot].Conn())
					held[slot].Release()
					held[slot] = nil
					paths := []string{
						"/api/me",
						"/api/nodes?within=" + project.ID + "&limit=50",
						"/api/nodes/" + workIDs[0],
						"/api/search?q=Memory&limit=50",
						"/api/inbox/messages?wait_ms=0",
					}
					timings := make([][]float64, len(paths))
					for iteration := range 130 {
						for endpoint, path := range paths {
							if endpoint == 2 {
								path = "/api/nodes/" + workIDs[iteration%len(workIDs)]
							}
							start := time.Now()
							backendMemoryRequest(b, ctx, hc, srv.URL, worker.Token, "GET", path, "", 200)
							if iteration >= 30 {
								timings[endpoint] = append(timings[endpoint], float64(time.Since(start))/float64(time.Millisecond))
							}
						}
						if iteration < 30 {
							for _, path := range []string{"/api/kinds", "/api/projects", "/api/nodes/tree?root_id=" + project.ID + "&limit=100", "/api/nodes/" + workIDs[iteration] + "/activity?limit=50"} {
								backendMemoryRequest(b, ctx, hc, srv.URL, worker.Token, "GET", path, "", 200)
							}
							// Figures is a person-facing route; exercise its existing
							// session authority alongside the CLI's scoped agent reads.
							backendMemoryRequest(b, ctx, person, srv.URL, "", "GET", "/api/projects/"+project.ID+"/figures", "", 200)
							body := fmt.Sprintf(`{"title":"Memory workload %d"}`, iteration)
							backendMemoryRequest(b, ctx, hc, srv.URL, worker.Token, "PATCH", "/api/nodes/"+workIDs[iteration], body, 200)
						}
					}
					held[slot], err = pool.Acquire(ctx)
					if err != nil {
						b.Fatal(err)
					}
					if held[slot].Conn().PgConn().PID() != pid {
						b.Fatal("sampled backend was replaced during workload")
					}
					warm := backendMemorySnapshot(b, ctx, held[slot].Conn())
					latencies := make([]map[string]any, len(paths))
					for i, values := range timings {
						slices.Sort(values)
						latencies[i] = map[string]any{"endpoint": paths[i], "samples": len(values), "samples_ms": values, "p50_ms": values[(50*len(values)+99)/100-1], "p95_ms": values[(95*len(values)+99)/100-1]}
					}
					out, err := json.Marshal(map[string]any{"variant": variant.name, "run": run + 1, "backend": slot + 1, "pid": pid, "exec_mode": cfg.ConnConfig.DefaultQueryExecMode.String(), "statement_cache_capacity": cfg.ConnConfig.StatementCacheCapacity, "policies": policies, "postgres": version, "migration": migration, "cold": cold, "warm": warm, "latencies": latencies})
					if err != nil {
						b.Fatal(err)
					}
					fmt.Printf("AEON1382 %s\n", out)
				}
			})
		}
	}
}

func backendMemoryServer(b *testing.B, pool *pgxpool.Pool) *httptest.Server {
	b.Helper()
	am, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{9}, 32), PublicURL: "http://127.0.0.1", BootstrapTenantSlug: "aeon", BootstrapAdminEmail: "admin@example.com"}, pool)
	if err != nil {
		b.Fatal(err)
	}
	return httptest.NewServer((&httpapi.Server{Pool: pool, Modules: []httpapi.Module{am, nodes.New(pool, nodes.SQLWriter{}), activity.New(pool), search.New(pool, nil), modelregistry.New(pool), inbox.New(pool), harness.New(pool)}, Middleware: []func(http.Handler) http.Handler{am.Middleware}}).Handler())
}

func backendMemoryRequest(b *testing.B, ctx context.Context, hc *http.Client, base, token, method, path, body string, status int) []byte {
	b.Helper()
	req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
	if err != nil {
		b.Fatal("construct workload request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := hc.Do(req)
	if err != nil {
		b.Fatalf("workload transport failed: %s %s", method, path)
	}
	defer res.Body.Close()
	const limit = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(raw) > limit || res.StatusCode != status {
		b.Fatalf("workload response failed: %s %s status=%d expected=%d bytes=%d", method, path, res.StatusCode, status, len(raw))
	}
	return raw
}

func backendMemorySnapshot(b *testing.B, ctx context.Context, conn *pgx.Conn) json.RawMessage {
	b.Helper()
	var raw json.RawMessage
	// Never prepare the instrumentation queries: they must not pollute the
	// workload's retained-plan count or its statement cache.
	if err := conn.QueryRow(ctx, `SELECT public.aeon1382_memory() || jsonb_build_object('prepared_statements',(SELECT count(*) FROM pg_prepared_statements))`, pgx.QueryExecModeSimpleProtocol).Scan(&raw); err != nil {
		b.Fatal(err)
	}
	return raw
}

const backendMemorySnapshotSQL = `
CREATE FUNCTION public.aeon1382_memory() RETURNS jsonb
LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog AS $function$
WITH memory AS MATERIALIZED (SELECT * FROM pg_backend_memory_contexts),
classified AS (
    SELECT c.*, CASE
        WHEN EXISTS (SELECT FROM memory p WHERE p.name IN ('CachedPlan','CachedPlanSource','CachedPlanQuery') AND c.path[p.level]=p.path[p.level]) THEN 'plans'
        WHEN EXISTS (SELECT FROM memory p WHERE p.name='CacheMemoryContext' AND c.path[p.level]=p.path[p.level]) THEN 'catalog'
        ELSE 'other' END AS category
    FROM memory c
), grouped AS (
    SELECT category,name,count(*) AS contexts,sum(total_bytes) AS total_bytes,sum(used_bytes) AS used_bytes
    FROM classified GROUP BY category,name
)
SELECT jsonb_build_object(
    'total_bytes',(SELECT sum(total_bytes) FROM memory),
    'used_bytes',(SELECT sum(used_bytes) FROM memory),
    'plan_bytes',(SELECT coalesce(sum(total_bytes),0) FROM classified WHERE category='plans'),
    'catalog_bytes',(SELECT coalesce(sum(total_bytes),0) FROM classified WHERE category='catalog'),
    'cache_memory_context_bytes',(SELECT coalesce(sum(total_bytes),0) FROM memory WHERE name='CacheMemoryContext'),
    'contexts',(SELECT jsonb_agg(to_jsonb(g) ORDER BY total_bytes DESC,category,name) FROM grouped g)
)
$function$;
REVOKE ALL ON FUNCTION public.aeon1382_memory() FROM PUBLIC;
`

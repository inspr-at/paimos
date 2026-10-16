// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/decisiondesk"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: a new read projection or RLS plan silently increases every pooled
// backend's retained memory. Measure fresh, equally exercised sessions, never
// a pool average diluted by unused connections. No wall-time assertions.
func TestBackendMemoryBudget(t *testing.T) {
	runBackendMemoryWorkload(t, false)
}

// Transplant this file and the testing.TB helper changes onto a release's code
// to compare its actual embedded schema AND handlers. In particular release
// 134 did not contain 1353_person_steps.sql despite shipping migration 1354.
// remote-test.sh <worktree> ./internal/cli -run '^$' -bench '^BenchmarkBackendMemoryRelease$' -benchtime=1x -count=1
func BenchmarkBackendMemoryRelease(b *testing.B) {
	if b.N != 1 {
		b.Fatal("use -benchtime=1x")
	}
	u, err := url.Parse(os.Getenv(dbtest.EnvDatabaseURL))
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != "55433" || !strings.HasPrefix(u.Path, "/aeon_run_") {
		b.Fatal("requires remote-test.sh's disposable PostgreSQL 18 database")
	}
	runBackendMemoryWorkload(b, true)
}

func runBackendMemoryWorkload(t testing.TB, diagnostics bool) {
	t.Helper()
	// Keep dbtest's exit watcher independent of the runner's short-lived DB.
	if u, err := url.Parse(os.Getenv(dbtest.EnvDatabaseURL)); err == nil && strings.HasPrefix(u.Path, "/aeon_run_") {
		u.Path = "/postgres"
		t.Setenv(dbtest.EnvDatabaseURL, u.String())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	priorLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(priorLog) })
	d := dbtest.Open(t)
	if err := db.EnsureTenant(ctx, d.App, "aeon", "Aeon"); err != nil {
		t.Fatal(err)
	}
	var migration string
	var version, policies int
	if err := d.Admin.QueryRow(ctx, `SELECT current_setting('server_version_num')::int,(SELECT max(version) FROM schema_migrations),(SELECT count(*) FROM pg_policies WHERE schemaname='public')`).Scan(&version, &migration, &policies); err != nil {
		t.Fatal(err)
	}
	if version < 180000 {
		t.Fatal("memory budget requires PostgreSQL 18")
	}
	if _, err := d.Admin.Exec(ctx, backendMemorySnapshotSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `GRANT EXECUTE ON FUNCTION public.aeon1382_memory() TO `+pgx.Identifier{d.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	seed := backendMemoryServer(t, d.App)
	worker := mintAgent(t, seed.URL, "memory-budget")
	dbtest.BindRole(t, d, worker.TenantID, worker.PrincipalID, "owner")
	seedProject(t, seed.URL, worker.Token)
	hc := &http.Client{Timeout: 15 * time.Second}
	var kinds struct{ Items []struct{ ID, Slug string } }
	if err := json.Unmarshal(backendMemoryRequest(t, ctx, hc, seed.URL, worker.Token, "GET", "/api/kinds", "", 200), &kinds); err != nil {
		t.Fatal(err)
	}
	var workKind string
	for _, kind := range kinds.Items {
		if kind.Slug == "work" {
			workKind = kind.ID
		}
	}
	var project struct{ ID string }
	if err := json.Unmarshal(backendMemoryRequest(t, ctx, hc, seed.URL, worker.Token, "GET", "/api/projects/lookup?ref=AEON", "", 200), &project); err != nil || project.ID == "" || workKind == "" {
		t.Fatal("project/work fixture missing")
	}
	seed.Close()
	// A 1,500-leaf project, with synthetic 1 KiB bodies and pending person
	// checks, exercises tree scope, list sorting/facets and the new Desk branch.
	err := db.InTenant(dbtest.Seed(ctx), d.App, worker.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,body,parent_id,state,fields,human_check)
 SELECT $1,$2,'MEM-'||g,'Memory item '||g,repeat('x',1024),$3,
 CASE WHEN g%4=0 THEN 'done' ELSE 'new' END,
 jsonb_build_object('priority',CASE WHEN g%3=0 THEN 'high' ELSE 'medium' END),
 CASE WHEN g<=12 THEN 'Check synthetic result' END FROM generate_series(1,1500) g`, worker.TenantID, workKind, project.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `ANALYZE nodes; ANALYZE node_kinds; ANALYZE principals; ANALYZE role_bindings`); err != nil {
		t.Fatal(err)
	}
	var ticket string
	if err := d.Admin.QueryRow(ctx, `SELECT id::text FROM nodes WHERE key='MEM-1'`).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	var hasAdmission bool
	if err := d.Admin.QueryRow(ctx, `SELECT to_regclass('delivery_admission_bindings') IS NOT NULL`).Scan(&hasAdmission); err != nil {
		t.Fatal(err)
	}
	var item string
	if hasAdmission {
		err := db.InTenant(dbtest.Seed(ctx), d.App, worker.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository,ci_workflow,nightly_workflow,updated_at) VALUES($1,$2,'fixture/memory','ci.yml','nightly.yml',now())`, worker.TenantID, project.ID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO delivery_items(tenant_id,id,project_id,ticket_node_id,repository,pull_request,head_sha,state,state_since,owner,observation,updated_at)
 VALUES($1,gen_random_uuid(),$2,$3,'fixture/memory',1,repeat('b',40),'pushed',now(),'fixture',jsonb_build_object('target','main','base',repeat('a',40),'head',repeat('b',40),'open',true),now()) RETURNING id::text`, worker.TenantID, project.ID, ticket).Scan(&item); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO delivery_admission_bindings(tenant_id,repository,pull_request,project_id,ticket_node_id,target_branch,base_sha,head_sha,scope)
 VALUES($1,'fixture/memory',1,$2,$3,'main',repeat('a',40),repeat('b',40),repeat('a',40)||'..'||repeat('b',40))`, worker.TenantID, project.ID, ticket)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	d.App.Close()
	trace := &backendMemoryTrace{queries: make(map[string]backendMemoryQuery)}
	cfg, err := db.ParsePoolConfig(d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["jit"] = "off"
	cfg.ConnConfig.Tracer = trace
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 3, 0, 0
	if _, err := db.ConfigurePool(cfg); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer db.CloseNotificationConnections(pool)
	srv := backendMemoryServer(t, pool, decisiondesk.New(pool), delivery.New(pool, crossreview.AppConfig{}, nil, nil, nil))
	defer srv.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	person := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	backendMemoryRequest(t, ctx, person, srv.URL, "", "POST", "/api/auth/dev-login", `{"email":"admin@example.com","tenant":"aeon"}`, 200)
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
			t.Fatal(err)
		}
	}
	for slot := range 2 {
		pid := held[slot].Conn().PgConn().PID()
		cold := backendMemorySnapshot(t, ctx, held[slot].Conn())
		held[slot].Release()
		held[slot] = nil
		paths := []string{
			"/api/nodes?within=" + project.ID + "&limit=50&sort=-updated_at&facets=state,kind,priority,assignee",
			"/api/nodes?limit=50", "/api/nodes/" + ticket,
			"/api/kinds", "/api/inbox/messages?wait_ms=1",
			"/api/decision-desk/projection?limit=50",
		}
		if hasAdmission {
			paths = append(paths, "/api/delivery/"+item+"/admission")
		}
		for endpoint, path := range paths {
			trace.reset()
			values := []float64{}
			for range 16 {
				start := time.Now()
				client, token := hc, worker.Token
				if endpoint >= 5 {
					client, token = person, ""
				}
				backendMemoryRequest(t, ctx, client, srv.URL, token, "GET", path, "", 200)
				values = append(values, float64(time.Since(start))/float64(time.Millisecond))
			}
			held[slot], err = pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if held[slot].Conn().PgConn().PID() != pid {
				t.Fatal("sampled backend changed")
			}
			warm := backendMemorySnapshot(t, ctx, held[slot].Conn())
			var m struct {
				TotalBytes int64 `json:"total_bytes"`
			}
			if err := json.Unmarshal(warm, &m); err != nil {
				t.Fatal(err)
			}
			if m.TotalBytes <= 0 || m.TotalBytes > 48<<20 {
				t.Fatalf("backend %d endpoint %d: allocated context bytes=%d, budget=48 MiB", slot, endpoint, m.TotalBytes)
			}
			slices.Sort(values)
			out := map[string]any{"migration": migration, "postgres": version, "policies": policies, "backend": slot, "endpoint": endpoint, "cold": backendMemorySummary(t, cold), "idle": backendMemorySummary(t, warm), "p50_ms": values[7], "p95_ms": values[15]}
			if diagnostics {
				out["process"] = backendProcessMemory(t, ctx, pid)
				out["plans"] = trace.explain(t, ctx, held[2].Conn())
			}
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("AEON1444 %s", raw)
			held[slot].Release()
			held[slot] = nil
		}
		held[slot], err = pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
}

type backendMemoryQuery struct {
	sql  string
	args []any
}
type backendMemoryTrace struct {
	mu      sync.Mutex
	queries map[string]backendMemoryQuery
}

func (m *backendMemoryTrace) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries = make(map[string]backendMemoryQuery)
}
func (m *backendMemoryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	sql := strings.ToUpper(strings.TrimSpace(d.SQL))
	if len(m.queries) < 128 && (strings.HasPrefix(sql, "SELECT") || strings.HasPrefix(sql, "WITH")) {
		m.queries[d.SQL] = backendMemoryQuery{d.SQL, slices.Clone(d.Args)}
	}
	return ctx
}
func (*backendMemoryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (m *backendMemoryTrace) explain(t testing.TB, ctx context.Context, conn *pgx.Conn) []json.RawMessage {
	t.Helper()
	m.mu.Lock()
	queries := slices.Collect(func(yield func(backendMemoryQuery) bool) {
		for _, q := range m.queries {
			if !yield(q) {
				return
			}
		}
	})
	m.mu.Unlock()
	slices.SortFunc(queries, func(a, b backendMemoryQuery) int { return strings.Compare(a.sql, b.sql) })
	result := []json.RawMessage{}
	for _, q := range queries {
		// Explain only substantive request SELECTs; never re-execute transaction
		// setup, advisory locks, audit writes or the memory instrumentation itself.
		lower := strings.ToLower(q.sql)
		if !strings.Contains(lower, "from ") || strings.Contains(lower, "set_config") || strings.Contains(lower, "aeon1382") || strings.Contains(lower, "pg_") || strings.Contains(lower, "for update") || strings.Contains(lower, "for no key update") {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.tenant_id',(SELECT id::text FROM tenants WHERE slug='aeon'),true),set_config('aeon.visible_projects','*',true)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
			t.Fatal(err)
		}
		var raw json.RawMessage
		err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, MEMORY, FORMAT JSON, TIMING OFF) "+q.sql, append([]any{pgx.QueryExecModeDescribeExec}, q.args...)...).Scan(&raw)
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatalf("request plan diagnostics: %v", err)
		}
		var plan any
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		clean, err := json.Marshal(backendPlanSummary(q.sql, plan))
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, clean)
	}
	return result
}

// Only numeric process accounting leaves the approved test host. Darwin's
// resident/physical footprint includes different pages than Linux RssAnon;
// retain its label and never present it as Linux anonymous memory.
func backendProcessMemory(t testing.TB, ctx context.Context, pid uint32) map[string]uint64 {
	t.Helper()
	if goruntime.GOOS != "darwin" {
		return nil
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probe, "python3", "-c", `import ctypes,json,sys
lib=ctypes.CDLL('/usr/lib/libproc.dylib')
buf=(ctypes.c_uint64*32)()
if lib.proc_pid_rusage(int(sys.argv[1]),2,ctypes.byref(buf)) != 0: raise SystemExit('proc_pid_rusage failed')
print(json.dumps({'darwin_resident_bytes':buf[8],'darwin_phys_footprint_bytes':buf[9]}))`, strconv.FormatUint(uint64(pid), 10)).Output()
	if err != nil {
		t.Fatal("backend process memory probe failed")
	}
	var result map[string]uint64
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func backendMemorySummary(t testing.TB, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "contexts")
	return m
}

func backendPlanSummary(sql string, value any) map[string]any {
	hash := sha256.Sum256([]byte(sql))
	result := map[string]any{"query_sha256": fmt.Sprintf("%x", hash)}
	relations := map[string]float64{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, x := range v {
				walk(x)
			}
		case map[string]any:
			for _, key := range []string{"Memory Used", "Memory Allocated", "Sort Space Used", "Peak Memory Usage", "Temp Read Blocks", "Temp Written Blocks", "Execution Time", "Planning Time"} {
				if x, ok := v[key].(float64); ok {
					if prior, _ := result[key].(float64); x > prior {
						result[key] = x
					}
				}
			}
			if relation, ok := v["Relation Name"].(string); ok {
				loops, _ := v["Actual Loops"].(float64)
				relations[relation] += loops
			}
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(value)
	result["relation_scan_loops"] = relations
	return result
}

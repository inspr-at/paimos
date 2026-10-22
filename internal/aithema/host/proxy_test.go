// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/embedding"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprovider"
	"github.com/inspr-at/paimos/internal/tenant"
	"golang.org/x/net/websocket"
)

func TestProxyHTTPAndWebSocketCredentialOriginBoundary(t *testing.T) {
	var calls atomic.Int32
	var echoSecret atomic.Bool
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+mockCredential || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("proxy credential boundary")
		}
		if strings.HasSuffix(r.URL.Path, "/inline") {
			if r.Header.Get("Origin") != hostOrigin || r.URL.Query().Get("ticket") != "single-use-test-ticket" {
				t.Error("WS scope")
			}
			websocket.Handler(func(conn *websocket.Conn) {
				defer conn.Close()
				var message string
				if websocket.Message.Receive(conn, &message) != nil {
					return
				}
				websocket.Message.Send(conn, "echo:"+message)
			}).ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "upstream=bad")
		w.Header().Set("Content-Security-Policy", "default-src *")
		if echoSecret.Load() {
			json.NewEncoder(w).Encode(map[string]string{"echo": mockCredential})
			return
		}
		w.Write([]byte(`{"ok":true}`))
	})
	s := f.session()
	f.deliver()
	path := "/api/projects/" + f.project + "/aithema/sessions/" + s.Session + "/proxy/input"
	w := f.call("POST", path, map[string]string{"text": "hello"})
	if w.Code != 200 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Content-Security-Policy") != "" {
		t.Fatal("upstream headers escaped")
	}
	before := calls.Load()
	r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://foreign.example")
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.p))
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 403 || calls.Load() != before {
		t.Fatal("foreign origin reached service")
	}
	w = f.call("POST", strings.Replace(path, "/input", "/arbitrary-admin-operation", 1), map[string]any{})
	if w.Code != 403 {
		t.Fatal("arbitrary operation proxied")
	}
	echoSecret.Store(true)
	w = f.call("POST", path, map[string]any{})
	if w.Code != 502 || strings.Contains(w.Body.String(), mockCredential) {
		t.Fatal("upstream credential echo escaped")
	}
	echoSecret.Store(false)
	server := httptest.NewServer((&httpapi.Server{Modules: []httpapi.Module{f.m}, Middleware: []func(http.Handler) http.Handler{func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.p)))
		})
	}}}).Handler())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + strings.TrimSuffix(path, "input") + "inline?ticket=single-use-test-ticket"
	config, err := websocket.NewConfig(wsURL, hostOrigin)
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("Cookie", "browser-session=never-forward")
	conn, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("WebSocket upgrade failed: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := websocket.Message.Send(conn, "hello"); err != nil {
		t.Fatal(err)
	}
	var received string
	if err := websocket.Message.Receive(conn, &received); err != nil || received != "echo:hello" {
		t.Fatal("WebSocket did not transfer messages")
	}
}

func TestDeprovisionExactScopeIdempotencyAndFutureRefusal(t *testing.T) {
	f := newFixture(t, nil)
	s := f.session()
	key := newID()
	payload := map[string]string{"issuer": hostOrigin, "sub": f.p.ID, "idempotency_key": key}
	w := f.call("POST", "/api/aithema/deprovision", payload)
	if w.Code != 202 {
		t.Fatalf("deprovision: %d %s", w.Code, w.Body.String())
	}
	first := w.Body.String()
	w = f.call("POST", "/api/aithema/deprovision", payload)
	if w.Code != 202 || w.Body.String() != first {
		t.Fatal("deprovision retry changed result")
	}
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='aithema.deprovisioned'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("deprovision retry repeated audit effect")
	}
	state, err := f.m.Journal.Current(t.Context(), f.p.TenantID, f.project, s.Session)
	if err != nil || !state.Tombstone {
		t.Fatal("deprovision did not tombstone subject")
	}
	payload["sub"] = newID()
	w = f.call("POST", "/api/aithema/deprovision", payload)
	if w.Code != 409 {
		t.Fatal("deprovision key reused for foreign subject")
	}
	payload["issuer"] = "https://foreign.example"
	payload["idempotency_key"] = newID()
	w = f.call("POST", "/api/aithema/deprovision", payload)
	if w.Code != 400 {
		t.Fatal("foreign issuer accepted")
	}
	// Even retained person bindings cannot create a new processing session
	// for an explicitly deprovisioned issuer/tenant/subject tuple.
	w = f.call("POST", "/api/projects/"+f.project+"/aithema/sessions", map[string]any{"authorization": map[string]any{}, "host_mode": "review"})
	if w.Code != 409 {
		t.Fatal("deprovisioned subject recreated authority")
	}
}

func TestNativeHostEventsCursorAndTenantScope(t *testing.T) {
	t.Run("idle minute", measureIdleMinute)
	f := newFixture(t, nil)
	s := f.session()
	f.deliver()
	// A native source event includes public projection identity, not its body.
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'intake.source_recorded',jsonb_build_object('project_node_id',$3::text,'label','do-not-forward'))`, f.p.TenantID, f.p.ID, f.project); err != nil {
		t.Fatal(err)
	}
	ctx := db.AllProjects(t.Context(), "host test events")
	if err := f.m.queueHostEvents(ctx, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.m.queueHostEvents(ctx, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	var n int
	var raw []byte
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*),min(payload::text)::jsonb FROM aithema_callbacks WHERE tenant_id=$1 AND sid=$2 AND operation='host-event'`, f.p.TenantID, s.Session).Scan(&n, &raw); err != nil || n != 1 {
		t.Fatalf("event retry %d %v", n, err)
	}
	if strings.Contains(string(raw), "do-not-forward") {
		t.Fatal("host-event forwarded content")
	}
	var foreign string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('foreign','foreign') RETURNING id::text`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.m.DeliverOne(context.Background(), foreign); err != nil || ok {
		t.Fatal("foreign callback visible")
	}
}

// R5/R7: idle optimization must retain outbox/embedding claims and real
// heartbeats. Execute a 60-second-equivalent schedule against real Postgres;
// pass counts establish cadence without sleeps or elapsed-time assertions.
func measureIdleMinute(t *testing.T) {
	f := newFixture(t, nil)
	f.session()
	f.deliver()
	pool, counts := dbtest.CountedPool(t, f.db.App)
	f.m.Pool = pool
	harness.New(pool).Mount(f.mux)
	base := "/api/projects/" + f.project + "/harness-sessions"
	lease := "idle-measurement-worker-lease-0000001"
	w := f.call("POST", base, map[string]any{
		"agent_principal_id": f.plugin.ID, "harness": "codex", "host": "idle-host",
		"harness_session_ref": "idle-measurement-session-ref", "worker_lease": lease,
		"management_mode": "unmanaged", "role": "worker",
	})
	if w.Code != 201 {
		t.Fatalf("idle register: %d %s", w.Code, w.Body)
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	f.plugin.Scopes = []string{"harness.worker"}
	digest := sha256.Sum256([]byte("idle-synthetic-secret-only"))
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id)
		VALUES($1,$2,'idle fixture','idlemeasurement',$3,ARRAY['harness.worker'],$4) RETURNING id::text`,
		f.p.TenantID, f.plugin.ID, hex.EncodeToString(digest[:]), f.p.ID).Scan(&f.plugin.AuthKeyID); err != nil {
		t.Fatal(err)
	}
	beat := func() {
		r := httptest.NewRequest("POST", base+"/"+session.ID+"/heartbeat", strings.NewReader(`{"phase":"working","activity":"idle","activity_sequence":1}`))
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		r.Header.Set("Authorization", "Bearer aeon_idlemeasurement_idle-synthetic-secret-only")
		r = r.WithContext(tenant.WithPrincipal(r.Context(), f.plugin))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("idle heartbeat: %d %s", w.Code, w.Body)
		}
	}
	// Saturate the grace counter and consume initial host projections before
	// the measured interval; quiet beats must continue advancing liveness.
	for i := 0; i < 3; i++ {
		beat()
	}
	ctx := db.AllProjects(t.Context(), "idle measurement")
	if err := f.m.queueHostEvents(ctx, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	for {
		ok, err := f.m.DeliverOne(ctx, f.p.TenantID)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	provider := modelprovider.New(pool, bytes.Repeat([]byte{9}, 32))
	if _, err := provider.Save(f.ctx(), f.p, modelprovider.Write{Settings: modelprovider.Settings{
		Enabled: true, BaseURL: f.service.URL, ChatModel: "fixture-chat", EmbeddingModel: "fixture-embed", Features: modelprovider.Features{Embeddings: true},
	}}); err != nil {
		t.Fatal(err)
	}
	// The measured worker is idle: discard only fixture-generated index jobs.
	if _, err := f.db.Admin.Exec(t.Context(), `DELETE FROM node_embedding_jobs WHERE tenant_id=$1`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.m.queueHostEvents(ctx, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	worker := embedding.NewWorker(pool, nil, embedding.Options{Resolve: provider.Embeddings})
	counts.Reset()
	for second := 0; second < 60; second++ {
		// Match Run's tenant discovery and ten-pass reconciliation too.
		rows, err := pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if second%10 == 0 {
			if err := f.m.reconcile(ctx, f.p.TenantID); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.m.queueHostEvents(ctx, f.p.TenantID); err != nil {
			t.Fatal(err)
		}
		if ok, err := f.m.DeliverOne(ctx, f.p.TenantID); err != nil || ok {
			t.Fatalf("idle outbox: %t %v", ok, err)
		}
		if second%2 == 0 {
			if n, err := worker.ProcessOnce(ctx); err != nil || n != 0 {
				t.Fatalf("idle embedding: %d %v", n, err)
			}
		}
		if second%15 == 0 {
			beat()
		}
	}
	all, _ := counts.Count(func(string) bool { return true })
	application, _ := counts.Count(func(sql string) bool {
		return sql != "begin" && sql != "commit" && sql != "rollback" && !strings.HasPrefix(sql, "savepoint ") && !strings.HasPrefix(sql, "release savepoint ")
	})
	cursorCalls, cursorRows := counts.Count(func(sql string) bool { return strings.HasPrefix(sql, "UPDATE aithema_host_sessions SET event_cursor=") })
	providerReads, _ := counts.Count(func(sql string) bool {
		return strings.HasPrefix(sql, "SELECT ") && strings.Contains(sql, "FROM workspace_model_provider")
	})
	warningCalls, warningRows := counts.Count(func(sql string) bool {
		return strings.Contains(sql, "UPDATE harness_sessions SET missing_progress_beats=")
	})
	outboxClaims, _ := counts.Count(func(sql string) bool { return strings.HasPrefix(sql, "UPDATE aithema_callbacks SET attempts=") })
	t.Logf("IDLE-MINUTE: one tenant, one idle host, one idle worker; 60 host passes + 6 reconciliations + 30 embedding passes + 4 heartbeats; SQL=%d, SQL excluding transaction controls=%d, cursor statements/rows=%d/%d, provider reads=%d, warning statements/rows=%d/%d, due outbox claims=%d", all, application, cursorCalls, cursorRows, providerReads, warningCalls, warningRows, outboxClaims)
	if cursorCalls != 0 || cursorRows != 0 {
		t.Errorf("unchanged high water wrote cursor: calls=%d rows=%d", cursorCalls, cursorRows)
	}
	if warningCalls != 0 || warningRows != 0 {
		t.Errorf("saturated warning counter rewritten: calls=%d rows=%d", warningCalls, warningRows)
	}
	if outboxClaims != 60 {
		t.Errorf("due outbox claims=%d, want 60", outboxClaims)
	}
}

func TestQuotedNumericCapAndAlgorithmConfusionRefused(t *testing.T) {
	f := newFixture(t, nil)
	s := f.session()
	design := strings.Repeat("a", 64)
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) { c["iat"] = strconv.FormatInt(time.Now().Unix(), 10) },
		func(c map[string]any) { c["exp"] = strconv.FormatInt(time.Now().Unix()+300, 10) },
		func(c map[string]any) { c["exp"] = float64(9007199254740992) },
		func(c map[string]any) { c["aud"] = "https://foreign.example" },
		func(c map[string]any) { c["iat"] = time.Now().Unix() + 1 },
		func(c map[string]any) { c["jku"] = "https://foreign.example/key" },
	} {
		if _, err := verifyCap(f.cap(s.Session, design, mutate), f.s, hostOrigin, design, time.Now()); err == nil {
			t.Fatal("invalid preview claims accepted")
		}
	}
	token := f.cap(s.Session, design, nil)
	parts := strings.Split(token, ".")
	for _, header := range []string{`{"alg":"none","kid":"preview-key","typ":"JWT"}`, `{"alg":"HS256","kid":"preview-key","typ":"JWT"}`, `{"alg":"EdDSA","kid":"preview-key","kid":"other","typ":"JWT"}`, `{"alg":"EdDSA","kid":"preview-key","typ":"JWT","jku":"https://foreign.example"}`} {
		parts[0] = base64.RawURLEncoding.EncodeToString([]byte(header))
		if _, err := verifyCap(strings.Join(parts, "."), f.s, hostOrigin, design, time.Now()); err == nil {
			t.Fatal("algorithm or header confusion accepted")
		}
	}
}

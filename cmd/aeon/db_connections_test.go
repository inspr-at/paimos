// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"runtime"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Risk: the real server's worker/stream composition bypasses the main pool cap.
// HTTP opening frames and pg_stat_activity are readiness/cleanup barriers; no
// elapsed-time assertion or sleep substitutes for an established listener.
func TestServeConnectionOriginBudget(t *testing.T) {
	t.Setenv("AEON_ENV", "dev")
	t.Setenv("AEON_BOOTSTRAP_TENANT_SLUG", "connections")
	t.Setenv("AEON_BOOTSTRAP_ADMIN_EMAIL", "connections@example.test")
	for _, key := range []string{"AEON_SESSION_KEY_FILE", "AEON_OIDC_ISSUER", "AEON_OIDC_CLIENT_ID"} {
		t.Setenv(key, "")
	}
	d := dbtest.Open(t)
	d.App.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.EnsureTenant(ctx, pool, "connections-other", "Other"); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DatabaseURL: d.AppURL, Env: "dev", PublicURL: "https://127.0.0.1", BootstrapTenantSlug: "connections", BootstrapTenantName: "Connections", MessagingKey: bytes.Repeat([]byte{1}, 32), PhonePush: &config.PhonePushConfig{PublicKey: public, PrivateKey: private, Subject: "mailto:fixture@example.test"}}
	done := make(chan error, 1)
	go func() { done <- serveWithPool(ctx, cfg, ln, pool, 1) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(20 * time.Second):
			t.Error("server did not stop")
		}
	}()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	defer client.CloseIdleConnections()
	base := "http://" + ln.Addr().String()
	call := func(path string, body any) map[string]any {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatalf("%s: %d %s", path, res.StatusCode, raw)
		}
		out := map[string]any{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	identity := call("/api/auth/dev-login", map[string]string{"email": "connections@example.test"})
	loggedIn := identity["principal"].(map[string]any)
	tenantID, person := loggedIn["tenant_id"].(string), loggedIn["id"].(string)
	var project, agent, role string
	// A second tenant exercises the server's per-tenant routine dispatch loops.
	if err := db.EnsureTenant(ctx, pool, "connections-other", "Other"); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,'CONN-1',id,'Connection budget' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tenantID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Connection fixture') RETURNING id::text`, tenantID).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'connection-fixture','Connection fixture') RETURNING id::text`, tenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"events.subscribe", "agents.plan.read", "nodes.read", "run.read", "harness.worker"}
	for _, scope := range scopes {
		if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, tenantID, role, scope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tenantID, agent, role); err != nil {
		t.Fatal(err)
	}
	// Public fixture material only, never a configured credential.
	prefix := strings.ReplaceAll(tenantID, "-", "") + "0123456789abcdef"
	secret := "connection_fixture_1353"
	bearer := "aeon_" + prefix + "_" + secret
	hash := sha256.Sum256([]byte(secret))
	if _, err := d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture',$6, $3,$4,$5)`, tenantID, agent, hex.EncodeToString(hash[:]), scopes, person, prefix); err != nil {
		t.Fatal(err)
	}
	lease := "connection-fixture-lease-00000000000000000001353"
	session := call("/api/projects/"+project+"/harness-sessions", map[string]any{"agent_principal_id": agent, "harness": "codex", "host": "fixture", "harness_session_ref": "connection-fixture", "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox", "stop"}})["id"].(string)
	var bodies []io.ReadCloser
	closeStreams := func() {
		for _, body := range bodies {
			_ = body.Close()
		}
		bodies = nil
	}
	defer closeStreams()
	open := func(path string, agentRequest bool) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		if agentRequest {
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("X-Aeon-Worker-Lease", lease)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, res.Body)
		if res.StatusCode != 200 {
			raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
			t.Fatalf("%s: %d %s", path, res.StatusCode, raw)
		}
		scan := bufio.NewScanner(res.Body)
		for scan.Scan() {
			if scan.Text() == "" {
				return
			}
		}
		t.Fatalf("missing opening frame %s: %v", path, scan.Err())
	}
	for range 12 {
		open("/api/events/stream?after=latest", false)
		open("/api/inbox/stream", false)
		open("/api/projects/"+project+"/delivery/flow/stream", false)
	}
	for range 2 {
		open("/api/events/subscribe?topics=plan", true)
		open("/api/runs/queued/notifications", true)
	}
	open("/api/projects/"+project+"/harness-sessions/"+session+"/notifications", true)
	counts := func() map[string]int {
		t.Helper()
		rows, err := d.Admin.Query(ctx, `SELECT application_name,count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename=$1 GROUP BY application_name`, pool.Config().ConnConfig.User)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]int{}
		for rows.Next() {
			var name string
			var count int
			if err := rows.Scan(&name, &count); err != nil {
				t.Fatal(err)
			}
			got[name] = count
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	expected := map[string]int{"aeon-events-listener": 1, "aeon-agentruns-listener": 1, "aeon-inbox-routine-listener": 1, "aeon-routineexec-listener": 1, "aeon-events-admission": 1}
	var got map[string]int
	for ctx.Err() == nil {
		got = counts()
		ready := true
		for name, count := range expected {
			if got[name] != count {
				ready = false
			}
		}
		if ready {
			break
		}
		// Missing attribution is a regression, not a reason to wait until timeout.
		if got[""] > 0 {
			t.Fatalf("unattributed serve connections: %v", got)
		}
		runtime.Gosched()
	}
	if ctx.Err() != nil {
		t.Fatalf("origins never reached expected counts: %v", got)
	}
	for name, count := range got {
		if name == "aeon-main" {
			if count < 1 || int32(count) > db.DefaultMaxConns {
				t.Fatalf("main budget: %v", got)
			}
		} else if expected[name] != count {
			t.Fatalf("unexpected connection origin/count: %v", got)
		}
	}
	if len(got) != len(expected)+1 {
		t.Fatalf("incomplete origin inventory: %v", got)
	}
	t.Logf("41 client streams, real background loops and two tenants: %v", got)
	closeStreams()
	// Server-owned event/routine listeners remain; every client-only backend and
	// admission session must disappear after HTTP cancellation is processed.
	for ctx.Err() == nil {
		got = counts()
		if got["aeon-agentruns-listener"] == 0 && got["aeon-events-admission"] == 0 {
			break
		}
		runtime.Gosched()
	}
	if ctx.Err() != nil {
		t.Fatalf("stream connections leaked: %v", got)
	}
	t.Logf("after stream close: %v", got)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server shutdown timed out")
	}
	// Do not consume the shutdown result twice in the deferred safety cleanup.
	done <- nil
	cleanupCtx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	var dedicated int
	for cleanupCtx.Err() == nil {
		if err := d.Admin.QueryRow(cleanupCtx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename=$1 AND application_name LIKE 'aeon-%' AND application_name<>'aeon-main'`, pool.Config().ConnConfig.User).Scan(&dedicated); err != nil {
			t.Fatal(err)
		}
		if dedicated == 0 {
			break
		}
		runtime.Gosched()
	}
	if dedicated != 0 {
		t.Fatal(fmt.Sprintf("%d dedicated sessions survived server shutdown", dedicated))
	}
}

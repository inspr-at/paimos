// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: production ignores the deployment opt-in, or serves chat while it is off.
func TestServeChatEnabled(t *testing.T) {
	t.Setenv("AEON_ENV", "dev")
	t.Setenv("AEON_BOOTSTRAP_TENANT_SLUG", "chat-switch")
	t.Setenv("AEON_BOOTSTRAP_ADMIN_EMAIL", "chat-switch@example.test")
	for _, name := range []string{"AEON_SESSION_KEY_FILE", "AEON_OIDC_ISSUER", "AEON_OIDC_CLIENT_ID", "AEON_PUBLIC_URL"} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		name    string
		enabled bool
		status  int
	}{
		{"off", false, http.StatusNotFound},
		{"on", true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := dbtest.Open(t)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			cfg := config.Config{
				ChatEnabled: tc.enabled, DatabaseURL: fresh.AppURL, Env: "dev",
				PublicURL: "http://127.0.0.1", BootstrapTenantSlug: "chat-switch", BootstrapTenantName: "Chat switch",
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- serveListener(ctx, cfg, ln) }()
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
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
			defer client.CloseIdleConnections()
			base := "http://" + ln.Addr().String()
			call := func(path, payload string) (int, []byte) {
				t.Helper()
				resp, err := client.Post(base+path, "application/json", bytes.NewBufferString(payload))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				return resp.StatusCode, body
			}
			// The already-bound listener queues this request until Serve accepts it;
			// no sleep or elapsed-time assertion stands in for server readiness.
			if status, body := call("/api/auth/dev-login", `{"email":"chat-switch@example.test"}`); status != http.StatusOK {
				t.Fatalf("fixture login = %d %s", status, body)
			}
			var project string
			if err := fresh.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title)
				SELECT t.id,'CHAT-1',k.id,'Chat switch project' FROM tenants t
				JOIN node_kinds k ON k.tenant_id=t.id AND k.slug='project'
				WHERE t.slug='chat-switch' RETURNING id::text`).Scan(&project); err != nil {
				t.Fatal(err)
			}
			status, body := call("/api/projects/"+project+"/chat-roles", `{"kind":"lead","slot_key":"lead"}`)
			if status != tc.status {
				t.Fatalf("chat route = %d, want %d: %s", status, tc.status, body)
			}
			if tc.enabled {
				var role chat.Role
				if err := json.Unmarshal(body, &role); err != nil {
					t.Fatal(err)
				}
				if role.Contract != "chat-v1" || role.ID == "" || role.ProjectID != project || role.Kind != "lead" || role.SlotKey != "lead" || role.ConversationScope != "person_project" {
					t.Fatalf("unexpected chat role: %+v", role)
				}
			} else if !bytes.Contains(body, []byte(`"error":"not found"`)) {
				t.Fatalf("disabled chat did not return its feature-gate refusal: %s", body)
			}
		})
	}
}

// Risk: background work consumes every slot and takes the entire installation
// out of service although Postgres itself is healthy (AEON-995).
func TestServeBackgroundPoolHeadroom(t *testing.T) {
	t.Run("default workers", func(t *testing.T) { testServeBackgroundPoolHeadroom(t, false) })
	t.Run("configured optional workers", func(t *testing.T) { testServeBackgroundPoolHeadroom(t, true) })
}

func testServeBackgroundPoolHeadroom(t *testing.T, configured bool) {
	t.Setenv("AEON_ENV", "dev")
	fresh := dbtest.Open(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	poolCfg, err := pgxpool.ParseConfig(fresh.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	poolCfg.MaxConns = 4 // Old pgx default on the affected four-CPU host.
	poolCfg.MinConns = 0
	if _, err := db.ConfigurePool(poolCfg); err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{}, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	openBarrier := func() { releaseOnce.Do(func() { close(release) }) }
	defer openBarrier()
	var attempts atomic.Int64
	poolCfg.PrepareConn = func(ctx context.Context, _ *pgx.Conn) (bool, error) {
		if db.IsBackground(ctx) && attempts.Add(1) <= 2 {
			held <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return true, nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DatabaseURL: fresh.AppURL, Env: "dev", PublicURL: "http://127.0.0.1", BootstrapTenantSlug: "pool-headroom", BootstrapTenantName: "Pool headroom"}
	if configured {
		// Start optional lanes too, without any provisioned credentials or vendor
		// traffic. Empty queues and deliberately absent App files fail closed.
		if err := db.EnsureTenant(ctx, pool, cfg.BootstrapTenantSlug, cfg.BootstrapTenantName); err != nil {
			t.Fatal(err)
		}
		var tid string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug=$1`, cfg.BootstrapTenantSlug).Scan(&tid); err != nil {
			t.Fatal(err)
		}
		cfg.PublicURL = "https://127.0.0.1"
		cfg.MessagingKey = bytes.Repeat([]byte{1}, 32)
		cfg.LinkKey = bytes.Repeat([]byte{2}, 32)
		cfg.AttachedMessages, cfg.AttachedMessagesSingleInstance = true, true
		private, public, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			t.Fatal(err)
		}
		cfg.PhonePush = &config.PhonePushConfig{PublicKey: public, PrivateKey: private, Subject: "mailto:fixture@example.test"}
		cfg.DoctrineGuardKey = bytes.Repeat([]byte{3}, 32)
		cfg.DoctrineAppID, cfg.DoctrineInstallationID, cfg.DoctrineAppTenantID = "1", "1", tid
		cfg.DoctrineAppKeyRef, cfg.DoctrineGateLogin, cfg.DoctrineDCOAcknowledged = "fixture.pem", "fixture", true
		cfg.ReviewAppID, cfg.ReviewInstallationID, cfg.ReviewAppTenantID = "1", "1", tid
		cfg.ReviewAppKeyFile, cfg.ReviewAppRepository = filepath.Join(t.TempDir(), "unprovisioned-app.pem"), "fixture/fixture"
	}
	done := make(chan error, 1)
	go func() {
		done <- serveWithPool(ctx, cfg, ln, pool, 1)
	}()
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
	client := &http.Client{Timeout: 10 * time.Second}
	base := "http://" + ln.Addr().String()
	for range 2 {
		select {
		case <-held:
		case err := <-done:
			t.Fatalf("server stopped: %v", err)
		case <-time.After(20 * time.Second):
			t.Fatal("real workers did not reach acquisition barrier")
		}
	}
	if got := pool.Stat().AcquiredConns(); got != 2 {
		t.Fatalf("background held %d slots, want exactly 2 with the acquisition barrier", got)
	}
	resp, err := client.Get(base + "/api/ready")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readiness with real background workers = %d %s (acquired=%d max=%d)", resp.StatusCode, body, pool.Stat().AcquiredConns(), pool.Stat().MaxConns())
	}

	var ready struct {
		Pool db.PoolStats `json:"pool"`
	}
	if err := json.Unmarshal(body, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Pool.Max != 4 || ready.Pool.BackgroundLimit != 2 || ready.Pool.Waiting < 2 || ready.Pool.AcquireSamples == 0 {
		t.Fatalf("readiness detail: %+v", ready.Pool)
	}
	resp, err = client.Get(base + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte(`"db":"ok"`)) {
		t.Fatalf("health under background load: %d %s", resp.StatusCode, body)
	}
	// A foreground operation shares the same pool and still has capacity.
	foreground, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foreground.Release()
	openBarrier()
	work, stop := context.WithTimeout(db.Background(ctx), 10*time.Second)
	defer stop()
	if _, err := inbox.NewSweeper(pool).SweepLocked(work); err != nil {
		t.Fatal(err)
	}
	if _, err := knowledge.TagOnce(work, pool); err != nil {
		t.Fatal(err)
	}
	if s := db.Stats(pool); s.NestedAcquires != 0 {
		t.Fatalf("worker attempted nested pool acquire: %+v", s)
	}
}

func TestLoggerJSONInProd(t *testing.T) {
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "listening", 0)
	var buf bytes.Buffer
	if err := loggerHandler("prod", &buf).Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(buf.Bytes()) || !bytes.Contains(buf.Bytes(), []byte(`"msg":"listening"`)) {
		t.Fatalf("prod log is not json: %s", buf.String())
	}
	buf.Reset()
	if err := loggerHandler("dev", &buf).Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if json.Valid(bytes.TrimSpace(buf.Bytes())) {
		t.Fatalf("dev log looks like json: %s", buf.String())
	}
}

func TestPDFRenderConcurrencyBounds(t *testing.T) {
	for raw, want := range map[string]int{"": 1, "1": 1, "4": 4} {
		got, err := pdfRenderConcurrency(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %d, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"0", "5", "many"} {
		if _, err := pdfRenderConcurrency(raw); err == nil {
			t.Errorf("%q: expected rejection", raw)
		}
	}
}

func TestResolveWeb(t *testing.T) {
	fsys, err := resolveWeb(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if embedded, ok := web.Static(); ok {
		if fsys == nil {
			t.Fatal("expected embedded assets")
		}
		if _, err := fs.ReadFile(embedded, "index.html"); err != nil {
			t.Fatal(err)
		}
	} else if fsys != nil {
		t.Fatal("expected no embedded assets without the webembed tag")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys, err = resolveWeb(config.Config{WebDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(fsys, "index.html")
	if err != nil || string(b) != "hi" {
		t.Fatalf("read %q err %v", b, err)
	}
	if _, err := resolveWeb(config.Config{WebDir: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("expected missing web dir to fail")
	}
}

func TestServeShutdownAndBootstrap(t *testing.T) {
	// Auth reads AEON_ENV itself and no longer treats an unset value as dev.
	t.Setenv("AEON_ENV", "dev")
	fresh := dbtest.Open(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Observe and deny HTTP egress before DNS/connect; fixture probes use only
	// the exact inbound loopback listener. DNS is independently denied. The
	// Linux child-process test also denies Internet sockets for all transports.
	var outboundDials, dnsDials atomic.Int64
	oldTransport, oldResolver := http.DefaultTransport, net.DefaultResolver
	transport := oldTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != ln.Addr().String() {
			outboundDials.Add(1)
			return nil, errors.New("test denies outbound HTTP")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		dnsDials.Add(1)
		return nil, errors.New("test denies DNS")
	}}
	t.Cleanup(func() {
		http.DefaultTransport, net.DefaultResolver = oldTransport, oldResolver
		transport.CloseIdleConnections()
	})
	t.Setenv("AEON_DATABASE_URL", fresh.URL)
	t.Setenv("AEON_DOCTRINE_GUARD_KEY_FILE", filepath.Join(t.TempDir(), "not-provisioned"))
	loaded, err := config.FromEnv()
	if err != nil || loaded.DoctrineGuardKey != nil {
		t.Fatal("missing doctrine guard must not prevent server startup")
	}
	cfg := config.Config{
		DoctrineGuardKey:    loaded.DoctrineGuardKey,
		DatabaseURL:         fresh.URL,
		Env:                 "dev",
		PublicURL:           "http://127.0.0.1",
		BootstrapTenantSlug: "p02-boot",
		BootstrapTenantName: "Boot",
		PairingNixGuide: &config.PairingNixGuide{
			ModuleURL: "https://example.test/boot/module.nix", ServiceOption: "boot.agent.enable",
			Platforms: []string{"darwin"}, ServiceNote: "Review the paired service before activation.",
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serveListener(ctx, cfg, ln)
	}()

	base := "http://" + ln.Addr().String()
	var resp *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err = http.Get(base + "/api/health")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"db":"ok"`)) {
		t.Fatalf("health %d %s", resp.StatusCode, body)
	}
	ready, err := http.Get(base + "/api/ready")
	if err != nil {
		t.Fatal(err)
	}
	readyBody, _ := io.ReadAll(ready.Body)
	ready.Body.Close()
	if ready.StatusCode != http.StatusOK || !bytes.Contains(readyBody, []byte(`"status":"ready"`)) {
		t.Fatalf("ready %d %s", ready.StatusCode, readyBody)
	}

	guide, err := http.Get(base + "/agents/register-agent")
	if err != nil {
		t.Fatal(err)
	}
	guideBody, _ := io.ReadAll(guide.Body)
	guide.Body.Close()
	if guide.StatusCode != http.StatusOK || !bytes.Contains(guideBody, []byte("Connect your machine")) || !bytes.Contains(guideBody, []byte("pair --url")) || !bytes.Contains(guideBody, []byte("short code")) {
		t.Fatalf("pairing guide is not readable without JavaScript: %d", guide.StatusCode)
	}
	metadata, err := http.Get(base + "/api/agent-pairing/guide")
	if err != nil {
		t.Fatal(err)
	}
	metadataBody, _ := io.ReadAll(metadata.Body)
	metadata.Body.Close()
	if metadata.StatusCode != http.StatusOK || !bytes.Contains(metadataBody, []byte(`"protocol":"pairing-v1"`)) || !bytes.Contains(metadataBody, []byte(`"default_tenant_slug":"p02-boot"`)) {
		t.Fatalf("public pairing metadata: %d", metadata.StatusCode)
	}
	for _, body := range [][]byte{guideBody, metadataBody} {
		for _, want := range []string{cfg.PairingNixGuide.ModuleURL, cfg.PairingNixGuide.ServiceOption, cfg.PairingNixGuide.ServiceNote, "Service module: macOS only.", "from a reviewed release pin with pair"} {
			if !bytes.Contains(body, []byte(want)) {
				t.Fatalf("server did not publish configured Nix guidance: missing %s", want)
			}
		}
	}

	var name string
	if err := fresh.Admin.QueryRow(context.Background(), `SELECT name FROM tenants WHERE slug = 'p02-boot'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Boot" {
		t.Fatalf("bootstrap name %s", name)
	}

	cancel()
	sawDrain := false
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		probe, probeErr := http.Get(base + "/api/ready")
		if probeErr != nil {
			break
		}
		probeBody, _ := io.ReadAll(probe.Body)
		probe.Body.Close()
		if probe.StatusCode == http.StatusServiceUnavailable && bytes.Contains(probeBody, []byte(`"unavailable"`)) {
			during, duringErr := http.Get(base + "/api/health")
			if duringErr != nil {
				t.Fatal(duringErr)
			}
			duringBody, _ := io.ReadAll(during.Body)
			during.Body.Close()
			if during.StatusCode != http.StatusOK || !bytes.Contains(duringBody, []byte(`"db":"ok"`)) {
				t.Fatalf("health during drain %d %s", during.StatusCode, duringBody)
			}
			sawDrain = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawDrain {
		t.Fatal("readiness did not fail while the listener was still open")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if outboundDials.Load() != 0 || dnsDials.Load() != 0 {
		t.Fatalf("default server attempted outbound traffic: connect=%d DNS=%d", outboundDials.Load(), dnsDials.Load())
	}
}

func TestStopServingDrainsBeforeClose(t *testing.T) {
	var draining atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hold", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		if draining.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	base := "http://" + ln.Addr().String()

	held := make(chan int, 1)
	go func() {
		resp, getErr := http.Get(base + "/hold")
		if getErr != nil {
			held <- 0
			return
		}
		resp.Body.Close()
		held <- resp.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("hold did not start")
	}

	done := make(chan error, 1)
	go func() {
		done <- stopServing(srv, func() { draining.Store(true) }, 2*time.Second, 400*time.Millisecond)
	}()
	sawUnavailable := false
	probeUntil := time.Now().Add(time.Second)
	for time.Now().Before(probeUntil) {
		resp, getErr := http.Get(base + "/ready")
		if getErr != nil {
			t.Fatal(getErr)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			sawUnavailable = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawUnavailable {
		t.Fatal("readiness did not fail during drain")
	}
	select {
	case code := <-held:
		t.Fatalf("in-flight ended during drain with %d", code)
	default:
	}
	close(release)
	select {
	case code := <-held:
		if code != http.StatusNoContent {
			t.Fatalf("hold %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}

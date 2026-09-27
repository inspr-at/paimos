// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/web"
)

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
	cfg := config.Config{
		DatabaseURL:         fresh.URL,
		Env:                 "dev",
		PublicURL:           "http://127.0.0.1",
		BootstrapTenantSlug: "p02-boot",
		BootstrapTenantName: "Boot",
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

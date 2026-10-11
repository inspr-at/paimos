// SPDX-License-Identifier: AGPL-3.0-only

package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// AEON-1228 risk: a handler's own read deadline fires while net/http's
// background read is in flight. net/http then cancels the connection context
// but keeps serving the keep-alive connection, so every later request a reverse
// proxy sends over it starts cancelled. The server must close that connection.
// The barrier is the handler observing its cancelled context; no sleeps.
func TestCancelledConnectionContextClosesConnection(t *testing.T) {
	s := &Server{Modules: []Module{moduleFunc(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/trigger", func(w http.ResponseWriter, r *http.Request) {
			// GET has no body: the background read already runs. Arm a deadline
			// that fires during the handler, as a body-read helper would.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Millisecond))
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
				t.Error("connection context was not cancelled; hang guard")
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("GET /api/probe", func(w http.ResponseWriter, r *http.Request) {
			if err := r.Context().Err(); err != nil {
				http.Error(w, "context already cancelled at entry", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})}}
	server := httptest.NewUnstartedServer(s.Handler())
	server.Config.ReadHeaderTimeout = 10 * time.Second
	server.Config.IdleTimeout = time.Minute
	server.Start()
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second)) // hang guard only
	responses := bufio.NewReader(conn)
	if _, err := io.WriteString(conn, "GET /api/trigger HTTP/1.1\r\nHost: aeon.test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	first, err := http.ReadResponse(responses, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Body.Close()
	if _, err := io.WriteString(conn, "GET /api/probe HTTP/1.1\r\nHost: aeon.test\r\n\r\n"); err != nil {
		return // closed: safe
	}
	next, err := http.ReadResponse(responses, nil)
	if err != nil {
		return // closed: safe
	}
	_ = next.Body.Close()
	if next.StatusCode != http.StatusNoContent {
		t.Fatalf("next request on the reused connection: status %d (first response Connection=%q)", next.StatusCode, first.Header.Get("Connection"))
	}
}

// A request that still arrives on a cancelled connection keeps its
// cancellation, closes that connection and is logged with its request id.
func TestRequestOnCancelledConnectionClosesIt(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil).WithContext(ctx))
	if rec.Header().Get("Connection") != "close" {
		t.Fatalf("status %d Connection=%q, want the connection closed", rec.Code, rec.Header().Get("Connection"))
	}
	if !strings.Contains(logs.String(), "cancelled_on_arrival") || !strings.Contains(logs.String(), rec.Header().Get(requestIDHeader)) {
		t.Fatalf("missing dead-on-arrival log with request id: %s", logs.String())
	}
}

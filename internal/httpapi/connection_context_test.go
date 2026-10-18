// SPDX-License-Identifier: AGPL-3.0-only

package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// AEON-1228 risk: a handler's own read deadline fires while net/http's
// background read is in flight. net/http then cancels the connection context
// but keeps serving the keep-alive connection, so every later request a reverse
// proxy sends over it starts cancelled. The server must close that connection.
// The barrier is the handler observing its cancelled context; no sleeps.
func TestCancelledConnectionContextClosesConnection(t *testing.T) {
	for _, mode := range []string{"header", "implicit write", "flush", "header before cancel", "flush before cancel", "informational header", "no write"} {
		t.Run(mode, func(t *testing.T) {
			var probes atomic.Int32
			settled := make(chan http.ConnState, 4)
			s := &Server{Modules: []Module{moduleFunc(func(mux *http.ServeMux) {
				mux.HandleFunc("GET /api/trigger", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Length", "0")
					if mode == "header before cancel" {
						w.WriteHeader(http.StatusOK)
					}
					if mode == "flush before cancel" {
						w.(http.Flusher).Flush()
					}
					if mode == "informational header" {
						w.WriteHeader(http.StatusEarlyHints)
					}
					controller := http.NewResponseController(w)
					if err := controller.SetReadDeadline(time.Now()); err != nil {
						t.Error(err)
						return
					}
					defer controller.SetReadDeadline(time.Time{})
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
						t.Error("connection context was not cancelled; hang guard")
						return
					}
					switch mode {
					case "header", "informational header":
						w.WriteHeader(http.StatusNoContent)
					case "implicit write":
						w.Header().Set("Content-Length", "2")
						_, _ = io.WriteString(w, "ok")
					case "flush":
						if err := controller.Flush(); err != nil {
							t.Error(err)
						}
					}
				})
				mux.HandleFunc("GET /api/probe", func(w http.ResponseWriter, r *http.Request) {
					probes.Add(1)
					if err := r.Context().Err(); err != nil {
						t.Error("fresh connection started cancelled")
					}
					w.WriteHeader(http.StatusNoContent)
				})
			})}}
			server := httptest.NewUnstartedServer(s.Handler())
			server.Config.ReadHeaderTimeout = 10 * time.Second
			server.Config.IdleTimeout = time.Minute
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateIdle || state == http.StateClosed {
					settled <- state
				}
			}
			server.Start()
			defer server.Close()
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(15 * time.Second)) // hang guard only
			responses := bufio.NewReader(conn)
			if _, err := io.WriteString(conn, "GET /api/trigger HTTP/1.1\r\nHost: aeon.test\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			var first *http.Response
			for {
				first, err = http.ReadResponse(responses, nil)
				if err != nil {
					t.Fatal(err)
				}
				if first.StatusCode >= 200 {
					break
				}
				_ = first.Body.Close()
			}
			if _, err := io.Copy(io.Discard, first.Body); err != nil {
				t.Fatal(err)
			}
			_ = first.Body.Close()
			want := http.StatusOK
			if mode == "header" || mode == "informational header" {
				want = http.StatusNoContent
			}
			if first.StatusCode != want {
				t.Fatalf("status %d, want %d", first.StatusCode, want)
			}
			if mode != "header before cancel" && mode != "flush before cancel" && !first.Close {
				t.Fatal("response committed after cancellation has no Connection: close")
			}
			// A flushed response can arrive before the handler observes
			// cancellation. Wait for its terminal connection state: StateIdle
			// fails immediately if the guard is missing; no timeout proves
			// closure.
			select {
			case state := <-settled:
				if state != http.StateClosed {
					t.Fatalf("cancelled connection returned to %s instead of closing", state)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("cancelled connection never reached a terminal state; hang guard")
			}
			// Observe the server's EOF before sending any more bytes. A probe
			// racing the server's close can turn that close into a TCP reset on
			// macOS; reading here proves the server closes without client input.
			_, err = http.ReadResponse(responses, nil)
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("cancelled connection was not closed: %v", err)
			}
			if probes.Load() != 0 {
				t.Fatal("cancelled connection served another handler")
			}
			fresh, err := server.Client().Get(server.URL + "/api/probe")
			if err != nil {
				t.Fatal(err)
			}
			_ = fresh.Body.Close()
			if fresh.StatusCode != http.StatusNoContent || probes.Load() != 1 {
				t.Fatal("fresh connection failed")
			}
		})
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
		t.Error("cancelled arrival ran downstream auth/handler")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil).WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" || rec.Header().Get("Connection") != "close" {
		t.Fatalf("status %d Connection=%q, want the connection closed", rec.Code, rec.Header().Get("Connection"))
	}
	if !strings.Contains(logs.String(), "cancelled_on_arrival") || !strings.Contains(logs.String(), rec.Header().Get(requestIDHeader)) {
		t.Fatalf("missing dead-on-arrival log with request id: %s", logs.String())
	}
	// A real net/http connection with a cancelled base context must emit the
	// retry response and close, rather than only mutating a recorder's headers.
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnContext = func(context.Context, net.Conn) context.Context { return ctx }
	server.Start()
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second)) // hang guard only
	if _, err := io.WriteString(conn, "GET /api/me HTTP/1.1\r\nHost: aeon.test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	responses := bufio.NewReader(conn)
	response, err := http.ReadResponse(responses, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "1" || !response.Close {
		t.Fatalf("cancelled arrival: status %d headers %v", response.StatusCode, response.Header)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	_, err = responses.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("cancelled arrival connection remained open: %v", err)
	}
}

// AEON-1477: a client abort during auth or the desk read must be logged as
// 499, while an internal deadline and a genuine failure remain server faults.
func TestClientCancellationIs499InsteadOfServerFailure(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		name   string
		err    error
		cancel bool
		status int
		level  string
	}{
		{"cancelled error", errors.Join(errors.New("private fixture text"), context.Canceled), false, 499, "INFO"},
		{"cancelled request", errors.New("private fixture text"), true, 499, "INFO"},
		{"internal deadline", context.DeadlineExceeded, false, 500, "ERROR"},
		{"server failure", errors.New("private fixture text"), false, 500, "ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.cancel {
					cancel() // The handler is already running: this is not a stale connection arrival.
				}
				WriteInternalError(w, r, "authenticate", tc.err, "internal")
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/decision-desk/projection", nil).WithContext(ctx))
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			if strings.Contains(logs.String(), "private fixture text") {
				t.Fatal("private error text reached logs")
			}
			var requests, failures int
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				switch record["msg"] {
				case "request":
					requests++
					if record["status"] != float64(tc.status) || record["status_class"] != statusClass(tc.status) {
						t.Fatalf("incorrect request status: %v", record)
					}
				case "request failed":
					failures++
					if record["level"] != tc.level || record["step"] != "authenticate" {
						t.Fatalf("incorrect failure level or step: %v", record)
					}
				}
			}
			if requests != 1 || failures != 1 {
				t.Fatalf("request/failure records = %d/%d, want 1/1", requests, failures)
			}
		})
	}
}

// AEON-1228: failures outside auth must remain diagnosable without logging
// URL capabilities, headers, response bodies or driver error text.
func TestHandlerFailureLoggingIsContentFree(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const private = "private-fixture-value"
	s := &Server{Modules: []Module{moduleFunc(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/public/{token}", func(w http.ResponseWriter, r *http.Request) {
			LogFailure(r, "handler_query", &pgconn.PgError{Code: "57014", Message: private, Detail: "SELECT '" + private + "'"})
			WriteError(w, http.StatusBadGateway, private)
		})
		mux.HandleFunc("GET /api/unlogged", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotImplemented)
		})
	})}}
	for _, tc := range []struct{ path, step, class, route string }{
		{"/api/public/" + private + "?q=" + private, "handler_query", "postgres_57014", "GET /api/public/{token}"},
		{"/api/unlogged", "handler", "internal", "GET /api/unlogged"},
	} {
		logs.Reset()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+private)
		req.Header.Set("X-Request-ID", private)
		req.AddCookie(&http.Cookie{Name: "fixture", Value: private})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if strings.Contains(logs.String(), private) || strings.Contains(logs.String(), "SELECT") {
			t.Fatal("failure logging disclosed request, response or SQL content")
		}
		var failures int
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			if record["msg"] != "request failed" {
				continue
			}
			failures++
			if record["level"] != "ERROR" || record["step"] != tc.step || record["error_class"] != tc.class || record["route"] != tc.route || record["request_id"] != rec.Header().Get(requestIDHeader) {
				t.Fatalf("wrong failure diagnostics: %v", record)
			}
		}
		if failures != 1 {
			t.Fatalf("failure records %d, want one", failures)
		}
	}
}

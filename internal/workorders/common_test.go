// SPDX-License-Identifier: AGPL-3.0-only

package workorders

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type bodyDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *bodyDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestBufferBodyBoundsAndDecode(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		tooLarge   bool
		invalid    bool
	}{
		{name: "valid", body: `{"reason":"process_exited"}`},
		{name: "at limit", body: `{}` + strings.Repeat(" ", (1<<20)-2)},
		{name: "over limit", body: `{}` + strings.Repeat(" ", (1<<20)-1), tooLarge: true},
		{name: "unknown field", body: `{"unknown":true}`, invalid: true},
		{name: "trailing JSON", body: `{} {}`, invalid: true},
		{name: "trailing garbage", body: `{} x`, invalid: true},
		{name: "empty", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			w := &bodyDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			err := BufferBody(w, r)
			if tc.tooLarge {
				var failure *Error
				if !errors.As(err, &failure) || failure.Status != 400 || failure.Message != "invalid JSON" {
					t.Fatalf("oversized body: %v", err)
				}
				if len(w.deadlines) != 1 || w.deadlines[0].IsZero() {
					t.Fatalf("failed read lost its drain deadline: %v", w.deadlines)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.body != "" && (len(w.deadlines) != 2 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero()) {
				t.Fatalf("successful read must set then clear deadline: %v", w.deadlines)
			}
			var body struct {
				Reason string `json:"reason"`
			}
			if err := Decode(r, &body); (err != nil) != tc.invalid {
				t.Fatalf("buffered decode=%v, want invalid=%t", err, tc.invalid)
			}
			if tc.name == "valid" && body.Reason != "process_exited" {
				t.Fatalf("body changed: %+v", body)
			}
		})
	}
}

type expiredBodyDeadlineWriter struct {
	http.ResponseWriter
	requested chan<- time.Time
}

func (w *expiredBodyDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	// Inject expiry at the actual network connection, avoiding a sleep or
	// wall-clock performance assertion. An absent deadline cannot unblock Read.
	w.requested <- deadline
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
}

func TestEndpointBodyNetworkDeadline(t *testing.T) {
	mux := http.NewServeMux()
	// A body timeout must return before any database connection is needed.
	mux.HandleFunc("POST /api/work-orders", Endpoint(nil, "work_orders.write", false, 201, nil))
	handler := (&httpapi.Server{Mux: mux}).Handler()
	p := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", TenantID: "22222222-2222-4222-8222-222222222222", Kind: tenant.Person}
	assertEndpointBodyNetworkDeadline(t, handler, p, "POST /api/work-orders")
}

func TestEndpointPositionedBodyNetworkDeadline(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", Kind: tenant.Person}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('body-deadline','Body deadline') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"GET /api/runs", "GET /api/harness-sessions"} {
		t.Run(pattern, func(t *testing.T) {
			mux := http.NewServeMux()
			// The watermark uses the real database, but the endpoint must time
			// out before opening its own transaction or calling its handler.
			mux.HandleFunc(pattern, Endpoint(nil, "", false, http.StatusOK, nil))
			handler := (&httpapi.Server{
				Mux: mux, Middleware: []func(http.Handler) http.Handler{events.PositionMiddleware(d.App)},
			}).Handler()
			assertEndpointBodyNetworkDeadline(t, handler, p, pattern)
		})
	}
}

func assertEndpointBodyNetworkDeadline(t *testing.T, handler http.Handler, p tenant.Principal, pattern string) {
	t.Helper()
	requested := make(chan time.Time, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The production middleware wraps this writer; its Unwrap must let the
		// response controller reach the connection deadline implementation.
		handler.ServeHTTP(&expiredBodyDeadlineWriter{w, requested}, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Announce a body but send no bytes. Only the server's read deadline ends
	// its read; the client deadline is merely a guard against a hung test.
	if _, err := io.WriteString(conn, pattern+" HTTP/1.1\r\nHost: test\r\nContent-Length: 1\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("stalled body did not receive a timeout response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(body), "request body read timed out") {
		t.Fatalf("timeout response=%d %s", response.StatusCode, body)
	}
	select {
	case deadline := <-requested:
		if deadline.IsZero() {
			t.Fatal("body read did not install a deadline")
		}
	default:
		t.Fatal("body read did not reach the network deadline controller")
	}
	select {
	case <-requested:
		t.Fatal("failed read cleared its drain deadline")
	default:
	}
}

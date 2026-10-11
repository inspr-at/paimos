// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type bodyDeadlineWriter struct {
	http.ResponseWriter
	deadlines []time.Time
	expire    bool
}

func (w *bodyDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *bodyDeadlineWriter) SetReadDeadline(at time.Time) error {
	w.deadlines = append(w.deadlines, at)
	if w.expire && !at.IsZero() {
		return http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
	}
	return nil
}

// Risk: a slow body in any migrated timeout/size class poisons keep-alive.
// Expiry is injected at the actual connection; timeouts only guard against hangs.
func TestSlowBodyRouteClassesCloseConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   int64
		timeout time.Duration
	}{
		{"ordinary JSON", 1 << 20, 10 * time.Second},
		{"work endpoint", 1 << 20, 30 * time.Second},
		{"webhook", 2 << 20, 5 * time.Second},
		{"delivery JSON", 16 << 10, 5 * time.Second},
		{"delivery routing", 16 << 10, 15 * time.Second},
		{"portal", 32 << 10, 10 * time.Second},
		{"theme", 8 << 10, 10 * time.Second},
		{"benefit retry", 4096, 5 * time.Second},
		{"chat", 8192, 3 * time.Second},
		{"schedule preview", 1 << 20, 3 * time.Second},
		{"workstation action", 2 << 20, 15 * time.Second},
		{"workstation marking", 4096, 15 * time.Second},
		{"daemon step-up", 256, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{Modules: []Module{moduleFunc(func(mux *http.ServeMux) {
				mux.HandleFunc("POST /api/body", func(w http.ResponseWriter, r *http.Request) {
					injected := &bodyDeadlineWriter{ResponseWriter: w, expire: true}
					_, err := ReadRequestBody(injected, r, tc.limit, tc.timeout)
					var network net.Error
					if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &network) && network.Timeout()) {
						t.Errorf("body did not fail for expiry: %v", err)
					}
					if len(injected.deadlines) != 1 || injected.deadlines[0].IsZero() {
						t.Errorf("failed read reset its deadline: %v", injected.deadlines)
					}
					WriteError(w, http.StatusRequestTimeout, "body timed out")
				})
				mux.HandleFunc("GET /api/probe", func(w http.ResponseWriter, r *http.Request) {
					if r.Context().Err() != nil {
						t.Error("fresh request was cancelled")
					}
					w.WriteHeader(http.StatusNoContent)
				})
			})}}
			server := httptest.NewUnstartedServer(s.Handler())
			server.Config.ConnContext = s.ConnContext
			server.Start()
			defer server.Close()
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err = io.WriteString(conn, "POST /api/body HTTP/1.1\r\nHost: aeon.test\r\nContent-Length: 10\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 408 || !response.Close {
				t.Fatalf("failed body response status=%d close=%v", response.StatusCode, response.Close)
			}
			if _, err = io.Copy(io.Discard, response.Body); err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			_, _ = io.WriteString(conn, "GET /api/probe HTTP/1.1\r\nHost: aeon.test\r\n\r\n")
			if _, err = http.ReadResponse(reader, nil); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("connection remained reusable: %v", err)
			}
			fresh, err := server.Client().Get(server.URL + "/api/probe")
			if err != nil {
				t.Fatal(err)
			}
			_ = fresh.Body.Close()
			if fresh.StatusCode != 204 {
				t.Fatalf("fresh connection: %d", fresh.StatusCode)
			}
		})
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingBody) Close() error             { return nil }

func TestBodyBoundsAndGuardWithLiveContext(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		readError  bool
	}{
		{"complete", "abcd", false}, {"too large", "abcde", true}, {"read error", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/body", strings.NewReader(tc.body))
			if tc.name == "read error" {
				r.Body = failingBody{}
			}
			w := &bodyDeadlineWriter{ResponseWriter: httptest.NewRecorder()}
			handler := requestIDMiddleware(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				raw, err := ReadRequestBody(out, r, 4, 10*time.Second)
				if tc.readError {
					if tc.name == "too large" {
						var size *http.MaxBytesError
						if !errors.As(err, &size) {
							t.Fatalf("wrong size error: %v", err)
						}
					} else if !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("wrong read error: %v", err)
					}
					WriteError(out, 400, "bad body")
				} else {
					if err != nil || string(raw) != tc.body {
						t.Fatalf("read=%q error=%v", raw, err)
					}
					out.WriteHeader(204)
				}
			}))
			handler.ServeHTTP(w, r)
			if r.Context().Err() != nil {
				t.Fatal("fixture must retain live context")
			}
			want := 2
			if tc.readError {
				want = 1
			}
			if len(w.deadlines) != want {
				t.Fatalf("deadline calls %v", w.deadlines)
			}
			if tc.readError && w.Header().Get("Connection") != "close" {
				t.Fatal("live-context read error bypassed connection guard")
			}
			if !tc.readError && (!w.deadlines[1].IsZero() || w.Header().Get("Connection") != "") {
				t.Fatal("complete read cannot resume keep-alive")
			}
		})
	}
}

type startedBody struct {
	io.ReadCloser
	entered chan struct{}
	once    sync.Once
}

func (b *startedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	return b.ReadCloser.Read(p)
}
func TestBodyCancellationWithoutTransportJoinsReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &startedBody{ReadCloser: reader, entered: make(chan struct{})}
	r := httptest.NewRequest("POST", "/api/body", nil).WithContext(ctx)
	r.Body = body
	w := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() { _, err := ReadRequestBody(w, r, 4, time.Minute); done <- err }()
	select {
	case <-body.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("read never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled reader did not join")
	}
	if w.Header().Get("Connection") != "close" {
		t.Fatal("cancelled body did not close connection")
	}
	if _, err := writer.Write([]byte("late")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("reader was not closed: %v", err)
	}
}

// Risk: caller deadlines are extended or a local writer's deadline error is
// ignored before admission. No body read occurs when deadline setup fails.
func TestBodyDeadlineSetupAndEarlierContext(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	r := httptest.NewRequest("POST", "/", strings.NewReader("a")).WithContext(ctx)
	w := &bodyDeadlineWriter{ResponseWriter: httptest.NewRecorder()}
	if _, err := ReadRequestBody(w, r, 4, time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	if !w.deadlines[0].Equal(deadline) {
		t.Fatalf("extended caller deadline: %v", w.deadlines)
	}
	r = httptest.NewRequest("POST", "/", nil)
	r.Body = failingBody{}
	denied := &deadlineFailureWriter{ResponseRecorder: httptest.NewRecorder()}
	if _, err := ReadRequestBody(denied, r, 4, time.Minute); !errors.Is(err, ErrBodyDeadlineUnavailable) {
		t.Fatalf("wrong deadline setup error: %v", err)
	}
	if denied.Header().Get("Connection") != "close" {
		t.Fatal("unread body was left reusable")
	}
}

type deadlineFailureWriter struct{ *httptest.ResponseRecorder }

func (*deadlineFailureWriter) SetReadDeadline(time.Time) error { return os.ErrPermission }

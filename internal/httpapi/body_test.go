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
	"sync/atomic"
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
			var probes atomic.Int32
			settled := make(chan http.ConnState, 4)
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
					probes.Add(1)
					if r.Context().Err() != nil {
						t.Error("fresh request was cancelled")
					}
					w.WriteHeader(http.StatusNoContent)
				})
			})}}
			server := httptest.NewUnstartedServer(s.Handler())
			server.Config.ConnContext = s.ConnContext
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
			// The server must close without returning the failed connection
			// to keep-alive. StateIdle fails immediately; the timeout guards
			// against hangs and is never evidence of closure.
			select {
			case state := <-settled:
				if state != http.StateClosed {
					t.Fatalf("failed body connection returned to %s instead of closing", state)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("failed body connection never reached a terminal state; hang guard")
			}
			// Read the server's EOF before sending any more bytes. A probe
			// racing the close can cause a TCP reset even though the server
			// correctly refused keep-alive.
			if _, err = http.ReadResponse(reader, nil); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("connection remained reusable: %v", err)
			}
			if probes.Load() != 0 {
				t.Fatal("failed body connection served another handler")
			}
			fresh, err := server.Client().Get(server.URL + "/api/probe")
			if err != nil {
				t.Fatal(err)
			}
			_ = fresh.Body.Close()
			if fresh.StatusCode != 204 || probes.Load() != 1 {
				t.Fatalf("fresh connection: status=%d probes=%d", fresh.StatusCode, probes.Load())
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
	// Reusing a buffered body must not re-arm a socket deadline while
	// net/http has already resumed its background keep-alive reader.
	buffered := httptest.NewRequest("POST", "/", strings.NewReader("abcd"))
	bufferedWriter := &bodyDeadlineWriter{ResponseWriter: httptest.NewRecorder()}
	for i := 0; i < 2; i++ {
		if err := BufferRequestBody(bufferedWriter, buffered, 4); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := io.ReadAll(buffered.Body)
	if err != nil || string(raw) != "abcd" || len(bufferedWriter.deadlines) != 2 {
		t.Fatalf("buffered reuse changed body/deadlines: %q, %v, %v", raw, err, bufferedWriter.deadlines)
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

// Risk: dribbling input bypasses the budget, transport failures look like 400,
// or a body guard shortens SSE response lifetimes. Expiry is injected after
// observed socket reads; wall-clock limits are hang guards only.
func TestRequestBodyDeadlineResponsesAndStreaming(t *testing.T) {
	t.Run("dribbling sender", func(t *testing.T) {
		progress := make(chan int)
		deadline := make(chan time.Time, 2)
		var admitted atomic.Bool
		server := httptest.NewServer(RequestBodyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = &dribblingRequestBody{ReadCloser: r.Body, progress: progress, controller: http.NewResponseController(w)}
			observed := &observedDeadlineWriter{ResponseWriter: w, deadlines: deadline}
			var in any
			if err := DecodeJSON(observed, r, 16, &in); err != nil {
				WriteError(w, 400, "invalid JSON") // The guard supplies 408.
				return
			}
			admitted.Store(true)
			w.WriteHeader(204)
		})))
		defer server.Close()
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: aeon.test\r\nContent-Length: 10\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		select {
		case at := <-deadline:
			if at.IsZero() {
				t.Fatal("no transport deadline installed")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("body read did not install deadline; hang guard")
		}
		for i, b := range []byte(`{"x`) {
			if _, err := conn.Write([]byte{b}); err != nil {
				t.Fatal(err)
			}
			select {
			case n := <-progress:
				if n != i+1 {
					t.Fatalf("dribble progress=%d", n)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("body read made no progress; hang guard")
			}
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 408 || !response.Close || admitted.Load() {
			t.Fatalf("status=%d close=%v admitted=%v", response.StatusCode, response.Close, admitted.Load())
		}
		if len(deadline) != 0 {
			t.Fatal("failed read cleared or extended its deadline")
		}
	})
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"normal", `{"x":1}`, 204},
		{"too large", `{"x":1234567890123456789}`, 413},
		{"unknown field", `{"y":1}`, 400},
		{"trailing value", `{"x":1} {}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(RequestBodyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					X int `json:"x"`
				}
				if err := DecodeJSON(w, r, 16, &in); err != nil {
					WriteError(w, 400, "invalid JSON")
					return
				}
				w.WriteHeader(204)
			})))
			defer server.Close()
			response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"progressing upload", "abcd", 204},
		{"upload remainder exceeds limit", "abcdefghi", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var admitted atomic.Bool
			server := httptest.NewServer(RequestBodyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				finish, err := BeginRequestBodyStream(w, r, 8, UploadBodyReadTimeout)
				if err != nil {
					t.Error(err)
					WriteError(w, 400, "upload failed")
					return
				}
				defer finish(false)
				var prefix [2]byte
				if _, err := io.ReadFull(r.Body, prefix[:]); err != nil {
					t.Error(err)
					WriteError(w, 400, "upload failed")
					return
				}
				if err := finish(true); err != nil {
					WriteError(w, 400, "upload failed")
					return
				}
				admitted.Store(true)
				w.WriteHeader(204)
			})))
			defer server.Close()
			response, err := server.Client().Post(server.URL, "application/octet-stream", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.want || admitted.Load() != (tc.want == 204) {
				t.Fatalf("status=%d admitted=%v", response.StatusCode, admitted.Load())
			}
		})
	}
	t.Run("SSE", func(t *testing.T) {
		continueStream := make(chan struct{})
		defer close(continueStream)
		s := &Server{Modules: []Module{moduleFunc(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /api/events/stream", func(w http.ResponseWriter, r *http.Request) {
				if _, hasDeadline := r.Context().Deadline(); hasDeadline {
					t.Error("SSE retained the ordinary request deadline")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: first\n\n")
				_ = http.NewResponseController(w).Flush()
				select {
				case <-continueStream:
					_, _ = io.WriteString(w, "data: second\n\n")
				case <-r.Context().Done():
					t.Error("SSE context cancelled during a live response")
				}
			})
		})}}
		server := httptest.NewUnstartedServer(s.Handler())
		server.Config.ReadHeaderTimeout = time.Second
		server.Config.ReadTimeout = time.Minute
		server.Config.IdleTimeout = time.Minute
		server.Start()
		defer server.Close()
		response, err := server.Client().Get(server.URL + "/api/events/stream")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		reader := bufio.NewReader(response.Body)
		if line, err := reader.ReadString('\n'); err != nil || line != "data: first\n" {
			t.Fatalf("first SSE chunk=%q err=%v", line, err)
		}
		continueStream <- struct{}{}
		remaining, err := io.ReadAll(reader)
		if err != nil || string(remaining) != "\ndata: second\n\n" {
			t.Fatalf("second SSE chunk=%q err=%v", remaining, err)
		}
	})
}

type observedDeadlineWriter struct {
	http.ResponseWriter
	deadlines chan<- time.Time
}

func (w *observedDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedDeadlineWriter) SetReadDeadline(at time.Time) error {
	w.deadlines <- at
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(at)
}

type dribblingRequestBody struct {
	io.ReadCloser
	progress   chan<- int
	controller *http.ResponseController
	read       int
}

func (b *dribblingRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.read += n
		if b.read == 3 {
			_ = b.controller.SetReadDeadline(time.Unix(1, 0))
		}
		b.progress <- b.read
	}
	return n, err
}

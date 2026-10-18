// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

type observedQueueBody struct {
	io.ReadCloser
	entered chan struct{}
	once    sync.Once
}

func (b *observedQueueBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	return b.ReadCloser.Read(p)
}

func TestQueueCancellationInterruptsHTTPTransport(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(tenant.WithPrincipal(t.Context(), f.person))
	defer cancel()
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = &observedQueueBody{ReadCloser: r.Body, entered: entered}
		f.mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "POST /api/queue HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("queue transport read not entered")
	}
	cancel()
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("cancelled queue transport did not return a response: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(raw), "request body read timed out") {
		t.Fatalf("wrong transport cancellation result: status=%d error=%v body=%s", response.StatusCode, err, raw)
	}
}

func TestQueueOversizedBodyDoesNotWaitForTenantFence(t *testing.T) {
	f := setup(t)
	tx, err := f.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/queue", strings.NewReader(strings.Repeat(" ", (1<<20)+1)))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), f.person))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); f.mux.ServeHTTP(rec, r) }()
	defer func() {
		_ = tx.Rollback(context.Background())
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("oversized queue request did not finish after fence release")
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("oversized queue input waited for the tenant fence")
	}
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("wrong oversized body result: %d %s", rec.Code, rec.Body.String())
	}
}

type queueBodyBarrier struct {
	entered, release chan struct{}
	read, close      sync.Once
}

func (b *queueBodyBarrier) Read(p []byte) (int, error) {
	b.read.Do(func() { close(b.entered) })
	<-b.release
	return 0, io.EOF
}
func (b *queueBodyBarrier) Close() error {
	b.close.Do(func() { close(b.release) })
	return nil
}

type queueDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *queueDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestQueueStalledBodyHoldsNoTenantFence(t *testing.T) {
	f := setup(t)
	id := f.ticket(t, "open", "high", nil)
	for _, path := range []string{"/api/queue", "/api/queue/" + id + "/estimate", "/api/queue/" + id + "/move", "/api/queue/next"} {
		t.Run(path, func(t *testing.T) {
			body := &queueBodyBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithCancel(tenant.WithPrincipal(t.Context(), f.person))
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
			r.Body = body
			rec := &queueDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			done := make(chan struct{})
			go func() { defer close(done); f.mux.ServeHTTP(rec, r) }()
			defer func() {
				cancel()
				_ = body.Close()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("queue handler did not finish")
				}
			}()
			select {
			case <-body.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("queue handler never reached body read")
			}
			tx, err := f.d.Admin.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(t.Context(), `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, f.person.TenantID); err != nil {
				t.Fatalf("stalled queue input holds tenant fence: %v", err)
			}
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("cancellation did not interrupt queue body")
			}
			if rec.Code != http.StatusRequestTimeout || !strings.Contains(rec.Body.String(), "request body read timed out") {
				t.Fatalf("wrong cancellation result: %d %s", rec.Code, rec.Body.String())
			}
			// A failed read keeps its transport deadline (AEON-652): the
			// interrupt is the last deadline call, never followed by a clear.
			if len(rec.deadlines) != 2 || !rec.deadlines[0].After(time.Time{}) || rec.deadlines[1] != time.Unix(1, 0) {
				t.Fatalf("body deadline not installed and interrupted, or cleared after the failed read: %v", rec.deadlines)
			}
		})
	}
}

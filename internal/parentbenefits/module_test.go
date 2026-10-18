// SPDX-License-Identifier: AGPL-3.0-only
package parentbenefits

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

// The transport clock is advanced explicitly once buffering reaches a
// stalled read. Real timeouts only guard hangs, never establish the deadline.
type stalledRetryBody struct {
	prefix  *strings.Reader
	reading chan struct{}
	expired chan struct{}
	closed  chan struct{}
	once    sync.Once
	close   sync.Once
}

func (b *stalledRetryBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	b.once.Do(func() { close(b.reading) })
	select {
	case <-b.expired:
		return 0, os.ErrDeadlineExceeded
	case <-b.closed:
		return 0, io.ErrClosedPipe
	}
}
func (b *stalledRetryBody) Close() error {
	b.close.Do(func() { close(b.closed) })
	return nil
}

type retryDeadlineWriter struct {
	*httptest.ResponseRecorder
	armed     chan time.Time
	deadlines []time.Time
}

func (w *retryDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	if !deadline.IsZero() {
		w.armed <- deadline
	}
	return nil
}

func TestRetryDeadlineBoundsBothBodyDecodes(t *testing.T) {
	for _, tc := range []struct{ name, prefix string }{
		{"first_decode", `{"expected_generation":"`},
		{"trailing_decode", `{"expected_generation":"11111111-1111-4111-8111-111111111111","expected_revision":"2026-10-04T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &stalledRetryBody{prefix: strings.NewReader(tc.prefix), reading: make(chan struct{}), expired: make(chan struct{}), closed: make(chan struct{})}
			defer body.Close()
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Body = body
			r.SetPathValue("nodeId", "22222222-2222-4222-8222-222222222222")
			r = r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{Kind: tenant.Person}))
			w := &retryDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), armed: make(chan time.Time, 1)}
			done := make(chan struct{})
			go func() {
				// A nil pool proves failed transport reads cannot reach transaction
				// admission, even after a complete first JSON value.
				New(nil, nil).retry(w, r)
				close(done)
			}()
			select {
			case <-body.reading:
			case <-time.After(5 * time.Second):
				t.Fatal("buffering never reached the stalled body read")
			}
			select {
			case deadline := <-w.armed:
				if deadline.IsZero() || deadline.After(time.Now().Add(5*time.Second)) {
					t.Fatal("body deadline must be bounded to five seconds", deadline)
				}
			default:
				// Release and join even on the reviewed implementation, which has
				// no deadline. It must fail for that missing guard, not a timeout.
				close(body.expired)
				<-done
				t.Fatal("buffering began a stalled body read without a transport deadline")
			}
			close(body.expired)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("retry did not terminate after the body deadline expired")
			}
			// Both stalls fail at the transport predecode boundary. A complete
			// first JSON value must not move the failure into the trailing decode.
			if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != `{"error":"expected generation and revision required"}` {
				t.Fatalf("stalled read response: %d %s", w.Code, w.Body.String())
			}
			if len(w.deadlines) != 1 || w.deadlines[0].IsZero() {
				t.Fatal("failed read must retain its transport deadline", w.deadlines)
			}
			if w.Header().Get("Connection") != "close" {
				t.Fatal("failed read left the connection reusable")
			}
			select {
			case <-body.closed:
			default:
				t.Fatal("failed body reader was not closed")
			}
		})
	}
}

var _ io.ReadCloser = (*stalledRetryBody)(nil)

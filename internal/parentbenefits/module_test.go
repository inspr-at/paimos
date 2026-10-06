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

// The transport clock is advanced explicitly once the decoder reaches a
// stalled read. Real timeouts only guard hangs, never establish the deadline.
type stalledRetryBody struct {
	prefix  *strings.Reader
	reading chan struct{}
	expired chan struct{}
	once    sync.Once
}

func (b *stalledRetryBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	b.once.Do(func() { close(b.reading) })
	<-b.expired
	return 0, os.ErrDeadlineExceeded
}
func (*stalledRetryBody) Close() error { return nil }

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
	for _, tc := range []struct{ name, prefix, message string }{
		{"first_decode", `{"expected_generation":"`, "expected generation and revision required"},
		{"trailing_decode", `{"expected_generation":"11111111-1111-4111-8111-111111111111","expected_revision":"2026-10-04T00:00:00Z"}`, "invalid request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &stalledRetryBody{prefix: strings.NewReader(tc.prefix), reading: make(chan struct{}), expired: make(chan struct{})}
			defer close(body.expired)
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Body = body
			r.SetPathValue("nodeId", "22222222-2222-4222-8222-222222222222")
			r = r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{Kind: tenant.Person}))
			w := &retryDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), armed: make(chan time.Time, 1)}
			done := make(chan struct{})
			go func() {
				New(nil, nil).retry(w, r)
				close(done)
			}()
			select {
			case <-body.reading:
			case <-time.After(5 * time.Second):
				t.Fatal("decoder never reached the stalled body read")
			}
			select {
			case deadline := <-w.armed:
				if deadline.IsZero() || deadline.After(time.Now().Add(5*time.Second)) {
					t.Fatal("body deadline must be bounded to five seconds", deadline)
				}
			default:
				// Release and join even on the reviewed implementation, which has
				// no deadline. It must fail for that missing guard, not a timeout.
				body.expired <- struct{}{}
				<-done
				t.Fatal("decoder began a stalled body read without a transport deadline")
			}
			body.expired <- struct{}{}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("retry did not terminate after the body deadline expired")
			}
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.message) {
				t.Fatalf("stalled read response: %d %s", w.Code, w.Body.String())
			}
			if len(w.deadlines) != 2 || !w.deadlines[1].IsZero() {
				t.Fatal("request body deadline was not cleared", w.deadlines)
			}
		})
	}
}

var _ io.ReadCloser = (*stalledRetryBody)(nil)

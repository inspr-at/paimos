// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// BufferRequestBody finishes bounded network reads before transaction admission.
// Authorization must still be checked inside the final mutation transaction.
func BufferRequestBody(w http.ResponseWriter, r *http.Request, limit int64) error {
	return BufferRequestBodyTimeout(w, r, limit, 10*time.Second)
}

// BufferRequestBodyTimeout replaces a completely read network body with memory.
func BufferRequestBodyTimeout(w http.ResponseWriter, r *http.Request, limit int64, timeout time.Duration) error {
	raw, err := ReadRequestBody(w, r, limit, timeout)
	if err == nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return err
}

var ErrBodyDeadlineUnavailable = errors.New("request body deadline unavailable")

type bodyReadFailureKey struct{}

func closeBodyConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Connection", "close")
	if failed, ok := r.Context().Value(bodyReadFailureKey{}).(*atomic.Bool); ok {
		failed.Store(true)
	}
}

// ReadRequestBody owns all HTTP request transport read deadlines. Size and time
// bounds precede reading. Any failed read engages the AEON-1228 guard, including
// failures where the transport has not cancelled the connection context.
// Only complete reads clear the deadline. Unsupported writers still get a time
// bound through context and a closed, joined reader. Bodies must support Close
// interrupting Read, as net/http requires of request bodies.
func ReadRequestBody(w http.ResponseWriter, r *http.Request, limit int64, timeout time.Duration) ([]byte, error) {
	if limit <= 0 || timeout <= 0 {
		closeBodyConnection(w, r)
		return nil, ErrBodyDeadlineUnavailable
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		closeBodyConnection(w, r)
		return nil, err
	}
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	err := controller.SetReadDeadline(deadline)
	transport := err == nil
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		closeBodyConnection(w, r)
		return nil, ErrBodyDeadlineUnavailable
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 1)
	go func() { raw, err := io.ReadAll(body); done <- result{raw, err} }()
	var got result
	select {
	case got = <-done:
	case <-ctx.Done():
		// net/http cancels context inside a failed socket read. Join it before
		// issuing another deadline; interrupt only a read that remains blocked.
		// Preserve AEON-652's failed-read drain and transaction-admission proof.
		if transport {
			timer := time.NewTimer(time.Second)
			select {
			case got = <-done:
				timer.Stop()
				goto completed
			case <-timer.C:
			}
			_ = controller.SetReadDeadline(time.Unix(1, 0))
		}
		_ = body.Close()
		got = <-done
	}
completed:
	if got.err != nil || ctx.Err() != nil {
		closeBodyConnection(w, r)
		_ = body.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, got.err
	}
	_ = body.Close()
	if transport {
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			closeBodyConnection(w, r)
			return nil, ErrBodyDeadlineUnavailable
		}
	}
	return got.raw, nil
}

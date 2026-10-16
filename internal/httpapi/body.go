// SPDX-License-Identifier: AGPL-3.0-only
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultBodyReadTimeout = 10 * time.Second

const UploadBodyReadTimeout = 2 * time.Minute

// BufferRequestBody finishes bounded network reads before transaction admission.
// Authorization must still be checked inside the final mutation transaction.
func BufferRequestBody(w http.ResponseWriter, r *http.Request, limit int64) error {
	return BufferRequestBodyTimeout(w, r, limit, DefaultBodyReadTimeout)
}

// BufferRequestBodyTimeout replaces a completely read network body with memory.
func BufferRequestBodyTimeout(w http.ResponseWriter, r *http.Request, limit int64, timeout time.Duration) error {
	raw, err := ReadRequestBody(w, r, limit, timeout)
	if err == nil {
		SetRequestBody(r, raw)
	}
	return err
}

type bufferedRequestBody struct{ *bytes.Reader }

func (*bufferedRequestBody) Close() error { return nil }

// SetRequestBody installs already bounded bytes without re-arming the socket's
// deadline when another layer decodes the body after net/http resumes reading.
func SetRequestBody(r *http.Request, raw []byte) {
	r.Body = &bufferedRequestBody{bytes.NewReader(raw)}
}

var ErrBodyDeadlineUnavailable = errors.New("request body deadline unavailable")

type bodyReadFailureKey struct{}

type bodyReadState struct {
	failed atomic.Bool
	status atomic.Int32
	writer http.ResponseWriter
}

// RequestBodyReader preserves decoder-specific policies while routing the
// complete network read through ReadRequestBody. A failed read stays an error
// for the consumer; it must never look like an empty or partial JSON document.
func RequestBodyReader(w http.ResponseWriter, r *http.Request, limit int64) io.ReadCloser {
	raw, err := ReadRequestBody(w, r, limit, DefaultBodyReadTimeout)
	if err != nil {
		return failedRequestBody{err}
	}
	return &bufferedRequestBody{bytes.NewReader(raw)}
}

type failedRequestBody struct{ err error }

func (b failedRequestBody) Read([]byte) (int, error) { return 0, b.err }
func (failedRequestBody) Close() error               { return nil }

// NewRequestDecoder keeps each route's unknown-field, number and trailing-value
// policy while enforcing byte and transport-time limits before decoding.
func NewRequestDecoder(w http.ResponseWriter, r *http.Request, limit int64) *json.Decoder {
	return json.NewDecoder(RequestBodyReader(w, r, limit))
}

// DecodeJSON accepts exactly one JSON value without unknown fields.
func DecodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	decoder := NewRequestDecoder(w, r, limit)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

// RequestBodyMiddleware also protects local HTTP transports that do not use
// Server.Handler. It maps body failures before the route writes its error,
// retaining the route's existing response envelope and application error code.
func RequestBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(bodyReadFailureKey{}).(*bodyReadState); ok {
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w, conn: r.Context()}
		sw.body.writer = sw
		r = r.WithContext(context.WithValue(r.Context(), bodyReadFailureKey{}, &sw.body))
		next.ServeHTTP(sw, r)
	})
}

func bodyFailureStatus(err error) int {
	var size *http.MaxBytesError
	var network net.Error
	switch {
	case errors.As(err, &size):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.As(err, &network) && network.Timeout():
		return http.StatusRequestTimeout
	default:
		return 0
	}
}

// BeginRequestBodyStream is the disk-staging exception to buffered reads. The
// caller must finish(true) before admitting a write and defer finish(false)
// to abort without draining on errors.
// The size cap covers the entire wire body, including multipart overhead.
func BeginRequestBodyStream(w http.ResponseWriter, r *http.Request, limit int64, timeout time.Duration) (finish func(bool) error, err error) {
	if limit <= 0 || timeout <= 0 {
		return nil, ErrBodyDeadlineUnavailable
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err := controller.SetReadDeadline(deadline); err != nil {
		cancel()
		if !errors.Is(err, http.ErrNotSupported) {
			closeBodyConnection(w, r)
			return nil, ErrBodyDeadlineUnavailable
		}
		// In-memory handler fixtures and alternate transports still use the
		// bounded, joined fallback; live net/http uploads stream to disk.
		if err := BufferRequestBodyTimeout(w, r, limit, timeout); err != nil {
			return nil, err
		}
		return func(bool) error { return nil }, nil
	}
	body := &streamRequestBody{ReadCloser: http.MaxBytesReader(w, r.Body, limit), w: w, r: r}
	r.Body = body
	var once sync.Once
	var result error
	return func(complete bool) error {
		once.Do(func() {
			defer cancel()
			if !complete {
				closeBodyConnection(w, r)
				_ = controller.SetReadDeadline(time.Unix(1, 0))
				_ = body.Close()
				return
			}
			// Multipart framing can end before the HTTP body. Drain within
			// the same size/time budget before allowing the final mutation.
			_, result = io.Copy(io.Discard, body)
			if result == nil {
				result = ctx.Err()
			}
			if result != nil {
				body.fail(result)
				_ = body.Close()
				return
			}
			_ = body.Close()
			if err := controller.SetReadDeadline(time.Time{}); err != nil {
				closeBodyConnection(w, r)
				result = ErrBodyDeadlineUnavailable
			}
		})
		return result
	}, nil
}

type streamRequestBody struct {
	io.ReadCloser
	w http.ResponseWriter
	r *http.Request
}

func (b *streamRequestBody) fail(err error) {
	closeBodyConnection(b.w, b.r)
	if state, ok := b.r.Context().Value(bodyReadFailureKey{}).(*bodyReadState); ok {
		if status := bodyFailureStatus(err); status != 0 {
			state.status.CompareAndSwap(0, int32(status))
		}
	}
}

func (b *streamRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.fail(err)
	}
	return n, err
}

func closeBodyConnection(w http.ResponseWriter, r *http.Request) {
	if w != nil {
		w.Header().Set("Connection", "close")
	}
	if state, ok := r.Context().Value(bodyReadFailureKey{}).(*bodyReadState); ok {
		state.failed.Store(true)
	}
}

// ReadRequestBody owns all HTTP request transport read deadlines. Size and time
// bounds precede reading. Any failed read engages the AEON-1228 guard, including
// failures where the transport has not cancelled the connection context.
// Only complete reads clear the deadline. Unsupported writers still get a time
// bound through context and a closed, joined reader. Bodies must support Close
// interrupting Read, as net/http requires of request bodies.
func ReadRequestBody(w http.ResponseWriter, r *http.Request, limit int64, timeout time.Duration) (raw []byte, readErr error) {
	state, _ := r.Context().Value(bodyReadFailureKey{}).(*bodyReadState)
	if w == nil && state != nil {
		w = state.writer
	}
	defer func() {
		if state != nil && readErr != nil {
			if status := bodyFailureStatus(readErr); status != 0 {
				state.status.CompareAndSwap(0, int32(status))
			}
		}
	}()
	if limit <= 0 || timeout <= 0 {
		closeBodyConnection(w, r)
		return nil, ErrBodyDeadlineUnavailable
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	if failed, ok := r.Body.(failedRequestBody); ok {
		return nil, failed.err
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		closeBodyConnection(w, r)
		return nil, err
	}
	if _, buffered := r.Body.(*bufferedRequestBody); buffered {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			closeBodyConnection(w, r)
		}
		return raw, err
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
		// A failed socket read may also cancel the connection context.
		if bodyFailureStatus(got.err) != 0 {
			return nil, got.err
		}
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

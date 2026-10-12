// SPDX-License-Identifier: AGPL-3.0-only

package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/inspr-at/paimos/internal/db"
)

type requestIDKey struct{}

const requestIDHeader = "X-Request-ID"

// RequestID returns the id set by the request-id middleware, if any.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func commonMiddleware(next http.Handler) http.Handler {
	return requestIDMiddleware(recoverMiddleware(securityMiddleware(next)))
}

const (
	ordinaryRequestBudget = time.Minute
	authenticationBudget  = 5 * time.Second
)

type handlerBudgetKey struct{}
type handlerBudget struct {
	parent context.Context
	stop   context.CancelFunc
}

// requestBudgetMiddleware runs before authentication, including pool admission
// and authorization. Ordinary API requests have a one-minute total deadline.
// Streams and routes with longer handler budgets get five seconds for admission;
// their handlers retain their existing lifetime after authentication finishes.
func requestBudgetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		duration := ordinaryRequestBudget
		separate := separateHandlerBudget(r.Pattern)
		if separate {
			duration = authenticationBudget
		}
		ctx, stop := context.WithTimeout(r.Context(), duration)
		defer stop()
		if separate {
			ctx = context.WithValue(ctx, handlerBudgetKey{}, handlerBudget{r.Context(), stop})
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func separateHandlerBudget(pattern string) bool {
	switch pattern {
	case "GET /api/events/stream", "GET /api/events/subscribe",
		"GET /api/inbox/stream", "GET /api/chat-threads/{id}/live",
		"GET /api/quotes/{quoteId}/collaboration/stream",
		"GET /api/projects/{projectId}/delivery/flow/stream",
		"GET /api/projects/{projectId}/harness-sessions/{sessionId}/notifications",
		"GET /api/projects/{projectId}/harness-sessions/{sessionId}/watch",
		"GET /api/runs/queued/notifications",
		"POST /api/projects/{projectId}/delivery/metrics/backfill",
		"POST /api/projects/{projectId}/delivery/metrics/check-again",
		"POST /api/projects/{projectId}/delivery/merge-backfill":
		return true
	}
	return false
}

// handlerBudgetMiddleware is inside all authentication/authorization middleware.
// Restore the caller's lifetime while retaining the authenticated values and
// transaction guards. Never admit a handler after the admission deadline.
func handlerBudgetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if budget, ok := r.Context().Value(handlerBudgetKey{}).(handlerBudget); ok {
			if err := r.Context().Err(); err != nil {
				LogFailure(r, "authentication_budget", err)
				WriteError(w, http.StatusServiceUnavailable, "service unavailable")
				return
			}
			// WithoutCancel also masks the admission context's cancellation
			// cause; its values alone must survive into the handler phase.
			ctx := handlerLifetimeContext{context.WithoutCancel(r.Context()), budget.parent}
			budget.stop()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

type handlerLifetimeContext struct {
	context.Context
	parent context.Context
}

func (ctx handlerLifetimeContext) Deadline() (time.Time, bool) { return ctx.parent.Deadline() }
func (ctx handlerLifetimeContext) Done() <-chan struct{}       { return ctx.parent.Done() }
func (ctx handlerLifetimeContext) Err() error                  { return ctx.parent.Err() }

// readStatementTimeoutMiddleware bounds list and search statements before the
// module starts a tenant transaction. It buffers these small JSON responses so
// a module's generic database error can be replaced by a clear 503 problem.
func readStatementTimeoutMiddleware(next http.Handler) http.Handler {
	duration := 15 * time.Second
	if raw := os.Getenv("AEON_READ_STATEMENT_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			duration = parsed
		} else {
			slog.Warn("invalid AEON_READ_STATEMENT_TIMEOUT; using 15s")
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || (r.Pattern != "GET /api/nodes" && r.Pattern != "GET /api/nodes/tree" && r.Pattern != "GET /api/projects" && r.Pattern != "GET /api/search") {
			next.ServeHTTP(w, r)
			return
		}
		ctx := db.WithReadStatementTimeout(r.Context(), duration)
		buffer := &readResponse{header: make(http.Header)}
		next.ServeHTTP(buffer, r.WithContext(ctx))
		if db.ReadStatementTimedOut(ctx) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(struct {
				Type   string `json:"type"`
				Title  string `json:"title"`
				Status int    `json:"status"`
				Detail string `json:"detail"`
			}{"about:blank", "Service Unavailable", http.StatusServiceUnavailable, "Read query exceeded the time limit; retry later."})
			return
		}
		for name, values := range buffer.header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		if buffer.status == 0 {
			buffer.status = http.StatusOK
		}
		w.WriteHeader(buffer.status)
		_, _ = w.Write(buffer.body.Bytes())
	})
}

type readResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *readResponse) Header() http.Header { return w.header }
func (w *readResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *readResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// blob: and data: images let the avatar crop dialog preview a local file
		// before upload; everything else stays same-origin.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func aithemaViewPolicy(origin string, next http.Handler) http.Handler {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SPA navigation cannot relax the document's Permissions-Policy. The
		// app document must allow same-origin capture before entering Journey.
		// Public portal documents retain their microphone denial.
		if r.URL.Path != "/portal" && !strings.HasPrefix(r.URL.Path, "/portal/") {
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(self), geolocation=()")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; connect-src 'self' wss://"+u.Host+"; frame-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

func attachmentViewPolicy(origin string, next http.Handler) http.Handler {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, " ;\r\n\t") {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/portal" && !strings.HasPrefix(r.URL.Path, "/portal/") {
			policy := strings.Split(w.Header().Get("Content-Security-Policy"), ";")
			out := make([]string, 0, len(policy)+1)
			for _, directive := range policy {
				directive = strings.TrimSpace(directive)
				if !strings.HasPrefix(directive, "frame-src ") && directive != "" {
					out = append(out, directive)
				}
			}
			out = append(out, "frame-src 'self' "+origin)
			w.Header().Set("Content-Security-Policy", strings.Join(out, "; "))
		}
		next.ServeHTTP(w, r)
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return requestIDMiddlewareWithMetrics(next, &TransportMetrics{})
}

func requestIDMiddlewareWithMetrics(next http.Handler, metrics *TransportMetrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Caller-supplied IDs can contain tokens. Generate our own trace value
		// before it enters response headers, context or structured logs.
		id := newRequestID()
		// net/http cancels a keep-alive connection's context on any failed read,
		// including a handler's own read deadline, but can keep the connection
		// open after draining the body. Every later request on it would start
		// cancelled and fail its first database call in 0 ms (AEON-1228). Any
		// response written after that cancellation closes the connection; a
		// request that still arrives cancelled is logged and closes it too.
		conn := r.Context()
		deadOnArrival := conn.Err() != nil
		if deadOnArrival {
			w.Header().Set("Connection", "close")
		}
		r = r.WithContext(context.WithValue(conn, requestIDKey{}, id))
		w.Header().Set(requestIDHeader, id)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK, conn: conn}
		r = r.WithContext(context.WithValue(r.Context(), bodyReadFailureKey{}, &sw.bodyFailed))
		var authenticated atomic.Bool
		r = r.WithContext(context.WithValue(r.Context(), authenticationKey{}, &authenticated))
		r = r.WithContext(context.WithValue(r.Context(), failureKey{}, &sw.failureLogged))
		start := time.Now()
		defer func() {
			if sw.status >= 500 && !sw.failureLogged.Load() {
				LogFailure(r, "handler", conn.Err())
			}
			total := metrics.record(r, sw.status, authenticated.Load(), deadOnArrival)
			attrs := []any{
				"method", r.Method,
				"path", requestLogRoute(r),
				"status", sw.status,
				"request_id", id,
				"conn_id", ConnectionID(conn),
				"route", metricRoute(r),
				"status_class", statusClass(sw.status),
				"authenticated", authenticated.Load(),
				"request_count", 1,
				"requests_total", total,
				"cancelled_on_arrival_total", metrics.cancelled.Load(),
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if deadOnArrival {
				attrs = append(attrs, "connection_context", "cancelled_on_arrival")
				slog.Warn("request arrived on a cancelled connection; connection closed", "path", requestLogRoute(r), "request_id", id, "conn_id", ConnectionID(conn))
			} else if sw.closed {
				attrs = append(attrs, "connection_context", "cancelled")
			}
			slog.Info("request", attrs...)
		}()
		if deadOnArrival {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "1")
			LogFailure(r, "connection_context", conn.Err())
			WriteError(sw, http.StatusServiceUnavailable, "service unavailable")
			return
		}
		next.ServeHTTP(sw, r)
		// A handler that wrote nothing gets its status line from net/http after
		// this returns; the connection header must be set before that.
		if !sw.wrote && (conn.Err() != nil || sw.bodyFailed.Load()) {
			sw.closed = true
			w.Header().Set("Connection", "close")
		}
		if (conn.Err() != nil || sw.bodyFailed.Load()) && sw.wrote && !sw.closeHeader {
			// Headers committed before cancellation cannot be changed. Flush
			// any buffered response, then let net/http abort this connection
			// (or only this stream on HTTP/2), rather than return it to reuse.
			sw.closed = true
			_ = http.NewResponseController(w).Flush()
			panic(http.ErrAbortHandler)
		}
	})
}

// routePatternMiddleware resolves the inner API mux before other middleware
// can clone the request or reject it. ServeMux normally sets Pattern while
// serving, but a later WithContext would hide that write from the logger.
func routePatternMiddleware(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, r.Pattern = mux.Handler(r)
		next.ServeHTTP(w, r)
	})
}

func requestLogRoute(r *http.Request) string {
	// Root and API catchalls identify no specific route. Use a sanitized path
	// for those, including unmatched public URLs and SPA capability links.
	if r.Pattern != "" && r.Pattern != "/" && r.Pattern != "/api" && r.Pattern != "/api/" {
		return r.Pattern
	}
	return redactedLogPath(r.URL.Path)
}

func redactedLogPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return "/"
	}
	// On an unmatched route there is no schema to distinguish an ID from a
	// capability. Keep only known static prefixes and redact every later
	// segment; this also covers future invite, link, confirmation and download
	// token shapes without having to recognize their exact spelling.
	if len(parts) >= 2 && strings.EqualFold(parts[0], "api") {
		switch strings.ToLower(parts[1]) {
		case "auth", "nodes", "quotes", "attachments", "public", "invites", "invite", "links", "link", "confirmation", "confirm", "downloads", "download":
			return "/api/" + strings.ToLower(parts[1]) + "/{redacted}"
		default:
			return "/api/{redacted}"
		}
	}
	if len(parts) == 1 && (parts[0] == "favicon.ico" || parts[0] == "robots.txt") {
		return path
	}
	switch strings.ToLower(parts[0]) {
	case "q", "offers", "portal", "invite", "invites", "link", "links", "confirm", "confirmation", "download", "downloads":
		return "/" + strings.ToLower(parts[0]) + "/{redacted}"
	default:
		return "/{redacted}"
	}
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				// Both the URL path and the panic value can contain a public
				// capability or an upstream credential. The request ID is enough
				// to correlate this failure without disclosing either value.
				LogFailure(r, "panic", nil)
				WriteError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
	// conn is the request context net/http derived from the connection. Once
	// it is cancelled the connection must not carry another request.
	conn          context.Context
	closed        bool
	closeHeader   bool
	failureLogged atomic.Bool
	bodyFailed    atomic.Bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	final := status >= 200 || status == http.StatusSwitchingProtocols
	if final {
		w.status = status
		w.wrote = true
	}
	if w.bodyFailed.Load() || w.conn != nil && w.conn.Err() != nil {
		w.closed = true
		w.ResponseWriter.Header().Set("Connection", "close")
		w.closeHeader = final
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	_ = w.FlushError()
}

func (w *statusWriter) FlushError() error {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

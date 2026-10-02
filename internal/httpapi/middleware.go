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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Caller-supplied IDs can contain tokens. Generate our own trace value
		// before it enters response headers, context or structured logs.
		id := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		r = r.WithContext(ctx)
		w.Header().Set(requestIDHeader, id)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		defer func() {
			slog.Info("request",
				"method", r.Method,
				"path", requestLogRoute(r),
				"status", sw.status,
				"request_id", id,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}()
		next.ServeHTTP(sw, r)
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
				// Both the URL path and the panic value can contain a public
				// capability or an upstream credential. The request ID is enough
				// to correlate this failure without disclosing either value.
				slog.Error("panic", "request_id", RequestID(r.Context()))
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
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
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
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

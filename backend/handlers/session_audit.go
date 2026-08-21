// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/httpcontract"
)

// SessionHeader is the canonical HTTP header name for the session tag.
// Clients (paimos CLI, MCP) set this per-invocation; human browsers
// don't need to care.
const SessionHeader = "X-PAIMOS-Session-Id"

// auditEnabled returns whether session-activity logging is turned on.
// PAI-116: defaults to ON for the NIS2 readiness target — the audit row
// is small (a single insert per mutation) and multi-agent use is now the
// expected operating mode. Operators who want to opt back out can set
// PAIMOS_AUDIT_SESSIONS=false. Reads the env var on every call so an
// operator can flip it at runtime without a restart.
func auditEnabled() bool {
	v := os.Getenv("PAIMOS_AUDIT_SESSIONS")
	if v == "" {
		return true
	}
	return v != "false" && v != "0"
}

// SessionAuditMiddleware records mutation requests (POST/PUT/PATCH/DELETE)
// to the session_activity table, tagged with the X-PAIMOS-Session-Id
// header. Missing/malformed headers are non-fatal — the audit row gets
// session_id=NULL so operators can still see "something happened
// without a session tag" and chase it down.
//
// Reads-only requests are skipped entirely so the audit table doesn't
// balloon with UI browsing noise. If you need full trace logging for
// a different reason, layer another middleware — this one is focused.
//
// PAI-809: supervisory-control traffic is excluded outright, before the
// wrapper and before the insert. The row this middleware writes stores
// the full request path, and on a control route that path *is* the
// secret — the delivery key a human is watching, the run a supervisor is
// steering, the command being decided. `control_events` is the mandatory
// and authoritative record for those requests; a second, weaker copy in
// an operator-readable convenience table is a disclosure with no
// compensating value.
func SessionAuditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auditEnabled() || !isMutation(r.Method) || httpcontract.IsControlRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Wrap the ResponseWriter so we can capture the final status
		// code (handlers often write it via WriteHeader or implicitly
		// via the first Write). statusCapturingWriter preserves the
		// standard http.ResponseWriter interface — no Flush/Hijack
		// exposure, deliberate since the audit hook shouldn't support
		// streaming shapes that are harder to reason about.
		sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		// Post-processing: record the row. Errors here are logged
		// but don't bubble up — audit failures must not 500 real
		// traffic.
		sessionID := r.Header.Get(SessionHeader)
		var sessArg any
		if sessionID != "" {
			sessArg = sessionID
		}
		var uidArg any
		if u := auth.GetUser(r); u != nil {
			uidArg = u.ID
		}
		if _, err := db.DB.Exec(
			`INSERT INTO session_activity(session_id, user_id, method, path, status_code) VALUES(?,?,?,?,?)`,
			sessArg, uidArg, r.Method, r.URL.Path, sw.status,
		); err != nil {
			log.Printf("session_audit: insert failed: %v", err)
		}
	})
}

// ordinaryRequestLogger is the access log for everything that is not
// supervisory control. A package var so a test can prove the control
// branch never reaches it.
var ordinaryRequestLogger = chimiddleware.Logger

// ControlAwareRecoverer preserves chi's diagnostic recoverer everywhere except
// the supervisory-control surface. chi includes the recovered value in its
// panic log, and that value may contain request/provider data. Control panics
// therefore become a plain 500; the outer control access log still records the
// closed route class, server request id, status, and duration.
func ControlAwareRecoverer(next http.Handler) http.Handler {
	ordinary := chimiddleware.Recoverer(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpcontract.IsControlRequest(r) {
			ordinary.ServeHTTP(w, r)
			return
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				if recoveredErr, ok := recovered.(error); ok && errors.Is(recoveredErr, http.ErrAbortHandler) {
					panic(recovered)
				}
				if r.Header.Get("Connection") != "Upgrade" {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ControlAwareRequestLogger is the router's outermost access log.
//
// The ordinary logger records method, full request URI (query included),
// remote address, status, and size — the right trade for CRUD traffic and
// the wrong one for supervisory control, where the URI carries the
// delivery key, the run id, and the command id. For a classified control
// request this logger writes the line itself and keeps exactly five
// fields: method (closed set), route class (closed set), request id
// (bounded charset), status, and duration. No path, no query, no header,
// no body, no error text.
//
// Mount this in place of chi's middleware.Logger, outermost, so nothing
// downstream can log the request under the ordinary format first.
func ControlAwareRequestLogger(next http.Handler) http.Handler {
	ordinary := ordinaryRequestLogger(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		class, isControl := httpcontract.ClassifyControlRequest(r)
		if !isControl {
			ordinary.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		// chi's wrapper is used rather than the audit writer above because
		// it forwards Flush/Hijack — a control route must not lose
		// streaming behavior just because it is logged privately.
		wrapped := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapped, r)
		status := wrapped.Status()
		if status == 0 {
			// Handler returned without writing: net/http sends 200.
			status = http.StatusOK
		}
		log.Printf("control: method=%s route_class=%s request_id=%s status=%d duration_ms=%d",
			safeLogMethod(r.Method), class, safeLogRequestID(wrapped.Header().Get(RequestIDHeader)),
			status, time.Since(started).Milliseconds())
	})
}

// safeLogMethod closes the method vocabulary. A method is a token, so it
// cannot break the line, but only the methods this API answers are worth
// recording — anything else is noise that a caller chose.
func safeLogMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost,
		http.MethodPut, http.MethodPatch, http.MethodDelete:
		return method
	default:
		return "OTHER"
	}
}

// safeLogRequestID bounds the one field on the control line whose value a
// caller can choose: RequestIDMiddleware echoes a client-supplied
// X-PAIMOS-Request-Id when present. Correlation is worth keeping, an
// attacker-authored log line is not, so anything outside a short opaque
// id charset is dropped rather than printed.
func safeLogRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "unavailable"
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r == '-', r == '_', r == '.', r == ':':
		default:
			return "unavailable"
		}
	}
	return value
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// statusCapturingWriter remembers the HTTP status code a handler sent
// so the audit row can record it. Defaults to 200 — matches net/http
// which implicitly sends 200 on first Write if the handler never
// called WriteHeader.
type statusCapturingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		// net/http's default — implicit 200 on first Write.
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// GetSessionActivity handles GET /api/sessions/:id/activity — returns
// mutations recorded under a session, paginated by keyset on id.
//
// Admin only (mounted via auth.RequireAdmin in main.go). Returns 200
// with an empty activity list if the session id has no rows — matches
// the "unknown session" case without leaking existence.
//
// Response: {activity: [{id, session_id, user_id, method, path,
// status_code, occurred_at}], next_cursor: int or null}
func GetSessionActivity(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if sessionID == "" {
		jsonError(w, "session id required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	limit := 100
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	var cursor int64
	if c := q.Get("cursor"); c != "" {
		if n, err := strconv.ParseInt(c, 10, 64); err == nil && n > 0 {
			cursor = n
		}
	}

	// Keyset pagination: ORDER BY id ASC, WHERE id > cursor.
	rows, err := db.DB.Query(`
		SELECT id, session_id, user_id, method, path, status_code, occurred_at
		FROM session_activity
		WHERE session_id = ? AND id > ?
		ORDER BY id ASC
		LIMIT ?
	`, sessionID, cursor, limit+1) // +1 to detect next page
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type row struct {
		ID         int64  `json:"id"`
		SessionID  string `json:"session_id"`
		UserID     *int64 `json:"user_id"`
		Method     string `json:"method"`
		Path       string `json:"path"`
		StatusCode int    `json:"status_code"`
		OccurredAt string `json:"occurred_at"`
	}
	out := make([]row, 0, limit)
	for rows.Next() {
		var rw row
		var sid *string
		var uid *int64
		if err := rows.Scan(&rw.ID, &sid, &uid, &rw.Method, &rw.Path, &rw.StatusCode, &rw.OccurredAt); err != nil {
			log.Printf("session_activity: scan: %v", err)
			continue
		}
		if sid != nil {
			rw.SessionID = *sid
		}
		rw.UserID = uid
		out = append(out, rw)
	}
	var next any
	if len(out) > limit {
		next = out[limit-1].ID // the last id of the page we're returning
		out = out[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"activity":    out,
		"next_cursor": next,
	})
}

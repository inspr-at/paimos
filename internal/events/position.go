// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// PositionHeader carries the tenant event-log position a response stands for
// (AEON-449). A client that holds several reads of one collection, or a read
// and a write of its own, keeps the newest position per entity: an older
// position never overwrites a newer one, whatever order the answers arrive in.
//
//   - A node-list read names the newest event before its handler: a lower bound
//     on the events its projections include. It runs once, even when newer events
//     commit while it runs. Clients can apply a page covering the triggering hint
//     while keeping a follow-up read for the newer hints (AEON-514).
//   - Other reads name the exact snapshot they return: the newest committed event
//     was the same before its first statement and after its last, so no event
//     committed while it ran, and it includes every event up to that position
//     and none after. A read that an event interrupted is run again (see
//     readAttempts); one that keeps being interrupted carries no position, and
//     the client merges it by the order it started in.
//   - An accepted write names the newest committed event after the handler's
//     transaction ended, so it is at or above the write's own event. A read whose
//     position is below it may predate the write. That is a floor, not a statement
//     about the write's own body: other events can commit between the write and
//     the watermark read, so the body may be older than the position. A client
//     never merges a write's body by this position.
//
// A session or a run is merged by the row's own revision instead: row_version
// grows inside the statement that changes the row (migration 1052), so a larger
// one is always the newer copy, whichever answer carries it. The position only
// orders the reads of one collection and holds the write floor.
//
// Every resource mutation appends an event (see Append), so the position
// advances with every change a person can make. Fields a worker refreshes
// without an event (a heartbeat) are not part of the position. The header is
// additive: a client that does not know it ignores it, and an answer without it is
// merged the way it was before.
const PositionHeader = "Aeon-Event-Position"

// readAttempts bounds how often an interrupted read is run again. Reads change
// nothing, so a repeat is safe; a tenant that commits events faster than a read
// completes gets the last answer without a position instead of waiting for quiet.
const readAttempts = 3

// positionReads are the reads the agents workspace merges by position: the
// lists and details that /agents holds side by side and that its own writes
// change. A read outside the set carries no header, so it costs nothing.
var positionReads = map[string]bool{
	"GET /api/nodes":                                             true,
	"GET /api/harness-sessions/live":                             true,
	"GET /api/harness-sessions":                                  true,
	"GET /api/projects/{projectId}/harness-sessions":             true,
	"GET /api/projects/{projectId}/harness-sessions/{sessionId}": true,
	"GET /api/runs":                                              true,
	"GET /api/runs/{runId}":                                      true,
	"GET /api/agent-accounts":                                    true,
	"GET /api/approvals":                                         true,
	"GET /api/models":                                            true,
	"GET /api/projects/{projectId}/messages":                     true,
	"GET /api/projects/{projectId}/message-targets":              true,
}

// PositionMiddleware sets PositionHeader on those reads and on every accepted
// (2xx) mutation of an authenticated request. Mount it after the auth
// middleware, so a request that auth refused never reaches it.
func PositionMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.PrincipalFrom(r.Context())
			if !ok || p.TenantID == "" {
				next.ServeHTTP(w, r)
				return
			}
			switch r.Method {
			case http.MethodGet:
				if positionReads[r.Pattern] {
					serveSettled(w, r, next, pool, p.TenantID)
					return
				}
			case http.MethodHead, http.MethodOptions:
			default:
				w = &positionWriter{ResponseWriter: w, r: r, pool: pool, tenantID: p.TenantID}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// serveSettled buffers listed reads. The node list carries the before counter
// as a lower bound without repeating the handler; other collections retain their
// settled snapshot contract (equal counters before and after the handler).
func serveSettled(w http.ResponseWriter, r *http.Request, next http.Handler, pool *pgxpool.Pool, tenantID string) {
	var answer *bufferedResponse
	for attempt := 0; attempt < readAttempts; attempt++ {
		before, err := newestEvent(r.Context(), pool, tenantID)
		if err != nil {
			break
		}
		answer = newBufferedResponse(w)
		next.ServeHTTP(answer, r)
		if answer.status < 200 || answer.status >= 300 {
			break
		}
		if r.Pattern == "GET /api/nodes" {
			answer.header.Set(PositionHeader, strconv.FormatInt(before, 10))
			break
		}
		if after, err := newestEvent(r.Context(), pool, tenantID); err != nil {
			break
		} else if after == before {
			answer.header.Set(PositionHeader, strconv.FormatInt(after, 10))
			break
		}
	}
	if answer == nil {
		// The position could not be read at all: the read answers without one.
		next.ServeHTTP(w, r)
		return
	}
	answer.replay(w)
}

// bufferedResponse holds a handler's answer until its position is known.
type bufferedResponse struct {
	writer http.ResponseWriter
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse(w http.ResponseWriter) *bufferedResponse {
	return &bufferedResponse{writer: w, header: w.Header().Clone()}
}

func (b *bufferedResponse) Header() http.Header { return b.header }

// Unwrap preserves connection deadlines through the buffer. Flush below stays
// a no-op so ResponseController cannot emit an unsettled response early.
func (b *bufferedResponse) Unwrap() http.ResponseWriter { return b.writer }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// Flush is a no-op: the listed reads are plain JSON answers, never streams.
func (b *bufferedResponse) Flush() {}

func (b *bufferedResponse) replay(w http.ResponseWriter) {
	dst := w.Header()
	for key := range dst {
		delete(dst, key)
	}
	for key, values := range b.header {
		dst[key] = values
	}
	if b.status == 0 {
		b.status = http.StatusOK
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}

// newestEvent is the tenant's latest committed event ID (0 before the first).
func newestEvent(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int64, error) {
	var id int64
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT last_id FROM event_counters WHERE tenant_id=$1`, tenantID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return id, err
}

// positionWriter stamps an accepted mutation when its status line goes out. By
// then the handler's transaction has ended: handlers answer after db.InTenant.
type positionWriter struct {
	http.ResponseWriter
	r        *http.Request
	pool     *pgxpool.Pool
	tenantID string
	started  bool
}

func (w *positionWriter) WriteHeader(status int) {
	if !w.started {
		w.started = true
		if status >= 200 && status < 300 {
			if position, err := newestEvent(w.r.Context(), w.pool, w.tenantID); err == nil {
				w.Header().Set(PositionHeader, strconv.FormatInt(position, 10))
			}
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *positionWriter) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *positionWriter) Flush() {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *positionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

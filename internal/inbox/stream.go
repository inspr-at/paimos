// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

func (m *module) stream(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	after, err := streamCursor(r)
	if err != nil {
		failure(w, err)
		return
	}
	// With ?session= the stream carries that generation's bound messages too
	// and keeps the session marked as listening on every wake and keepalive.
	sessionID, err := sessionQuery(r)
	if err != nil {
		failure(w, err)
		return
	}
	conn, err := listenConn(r.Context(), m.pool)
	if err != nil {
		failure(w, err)
		return
	}
	defer closeListen(conn)
	if _, err = conn.Exec(r.Context(), "LISTEN aeon_events"); err != nil {
		failure(w, err)
		return
	}
	batch, err := m.pendingVia(r.Context(), p, after, 200, sessionID, SeenStream)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	flush := func(payload string) error {
		if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := fmt.Fprint(w, payload); err != nil {
			return err
		}
		return rc.Flush()
	}
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	if err := flush(": connected\n\n"); err != nil {
		return
	}
	heartbeat := m.heartbeat
	if heartbeat <= 0 {
		heartbeat = 15 * time.Second
	}
	nextBeat := time.Now().Add(heartbeat)
	for {
		for _, msg := range batch {
			if r.Context().Err() != nil {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				return
			}
			if err = flush(fmt.Sprintf("id: %d\nevent: message\ndata: %s\n\n", msg.SentEventID, data)); err != nil {
				return
			}
			after = msg.SentEventID
		}
		if len(batch) < 200 {
			if err := m.waitStream(r.Context(), conn, p, &nextBeat, heartbeat, flush); err != nil {
				return
			}
		}
		batch, err = m.pendingVia(r.Context(), p, after, 200, sessionID, SeenStream)
		if err != nil {
			return
		}
	}
}

// Last-Event-ID wins when present so an EventSource reconnect resumes after
// the query string's original after cursor.
func streamCursor(r *http.Request) (int64, error) {
	if values, present := r.Header["Last-Event-Id"]; present {
		if len(values) != 1 {
			return 0, badRequest("invalid Last-Event-ID")
		}
		after, err := parseNonNeg(values[0])
		if err != nil {
			return 0, badRequest("invalid Last-Event-ID")
		}
		return after, nil
	}
	if r.URL.Query().Has("after") {
		after, err := parseNonNeg(r.URL.Query().Get("after"))
		if err != nil {
			return 0, badRequest("invalid after")
		}
		return after, nil
	}
	return 0, nil
}

func (m *module) waitStream(ctx context.Context, conn *pgx.Conn, p tenant.Principal, nextBeat *time.Time, heartbeat time.Duration, flush func(string) error) error {
	for {
		if !time.Now().Before(*nextBeat) {
			if err := flush(": keepalive\n\n"); err != nil {
				return err
			}
			*nextBeat = time.Now().Add(heartbeat)
			return nil
		}
		waitCtx, cancel := context.WithDeadline(ctx, *nextBeat)
		n, err := conn.WaitForNotification(waitCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if err = flush(": keepalive\n\n"); err != nil {
				return err
			}
			*nextBeat = time.Now().Add(heartbeat)
			return nil
		}
		if notificationTenant(n.Payload) == p.TenantID {
			return nil
		}
	}
}

func notificationTenant(payload string) string {
	var hint struct {
		TenantID string `json:"tenant_id"`
	}
	if json.Unmarshal([]byte(payload), &hint) != nil {
		return ""
	}
	return hint.TenantID
}

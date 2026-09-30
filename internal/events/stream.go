// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// maxLiveReplay bounds the backlog a live-mode reconnect replays (see stream).
const maxLiveReplay = 1000

// newest is the tenant's latest committed event ID (0 before the first).
func (m *module) newest(ctx context.Context, p tenant.Principal) (int64, error) {
	return newestEvent(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID)
}

func (m *module) stream(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	after := int64(0)
	// ?after= opts into live mode (AEON-326): "latest" starts at the newest
	// event instead of replaying the log, and every connection opens with a
	// stream.ready event naming the ID it resumes after. A browser reconnect
	// sends Last-Event-ID, which wins over the query.
	query := r.URL.Query()
	live, latest, resumed := query.Has("after"), query.Get("after") == "latest", false
	if values, present := r.Header["Last-Event-Id"]; present {
		var err error
		if len(values) != 1 {
			writeError(w, 400, "invalid_request", "invalid Last-Event-ID")
			return
		}
		after, err = parseID(values[0])
		if err != nil {
			writeError(w, 400, "invalid_request", "invalid Last-Event-ID")
			return
		}
		latest = false
	} else if live && !latest {
		var err error
		if after, err = parseID(query.Get("after")); err != nil {
			writeError(w, 400, "invalid_request", "invalid after")
			return
		}
	}
	// LISTEN is session state, not a tenant data query. Do not hold a pool slot:
	// even with many streams, replay and writers must still acquire connections.
	connectCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	conn, err := pgx.ConnectConfig(connectCtx, m.pool.Config().ConnConfig.Copy())
	cancel()
	if err != nil {
		failure(w, err)
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(ctx)
	}()
	if _, err = conn.Exec(r.Context(), "LISTEN aeon_events"); err != nil {
		failure(w, err)
		return
	}
	if live {
		// Subscribed first, so nothing committed from here on is missed.
		// Writers commit in ID order (they share the tenant counter).
		newest, err := m.newest(r.Context(), p)
		if err != nil {
			failure(w, err)
			return
		}
		// A resume point from another database, or one so far behind that a
		// refetch is cheaper than the replay, restarts at the newest event.
		// stream.ready says resumed:false then (and for "latest"): the
		// client's cue to refetch what it shows.
		if latest || after > newest || newest-after > maxLiveReplay {
			after = newest
		} else {
			resumed = true
		}
	}
	// Subscribe before replay to close the gap between reading and listening.
	batch, err := m.read(r.Context(), p, "", after, 200)
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
	if live {
		if err := flush(fmt.Sprintf("id: %d\nevent: stream.ready\ndata: {\"after\":%d,\"resumed\":%t}\n\n", after, after, resumed)); err != nil {
			return
		}
	}
	heartbeat := time.Now().Add(15 * time.Second)
	for {
		for _, e := range batch.Items {
			if r.Context().Err() != nil {
				return
			}
			data, err := json.Marshal(e)
			if err != nil {
				return
			}
			if err = flush(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data)); err != nil {
				return
			}
			after = e.ID
		}
		if batch.NextAfter == nil {
			// Unrelated tenants must not keep extending the heartbeat deadline.
			for {
				// pgx can return an already queued notification even with an
				// expired context, so check the deadline before draining more.
				if !time.Now().Before(heartbeat) {
					if err = flush(": keepalive\n\n"); err != nil {
						return
					}
					heartbeat = time.Now().Add(15 * time.Second)
					break
				}
				waitCtx, cancel := context.WithDeadline(r.Context(), heartbeat)
				n, err := conn.WaitForNotification(waitCtx)
				cancel()
				if r.Context().Err() != nil {
					return
				}
				if err != nil {
					if !errors.Is(err, context.DeadlineExceeded) {
						return
					}
					if err = flush(": keepalive\n\n"); err != nil {
						return
					}
					heartbeat = time.Now().Add(15 * time.Second)
					break // Periodic durable replay also tolerates missed wakeups.
				}
				var hint struct {
					TenantID string `json:"tenant_id"`
				}
				if json.Unmarshal([]byte(n.Payload), &hint) == nil && hint.TenantID == p.TenantID {
					break
				}
			}
		}
		batch, err = m.read(r.Context(), p, "", after, 200)
		if err != nil {
			return
		} // Client resumes from the last successfully sent ID.
	}
}

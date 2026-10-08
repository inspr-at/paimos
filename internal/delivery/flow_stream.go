// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const (
	flowStreamHeartbeat = 15 * time.Second
	flowStreamLifetime  = 30 * time.Minute
	flowStreamBatch     = 200
	flowStreamSlots     = 64
)

var flowEventTypes = []string{"delivery.step", "delivery.item", "delivery.incident"}

// flowHint is a value-free live hint. Clients refetch the flow or the run.
type flowHint struct {
	ID         int64     `json:"id"`
	Type       string    `json:"type"`
	At         time.Time `json:"at"`
	ItemID     string    `json:"item_id"`
	StepID     *string   `json:"step_id,omitempty"`
	IncidentID *string   `json:"incident_id,omitempty"`
}

// flowStream is the Live feed of one project's flow: delivery.step,
// delivery.item and delivery.incident hints after the event log commits them.
// Members need delivery.read, re-checked on every heartbeat; a revoked reader
// is disconnected. Hints carry ids only.
func (m *Module) flowStream(w http.ResponseWriter, r *http.Request) {
	p, project, ok := metricProject(w, r)
	if !ok {
		return
	}
	after, latest := int64(0), true
	if values, present := r.Header["Last-Event-Id"]; present {
		n, err := strconv.ParseInt(values[0], 10, 64)
		if len(values) != 1 || err != nil || n < 0 {
			respondError(w, fail(400, "invalid Last-Event-ID"))
			return
		}
		after, latest = n, false
	} else if v := r.URL.Query().Get("after"); v != "" && v != "latest" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			respondError(w, fail(400, "invalid after"))
			return
		}
		after, latest = n, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), flowStreamLifetime)
	defer cancel()
	authorize := func() error {
		authCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		return db.InTenant(authCtx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := projectSettings(authCtx, tx, project); err != nil {
				return err
			}
			return requireMetrics(authCtx, tx, p, "delivery.read", project)
		})
	}
	if err := authorize(); err != nil {
		respondError(w, err)
		return
	}
	select {
	case m.flowSlots <- struct{}{}:
		defer func() { <-m.flowSlots }()
	default:
		w.Header().Set("Retry-After", "15")
		respondError(w, fail(429, "too many flow streams; retry later"))
		return
	}
	// LISTEN is session state on its own connection, never a pool slot.
	connectCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	conn, err := pgx.ConnectConfig(connectCtx, m.pool.Config().ConnConfig.Copy())
	stop()
	if err != nil {
		respondError(w, err)
		return
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = conn.Close(closeCtx)
	}()
	if _, err = conn.Exec(ctx, "LISTEN aeon_events"); err != nil {
		respondError(w, err)
		return
	}
	// Subscribed before the boundary read, so nothing committed later is missed.
	newest, err := m.flowNewest(ctx, p, project)
	if err != nil {
		respondError(w, err)
		return
	}
	if latest {
		after = newest
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	flush := func(payload string) error {
		if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, payload); err != nil {
			return err
		}
		return rc.Flush()
	}
	if flush(fmt.Sprintf("id: %d\nevent: stream.ready\ndata: {\"after\":%d}\n\n", after, after)) != nil {
		return
	}
	heartbeat := time.Now().Add(flowStreamHeartbeat)
	for ctx.Err() == nil {
		hints, err := m.flowHints(ctx, p, project, after)
		if err != nil {
			return
		}
		for _, h := range hints {
			data, err := json.Marshal(h)
			if err != nil || flush(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", h.ID, h.Type, data)) != nil {
				return
			}
			after = h.ID
		}
		if len(hints) == flowStreamBatch {
			continue
		}
		if err := waitFlowNotify(ctx, conn, p.TenantID, heartbeat); err != nil {
			if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return
			}
			if authorize() != nil || flush("event: stream.ping\ndata: {}\n\n") != nil {
				return
			}
			heartbeat = time.Now().Add(flowStreamHeartbeat)
		}
	}
}

// The flow hints are read as a service: event-family visibility hides
// delivery events from project readers, so the project and the reader's
// delivery.read are checked explicitly instead, and only ids leave.
func (m *Module) flowNewest(ctx context.Context, p tenant.Principal, project string) (int64, error) {
	var newest int64
	err := db.InTenant(db.AllProjects(ctx, "delivery flow stream: value-free hints of one authorized project"), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM events WHERE type=ANY($1::text[]) AND node_id=$2`, flowEventTypes, project).Scan(&newest)
	})
	return newest, err
}

func (m *Module) flowHints(ctx context.Context, p tenant.Principal, project string, after int64) ([]flowHint, error) {
	out := []flowHint{}
	err := db.InTenant(db.AllProjects(ctx, "delivery flow stream: value-free hints of one authorized project"), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,type,at,after FROM events WHERE type=ANY($1::text[]) AND node_id=$2 AND id>$3 ORDER BY id LIMIT $4`, flowEventTypes, project, after, flowStreamBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h flowHint
			var raw []byte
			if err := rows.Scan(&h.ID, &h.Type, &h.At, &raw); err != nil {
				return err
			}
			var ids struct {
				Item     string  `json:"item_id"`
				Step     *string `json:"step_id"`
				Incident *string `json:"incident_id"`
			}
			if err := json.Unmarshal(raw, &ids); err != nil {
				return err
			}
			h.At, h.ItemID, h.StepID, h.IncidentID = h.At.UTC(), ids.Item, ids.Step, ids.Incident
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// waitFlowNotify returns nil on a commit in this tenant and
// context.DeadlineExceeded at the heartbeat.
func waitFlowNotify(ctx context.Context, conn *pgx.Conn, tid string, deadline time.Time) error {
	for {
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		n, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return context.DeadlineExceeded
			}
			return err
		}
		var hint struct {
			TenantID string `json:"tenant_id"`
		}
		if json.Unmarshal([]byte(n.Payload), &hint) == nil && hint.TenantID == tid {
			return nil
		}
	}
}

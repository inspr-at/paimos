// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// The existing aeon_events channel is a commit hint, never a source of process
// authority. No content or lease is returned and no receipt is advanced here.
func (m *Module) notifications(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		workorders.WriteError(w, workorders.Fail(401, "authentication required"))
		return
	}
	if p.Kind != tenant.Agent {
		workorders.WriteError(w, workorders.Fail(403, "agent required"))
		return
	}
	if !workorders.UUID(r.PathValue("projectId")) || !workorders.UUID(r.PathValue("sessionId")) {
		workorders.WriteError(w, workorders.Fail(403, "harness worker proof rejected"))
		return
	}
	// Authorize before allocating a dedicated LISTEN connection, then authorize
	// again after subscribing. The initial wake closes the replay/listen gap.
	if _, err := m.notificationHint(r, p, 0); err != nil {
		workorders.WriteError(w, err)
		return
	}
	connectCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	conn, err := pgx.ConnectConfig(connectCtx, m.pool.Config().ConnConfig.Copy())
	if err == nil {
		_, err = conn.Exec(connectCtx, "LISTEN aeon_events")
	}
	cancel()
	if conn != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = conn.Close(ctx)
		}()
	}
	if err != nil {
		workorders.WriteError(w, err)
		return
	}
	if _, err := m.notificationHint(r, p, 0); err != nil {
		workorders.WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	send := func(name string) error {
		if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: {}\n\n", name); err != nil {
			return err
		}
		return rc.Flush()
	}
	if err := send("harness.wake"); err != nil {
		return
	}
	nextBeat := time.Now().Add(15 * time.Second)
	for r.Context().Err() == nil {
		// A busy tenant cannot postpone authority checks or the keepalive.
		if !time.Now().Before(nextBeat) {
			if _, err := m.notificationHint(r, p, 0); err != nil {
				return
			}
			if err := send("stream.ping"); err != nil {
				return
			}
			nextBeat = time.Now().Add(15 * time.Second)
		}
		waitCtx, cancel := context.WithDeadline(r.Context(), nextBeat)
		notice, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil {
				continue
			}
			return
		}
		var hint struct {
			TenantID string `json:"tenant_id"`
			ID       int64  `json:"id"`
		}
		if json.Unmarshal([]byte(notice.Payload), &hint) != nil || hint.TenantID != p.TenantID || hint.ID <= 0 {
			continue
		}
		relevant, err := m.notificationHint(r, p, hint.ID)
		if err != nil {
			return
		}
		if relevant {
			if err := send("harness.wake"); err != nil {
				return
			}
		}
	}
}

func (m *Module) notificationHint(r *http.Request, p tenant.Principal, eventID int64) (bool, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	relevant := false
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Revalidate the exact bearer, not just the principal cached at connection.
		if _, err := workorders.CurrentKeyPrincipal(r, tx, p, "harness.worker"); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
			return workorders.Fail(403, "harness.worker permission required")
		}
		s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
		if errors.Is(err, pgx.ErrNoRows) {
			return workorders.Fail(403, "harness worker proof rejected")
		}
		if err != nil {
			return err
		}
		if err := proof(s, r, p); err != nil {
			return err
		}
		if s.Management != "managed" {
			return workorders.Fail(409, "managed worker required")
		}
		if eventID == 0 {
			return nil
		}
		// Principal inbox events can be hidden from project event readers. Read
		// only this worker's recipient rows, just as drain does, by the committed
		// sent-event ID; never broaden its event-history or inbox permissions.
		return tx.QueryRow(ctx, `SELECT
   EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=$1 AND e.id=$2
    AND e.type IN ('harness.control_requested','harness.pause_requested','harness.pause_interrupt_requested','harness.pause_stop_requested','harness.pause_level_changed','harness.resume_requested','harness.tier_requested')
    AND e.node_id=$3::uuid AND (e.after->>'session_id'=$4 OR e.after->>'id'=$4 OR e.after->'session'->>'id'=$4))
   OR EXISTS(SELECT 1 FROM inbox_messages m WHERE m.tenant_id=$1 AND m.sent_event_id=$2
    AND m.content_mode='durable' AND m.chat_thread_id IS NULL AND m.recipient_principal_id=$5::uuid
    AND (m.recipient_session_id IS NULL OR m.recipient_session_id=$4::uuid))`, p.TenantID, eventID, s.ProjectID, s.ID, s.AgentPrincipalID).Scan(&relevant)
	})
	return relevant, err
}

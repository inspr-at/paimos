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
	// Reconnect's initial wake recovers control/inbox changes during recycling.
	ctx, stop := context.WithTimeout(r.Context(), db.ListenerMaxLifetime)
	defer stop()
	r = r.WithContext(ctx)
	// Authorize before joining the shared LISTEN connection, then authorize
	// again after subscribing. The initial wake closes the replay/listen gap.
	if _, _, err := m.notificationHint(r, p, nil); err != nil {
		workorders.WriteError(w, err)
		return
	}
	conn, err := db.SubscribeNotifications(r.Context(), m.pool, "aeon_events", p.TenantID)
	if err != nil {
		workorders.WriteError(w, err)
		return
	}
	defer func() { _ = conn.Close(context.Background()) }()
	after, _, err := m.notificationHint(r, p, nil)
	if err != nil {
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
	stream := harnessNotificationStream{
		tenantID: p.TenantID,
		after:    after,
		now:      time.Now,
		check: func(after int64) (int64, bool, error) {
			return m.notificationHint(r, p, &after)
		},
		send: send,
	}
	stream.run(r.Context(), conn)
}

// The position comes only from an authorized durable read. Queued notifications
// through it are redundant, even if their payloads are repeated or forged.
// The injected clock lets tests establish a due heartbeat during a backlog.
type harnessNotificationStream struct {
	tenantID string
	after    int64
	now      func() time.Time
	check    func(int64) (int64, bool, error)
	send     func(string) error
}

func (s *harnessNotificationStream) refresh(force bool) error {
	through, relevant, err := s.check(s.after)
	if err != nil {
		return err
	}
	s.after = through
	if relevant || force {
		return s.send("harness.wake")
	}
	return nil
}

func (s *harnessNotificationStream) run(ctx context.Context, conn db.NotificationListener) {
	nextBeat := s.now().Add(15 * time.Second)
	for ctx.Err() == nil {
		// A busy tenant cannot postpone authority checks or the keepalive.
		if !s.now().Before(nextBeat) {
			// Reconcile from durable state even if its notification was missed.
			if err := s.refresh(false); err != nil {
				return
			}
			if err := s.send("stream.ping"); err != nil {
				return
			}
			nextBeat = s.now().Add(15 * time.Second)
		}
		waitCtx, cancel := context.WithDeadline(ctx, nextBeat)
		notice, err := conn.WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}
			return
		}
		if db.IsResync(notice) {
			if err := s.refresh(true); err != nil {
				return
			}
			continue
		}
		var hint struct {
			TenantID string `json:"tenant_id"`
			ID       int64  `json:"id"`
		}
		if json.Unmarshal([]byte(notice.Payload), &hint) != nil || hint.TenantID != s.tenantID || hint.ID <= s.after {
			continue
		}
		if err := s.refresh(false); err != nil {
			return
		}
	}
}

func (m *Module) notificationHint(r *http.Request, p tenant.Principal, after *int64) (int64, bool, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	through := int64(0)
	relevant := false
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Bound the batch BEFORE checking authority. A revocation can commit
		// after those checks; its newer events must wait for a fresh check,
		// rather than enter this batch through a later statement snapshot.
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT last_id FROM event_counters WHERE tenant_id=$1),0)`, p.TenantID).Scan(&through); err != nil {
			return err
		}
		// Revalidate the exact bearer, not just the principal cached at connection.
		if _, err := workorders.CurrentKeyPrincipal(r, tx, p, "harness.worker"); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return workorders.Fail(403, "harness.worker permission required")
			}
			return err
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
		if after == nil || *after == through {
			return nil
		}
		// The payload only wakes this read: it cannot advance the position,
		// select a session, or stand in for the current bearer/proof checks.
		// Principal inbox events can be hidden from project event readers. Read
		// only this worker's recipient rows, just as drain does, by the committed
		// sent-event ID; never broaden its event-history or inbox permissions.
		return tx.QueryRow(ctx, `SELECT
   EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=$1 AND e.id>$2 AND e.id<=$3
    AND e.type IN ('harness.control_requested','harness.pause_requested','harness.pause_interrupt_requested','harness.pause_stop_requested','harness.pause_level_changed','harness.resume_requested','harness.tier_requested')
    AND e.node_id=$4::uuid AND (e.after->>'session_id'=$5 OR e.after->>'id'=$5 OR e.after->'session'->>'id'=$5))
   OR EXISTS(SELECT 1 FROM inbox_messages m WHERE m.tenant_id=$1 AND m.sent_event_id>$2 AND m.sent_event_id<=$3
    AND m.content_mode='durable' AND m.chat_thread_id IS NULL AND m.recipient_principal_id=$6::uuid
    AND (m.recipient_session_id IS NULL OR m.recipient_session_id=$5::uuid))`, p.TenantID, *after, through, s.ProjectID, s.ID, s.AgentPrincipalID).Scan(&relevant)
	})
	return through, relevant, err
}

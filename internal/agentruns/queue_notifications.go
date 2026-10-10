// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

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

// queueNotificationHint reveals no event history or account state. The exact
// bearer remains live, and only this principal's queued run can produce a hint.
func (m *module) queueNotificationHint(r *http.Request, p tenant.Principal, eventID int64) (bool, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	relevant := false
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		current, err := workorders.CurrentKeyPrincipal(r, tx, p, "run.read")
		if err != nil {
			return err
		}
		if eventID == 0 {
			return nil
		}
		var project *string
		err = tx.QueryRow(ctx, `SELECT n.project_id::text FROM events e
		JOIN agent_runs a ON a.tenant_id=e.tenant_id AND a.id::text=e.after->>'id'
		JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.work_order_id
		WHERE e.tenant_id=$1 AND e.id=$2 AND e.type IN ('run.created','run.capacity_override')
		AND a.agent_principal_id=$3 AND a.status='queued' AND n.deleted_at IS NULL`, p.TenantID, eventID, p.ID).Scan(&project)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		scope := authz.Scope{}
		if project != nil {
			scope.ProjectID = *project
		}
		if err := authz.RequireTx(ctx, tx, current, "run.read", scope); err != nil {
			return nil
		}
		relevant = true
		return nil
	})
	return relevant, err
}

func (m *module) queueNotifications(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
		workorders.WriteError(w, workorders.Fail(401, "authentication required"))
		return
	}
	if p.Kind != tenant.Agent {
		workorders.WriteError(w, workorders.Fail(403, "agent required"))
		return
	}
	ctx, stop := context.WithTimeout(r.Context(), db.ListenerMaxLifetime)
	defer stop()
	r = r.WithContext(ctx)
	if _, err := m.queueNotificationHint(r, p, 0); err != nil {
		workorders.WriteError(w, err)
		return
	}
	// Bound dedicated backends, including concurrent reconnects for one daemon.
	m.queueHintMu.Lock()
	if m.queueHintReaders == nil {
		m.queueHintReaders = make(map[string]int)
	}
	key := p.TenantID + ":" + p.ID
	total := 0
	for _, count := range m.queueHintReaders {
		total += count
	}
	if total >= 32 || m.queueHintReaders[key] >= 2 {
		m.queueHintMu.Unlock()
		workorders.WriteError(w, workorders.Fail(429, "queue listener limit"))
		return
	}
	m.queueHintReaders[key]++
	m.queueHintMu.Unlock()
	defer func() {
		m.queueHintMu.Lock()
		defer m.queueHintMu.Unlock()
		m.queueHintReaders[key]--
		if m.queueHintReaders[key] == 0 {
			delete(m.queueHintReaders, key)
		}
	}()
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := pgx.ConnectConfig(op, m.pool.Config().ConnConfig.Copy())
	if err == nil {
		_, err = conn.Exec(op, "LISTEN aeon_events")
	}
	cancel()
	if conn != nil {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = conn.Close(closeCtx)
		}()
	}
	if err != nil {
		workorders.WriteError(w, err)
		return
	}
	// Initial wake after LISTEN closes the startup/reconnect gap without replay.
	if _, err := m.queueNotificationHint(r, p, 0); err != nil {
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
	if err := send("queue.wake"); err != nil {
		return
	}
	nextBeat := time.Now().Add(15 * time.Second)
	for ctx.Err() == nil {
		if !time.Now().Before(nextBeat) {
			if _, err := m.queueNotificationHint(r, p, 0); err != nil {
				return
			}
			if err := send("stream.ping"); err != nil {
				return
			}
			nextBeat = time.Now().Add(15 * time.Second)
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
		var hint struct {
			TenantID string `json:"tenant_id"`
			ID       int64  `json:"id"`
		}
		if len(notice.Payload) > 1024 || json.Unmarshal([]byte(notice.Payload), &hint) != nil || hint.TenantID != p.TenantID || hint.ID <= 0 {
			continue
		}
		relevant, err := m.queueNotificationHint(r, p, hint.ID)
		if err != nil {
			return
		}
		if relevant {
			if err := send("queue.wake"); err != nil {
				return
			}
		}
	}
}

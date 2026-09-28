// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// This relay is deliberately independent of events, telemetry and persistence.
// It has no history, replay cursor, disk journal or disconnected-viewer buffer.
// A slow viewer loses its connection, never accumulates an unbounded queue.
type watchRelay struct {
	mu      sync.Mutex
	viewers map[string]map[chan string]bool
	count   int
}

func (b *watchRelay) subscribe(key string) (chan string, func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count >= 256 || len(b.viewers[key]) >= 16 {
		return nil, nil, false
	}
	if b.viewers == nil {
		b.viewers = make(map[string]map[chan string]bool)
	}
	if b.viewers[key] == nil {
		b.viewers[key] = make(map[chan string]bool)
	}
	c := make(chan string, 1)
	b.viewers[key][c] = true
	b.count++
	return c, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.viewers[key][c] {
			delete(b.viewers[key], c)
			b.count--
			close(c)
		}
		if len(b.viewers[key]) == 0 {
			delete(b.viewers, key)
		}
	}, true
}
func (b *watchRelay) publish(key, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.viewers[key] {
		select {
		case c <- text:
		default:
			delete(b.viewers[key], c)
			b.count--
			close(c)
		}
	}
	if len(b.viewers[key]) == 0 {
		delete(b.viewers, key)
	}
}
func inertText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}
func (m *Module) watchAllowed(ctx context.Context, p tenant.Principal, project, session string) error {
	if p.Kind != tenant.Person || !uuidRE.MatchString(project) || !uuidRE.MatchString(session) {
		return fail(403, "forbidden", "person conversation permission required")
	}
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(ctx, tx, p, "harness.watch", authz.Scope{ProjectID: project}) != nil {
			return fail(403, "forbidden", "explicit harness.watch permission required")
		}
		var live bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_attach_requests a
 JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 JOIN harness_sessions s ON s.tenant_id=a.tenant_id AND s.id=a.session_id
 WHERE a.project_id=$1 AND a.session_id=$2 AND a.state='active' AND a.lease_until>clock_timestamp()
 AND c.state='connected' AND q.state='redeemed' AND s.stopped_at IS NULL AND s.archived_at IS NULL
 AND s.project_id=a.project_id AND s.ticket_node_id=a.ticket_id)`, project, session).Scan(&live)
		if err != nil {
			return err
		}
		if !live {
			return fail(410, "attach_ended", "watch unavailable or lease ended")
		}
		var snapshot attachwatch.Snapshot
		var owner string
		if err = tx.QueryRow(ctx, `SELECT snapshot,owner_id::text FROM harness_attach_requests WHERE session_id=$1`, session).Scan(&snapshot, &owner); err != nil {
			return err
		}
		return attachScope(ctx, tx, p.TenantID, owner, snapshot)
	})
}
func (m *Module) attachStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		WriteError(w, fail(403, "forbidden", "person required"))
		return
	}
	project, session := r.PathValue("projectId"), r.PathValue("sessionId")
	if err := m.watchAllowed(r.Context(), p, project, session); err != nil {
		WriteError(w, err)
		return
	}
	ch, unsubscribe, ok := m.watch.subscribe(p.TenantID + "/" + session)
	if !ok {
		WriteError(w, fail(429, "rate_limited", "watch viewer limit reached"))
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	controller := http.NewResponseController(w)
	write := func(kind string, v any) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(3 * time.Second))
		raw, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !write("keepalive", nil) {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var text string
		select {
		case <-r.Context().Done():
			return
		case v, open := <-ch:
			if !open {
				write("end", nil)
				return
			}
			text = v
		case <-tick.C:
		}
		// Recheck before every delivery as well as when idle. Failure closes access.
		if m.watchAllowed(r.Context(), p, project, session) != nil {
			write("end", nil)
			return
		}
		if text != "" {
			if !write("text", text) {
				return
			}
		} else if !write("keepalive", nil) {
			return
		}
	}
}

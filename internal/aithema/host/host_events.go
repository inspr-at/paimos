// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"strconv"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

// queueHostEvents projects native host events into value-free accelerators.
// The durable cursor and outbox rows commit together, and stable keys make
// repeated scans harmless. Hydration still uses the fenced host read routes.
func (m *Module) queueHostEvents(ctx context.Context, tid string) error {
	var sessions []string
	if err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT h.sid::text FROM aithema_host_sessions h JOIN aithema_sessions s USING(tenant_id,sid) WHERE h.tenant_id=$1 AND NOT s.tombstone AND NOT s.suspended ORDER BY h.sid`, tid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				return err
			}
			sessions = append(sessions, sid)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, sid := range sessions {
		if err := db.InTenant(ctx, m.Pool, tid, func(tx pgx.Tx) error {
			var cursor, high int64
			if err := tx.QueryRow(ctx, `SELECT event_cursor FROM aithema_host_sessions WHERE tenant_id=$1 AND sid=$2 FOR UPDATE`, tid, sid).Scan(&cursor); err != nil {
				return err
			}
			state, err := m.Journal.LockAuthority(ctx, tx, tid, sid)
			if err != nil {
				return err
			}
			if state.Tombstone || state.Suspended {
				return nil
			}
			a, _, err := m.live(ctx, tx, tid, state, "intake.read")
			if err != nil {
				return nil
			}
			if err := tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, tid).Scan(&high); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT e.id,e.type FROM events e LEFT JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id WHERE e.tenant_id=$1 AND e.id>$2 AND e.id<=$3 AND (e.node_id=$4::uuid OR n.project_id=$4::uuid OR e.after->>'project_node_id'=$4::text) AND e.type NOT LIKE 'aithema.%' ORDER BY e.id LIMIT 100`, tid, cursor, high, a.Project)
			if err != nil {
				return err
			}
			type notification struct {
				ID   int64
				Type string
			}
			var notifications []notification
			for rows.Next() {
				var n notification
				if err := rows.Scan(&n.ID, &n.Type); err != nil {
					rows.Close()
					return err
				}
				notifications = append(notifications, n)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, n := range notifications {
				if _, err := enqueue(ctx, tx, tid, sid, "host-event", "event:"+sid+":"+strconv.FormatInt(n.ID, 10), map[string]any{"sid": sid, "event_id": n.ID, "type": n.Type}); err != nil {
					return err
				}
			}
			if len(notifications) == 100 {
				high = notifications[99].ID
			}
			_, err = tx.Exec(ctx, `UPDATE aithema_host_sessions SET event_cursor=$3 WHERE tenant_id=$1 AND sid=$2`, tid, sid, high)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

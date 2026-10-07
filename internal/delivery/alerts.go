// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Alert struct {
	ItemID    string     `json:"item_id"`
	State     State      `json:"state"`
	Since     time.Time  `json:"state_since"`
	AlertedAt time.Time  `json:"alerted_at"`
	Recipient *string    `json:"recipient_principal_id"`
	MessageID *string    `json:"inbox_message_id"`
	ClearedAt *time.Time `json:"cleared_at"`
}

func alertKey(tid string, i Item) string {
	h := sha256.Sum256([]byte(tid + "|" + i.ID + "|" + string(i.State) + "|" + i.Since.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h[:])
}

func alertBody(i Item, key string, now time.Time) string {
	if key == "" {
		key = "Unlinked delivery"
	}
	pr := "no PR yet"
	if i.PR != nil {
		pr = fmt.Sprintf("PR #%d", *i.PR)
	}
	// Platform text is kept to a bounded plain line even for imported keys.
	key = strings.Join(strings.Fields(key), " ")
	return fmt.Sprintf("%s · %s: %s for %d min (deadline %s, %d min) · next: %s", key, pr, i.State,
		int64(now.Sub(i.Since)/time.Minute), i.Deadline.UTC().Format(time.RFC3339), int64(i.Deadline.Sub(i.Since)/time.Minute), i.Owner)
}

// SweepAlerts is deterministic with the module's injected clock. Episode rows,
// inbox acceptance and the alert event commit together. Replica exclusion is
// per tenant; the tenant/tree fence rechecks live state, lead and authority.
func (m *Module) SweepAlerts(ctx context.Context, tid string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	lock := "aeon-delivery-stall-alerts:" + tid
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, lock).Scan(&locked); err != nil || !locked {
		return 0, err
	}
	defer unlock(conn, lock)
	service := db.AllProjects(ctx, "delivery stall alerts")
	now := m.now().UTC()
	count := 0
	var cursor string
	for {
		ids := []string{}
		err = db.InTenant(service, m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT i.id FROM delivery_items i `+alertSettingsJoin+`
 WHERE `+alertDeadlineSQL+` < $1 AND NOT EXISTS (
  SELECT 1 FROM delivery_alerts a WHERE a.item_id=i.id AND a.state=i.state AND a.state_since=i.state_since)
 UNION SELECT a.item_id FROM delivery_alerts a WHERE a.cleared_at IS NULL AND NOT EXISTS (
  SELECT 1 FROM delivery_items i WHERE i.id=a.item_id AND i.state=a.state AND i.state_since=a.state_since))
 SELECT id::text FROM candidates WHERE ($2::uuid IS NULL OR id>$2) ORDER BY id LIMIT 100`, now, nullable(cursor))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if err != nil {
			return count, err
		}
		for _, id := range ids {
			alerted, err := m.alertItem(service, tid, id, now)
			if err != nil {
				return count, err
			}
			if alerted {
				count++
			}
		}
		if len(ids) < 100 {
			return count, nil
		}
		cursor = ids[len(ids)-1]
	}
}

func (m *Module) alertItem(ctx context.Context, tid, id string, now time.Time) (bool, error) {
	// Ensure can create its own event. Prepare it in an independent transaction
	// so the final write takes all resource/FK locks before any event counter.
	var actor tenant.Principal
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		var err error
		actor, err = systemactor.Ensure(ctx, tx, tid)
		return err
	})
	if err != nil {
		return false, err
	}
	alerted := false
	err = db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, tid); err != nil {
			return err
		}
		i, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE delivery_alerts a SET cleared_at=$2 WHERE item_id=$1 AND cleared_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM delivery_items i WHERE i.id=a.item_id AND i.state=a.state AND i.state_since=a.state_since)`, id, now); err != nil {
			return err
		}
		if i == nil {
			return nil
		}
		settings, err := settingsTx(ctx, tx, i.Project)
		if err != nil {
			return err
		}
		minutes := settings.Deadlines[i.State]
		if minutes <= 0 {
			return nil
		}
		deadline := i.Since.Add(time.Duration(minutes) * time.Minute)
		i.Deadline = &deadline
		if !deadline.Before(now) {
			return nil
		}
		var present bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_alerts WHERE item_id=$1 AND state=$2 AND state_since=$3)`, id, i.State, i.Since).Scan(&present); err != nil || present {
			return err
		}
		var recipient *string
		if err = tx.QueryRow(ctx, `SELECT coalesce(
 (SELECT owner_person_id::text FROM project_lead_settings WHERE project_id=$1),
 (SELECT owner_person_id::text FROM project_lead_settings WHERE project_id IS NULL))`, i.Project).Scan(&recipient); err != nil {
			return err
		}
		// System is an internal notice actor, with no user/key grant fabricated.
		// Recheck the recipient's right to this delivery under the access fence;
		// an inactive or no-longer-authorized lead receives no project details.
		if recipient != nil {
			lead := tenant.Principal{ID: *recipient, TenantID: tid, Kind: tenant.Person}
			for _, permission := range []string{"delivery.read", "inbox.read"} {
				err = authz.RequireTx(ctx, tx, lead, permission, scope(i.Project))
				if errors.Is(err, authz.ErrForbidden) {
					recipient = nil
					break
				}
				if err != nil {
					return err
				}
			}
		}
		var ticketKey string
		if i.Ticket != nil {
			if err = tx.QueryRow(ctx, `SELECT left(key,128) FROM nodes WHERE id=$1 AND deleted_at IS NULL FOR KEY SHARE`, i.Ticket).Scan(&ticketKey); err != nil {
				return err
			}
		}
		if i.Project != nil {
			if _, err = tx.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR KEY SHARE`, i.Project); err != nil {
				return err
			}
		}
		parents := []string{actor.ID}
		if recipient != nil {
			parents = append(parents, *recipient)
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, parents); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO delivery_alerts(tenant_id,item_id,state,state_since,alerted_at,recipient_principal_id)
 VALUES($1,$2,$3,$4,$5,$6)`, tid, id, i.State, i.Since, now, recipient); err != nil {
			return err
		}
		// All existing rows and FK parents are fenced before inbox's first event.
		var messageID *string
		if recipient != nil {
			msg, err := inbox.AcceptMessageTx(ctx, tx, actor, inbox.Acceptance{
				RecipientPrincipalID: *recipient, SenderLabel: "Delivery", ProjectID: i.Project,
				Body: alertBody(*i, ticketKey, now), IdempotencyKey: alertKey(tid, *i),
			})
			if err != nil {
				return err
			}
			messageID = &msg.ID
			// This updates only our freshly inserted, already locked row. There
			// is deliberately no message FK to acquire after the event counter.
			if _, err = tx.Exec(ctx, `UPDATE delivery_alerts SET inbox_message_id=$4 WHERE item_id=$1 AND state=$2 AND state_since=$3`, id, i.State, i.Since, messageID); err != nil {
				return err
			}
		}
		_, err = events.Append(ctx, tx, actor, events.Change{Type: "delivery.stall_alerted", NodeID: i.Ticket, At: &now,
			After: map[string]any{"item_id": id, "state": i.State, "state_since": i.Since, "deadline_at": deadline,
				"owner": i.Owner, "recipient_principal_id": recipient, "inbox_message_id": messageID}})
		alerted = err == nil
		return err
	})
	return alerted && err == nil, err
}

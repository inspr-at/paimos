// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
)

// All predicates use inbox_messages alias m. Desk replies retain their own
// replay and thirty-minute alert protocol, even when answered by a person.
const personSessionChatSQL = `m.content_mode='durable' AND m.chat_thread_id IS NULL
 AND m.recipient_session_id IS NOT NULL
 AND EXISTS(SELECT 1 FROM principals sp WHERE sp.tenant_id=m.tenant_id AND sp.id=m.sender_principal_id AND sp.kind='person')
 AND NOT EXISTS(SELECT 1 FROM inbox_compat_messages dc WHERE dc.tenant_id=m.tenant_id AND dc.inbox_message_id=m.id AND dc.key_digest='desk/'||dc.id::text)`

const chatPickupAvailableSQL = `(m.pickup_lease_until IS NULL OR m.pickup_lease_until<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM harness_deliveries h WHERE h.tenant_id=m.tenant_id AND h.message_id=m.id AND h.completed_at IS NULL AND h.released_at IS NULL)`

// sweepChatPickup shares Desk's delivery.stall_alerted channel. A receipt owns
// its one-time notice marker; each bounded candidate is rechecked under the
// session/message/receipt locks, so pickup and competing sweepers cannot race it.
func (s *Sweeper) sweepChatPickup(ctx context.Context, tid string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := s.clock().UTC()
	var ids []string
	err := db.InTenant(ctx, s.pool, tid, func(tx pgx.Tx) error {
		settings, err := loadDeliverySettings(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT m.id::text FROM inbox_messages m
 JOIN inbox_receipts r ON r.tenant_id=m.tenant_id AND r.message_id=m.id
 JOIN harness_sessions hs ON hs.tenant_id=m.tenant_id AND hs.id=m.recipient_session_id
 WHERE (`+personSessionChatSQL+`) AND r.state='queued' AND r.pickup_stalled_at IS NULL
 AND m.fetched_at IS NULL AND m.acked_at IS NULL AND (m.expires_at IS NULL OR m.expires_at>$1)
 AND hs.stopped_at IS NULL AND hs.archived_at IS NULL
 AND m.created_at+make_interval(secs=>$2)<=$1
 ORDER BY m.created_at,m.id LIMIT $3`, now, settings.ChatPickupStallSeconds, sweepBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil || len(ids) == 0 {
		return err
	}
	// Creating the System actor can append an event. Commit that preparation
	// separately, before any alert transaction takes existing resource locks.
	var sysID string
	err = db.InTenant(ctx, s.pool, tid, func(tx pgx.Tx) error {
		sys, err := systemActor(ctx, tx, tid)
		sysID = sys.ID
		return err
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.alertChatPickup(ctx, tid, sysID, id, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sweeper) alertChatPickup(ctx context.Context, tid, sysID, id string, now time.Time) error {
	return db.InTenant(ctx, s.pool, tid, func(tx pgx.Tx) error {
		var project, session, target string
		var owner, lead *string
		err := tx.QueryRow(ctx, `SELECT hs.project_id::text,hs.id::text,coalesce(hs.display_label,p.name),hs.owner_principal_id::text,ps.agent_principal_id::text
 FROM harness_sessions hs JOIN principals p ON p.tenant_id=hs.tenant_id AND p.id=hs.agent_principal_id
 LEFT JOIN harness_sessions ps ON ps.tenant_id=hs.tenant_id AND ps.id=hs.parent_id AND ps.project_id=hs.project_id AND ps.role='coordinator'
 WHERE hs.id=(SELECT recipient_session_id FROM inbox_messages WHERE id=$1::uuid)
 AND hs.stopped_at IS NULL AND hs.archived_at IS NULL FOR KEY SHARE OF hs`, id).Scan(&project, &session, &target, &owner, &lead)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var sender, recipient, body string
		var since time.Time
		err = tx.QueryRow(ctx, `SELECT m.sender_principal_id::text,m.recipient_principal_id::text,m.body,m.created_at
 FROM inbox_messages m WHERE m.id=$1::uuid AND (`+personSessionChatSQL+`)
 AND m.fetched_at IS NULL AND m.acked_at IS NULL AND (m.expires_at IS NULL OR m.expires_at>$2)
 FOR NO KEY UPDATE OF m`, id, now).Scan(&sender, &recipient, &body, &since)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		settings, err := loadDeliverySettings(ctx, tx)
		if err != nil || now.Before(since.Add(time.Duration(settings.ChatPickupStallSeconds)*time.Second)) {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE inbox_receipts SET pickup_stalled_at=$2 WHERE message_id=$1::uuid AND state='queued' AND pickup_stalled_at IS NULL`, id, now)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		// Fence every existing FK/target before the event counter. postNotice
		// only inserts fresh identities after these locks are held.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM nodes WHERE id=$1::uuid FOR KEY SHARE`, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{sender, sysID}); err != nil {
			return err
		}
		var targets int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM inbox_delivery_targets WHERE principal_id=$1::uuid AND kind='webhook' AND enabled ORDER BY id LIMIT 33 FOR KEY SHARE) t`, sender).Scan(&targets); err != nil {
			return err
		}
		if targets > 32 {
			return errors.New("chat pickup notice target limit exceeded")
		}
		sys, err := systemActor(ctx, tx, tid)
		if err != nil {
			return err
		}
		deadline := since.Add(time.Duration(settings.ChatPickupStallSeconds) * time.Second)
		_, err = events.Append(ctx, tx, sys, events.Change{Type: "delivery.stall_alerted", NodeID: &project, At: &now,
			After: map[string]any{"source": "session_chat", "item_id": id, "message_id": id, "recipient_session_id": session,
				"recipient_principal_id": recipient, "owner_principal_id": owner, "coordinator_principal_id": lead,
				"state": "chat_not_picked_up", "state_since": since, "deadline_at": deadline, "owner": "coordinator"}})
		if err != nil {
			return err
		}
		notice := fmt.Sprintf("Still waiting for %s to pick up your message. The session has not picked it up after %d minutes. The session owner or lead has been alerted. The message stays queued; it does not need to be sent again. Message %s.\n\n> %s", target, settings.ChatPickupStallSeconds/60, id, excerptOf(body, 160))
		return postNotice(ctx, tx, sys, sender, nil, "chat-pickup-stalled/"+id, notice)
	})
}

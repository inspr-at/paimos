// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	maxBodyRunes = 65536
	maxKeyRunes  = 128
)

// Message is one durable inbox row. AckedAt is null until the recipient acks.
type Message struct {
	RecipientSessionID   *string `json:"recipient_session_id,omitempty"`
	SenderSessionID      *string `json:"sender_session_id,omitempty"`
	SenderLabel          string  `json:"sender_label,omitempty"`
	frozenSenderLabel    string
	ID                   string     `json:"id"`
	SenderPrincipalID    string     `json:"sender_principal_id"`
	RecipientPrincipalID string     `json:"recipient_principal_id"`
	Body                 string     `json:"body"`
	IdempotencyKey       string     `json:"idempotency_key"`
	ReplyToID            *string    `json:"reply_to_id,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	SentEventID          int64      `json:"sent_event_id"`
	CreatedAt            time.Time  `json:"created_at"`
	AckedAt              *time.Time `json:"acked_at"`
}

// Page is a listen result. NextAfter is the sent event id to pass as after.
type Page struct {
	Items     []Message `json:"items"`
	NextAfter int64     `json:"next_after"`
}

// messageMeta is the tenant-event snapshot. It intentionally has no body.
type messageMeta struct {
	ID                   string     `json:"id"`
	SenderPrincipalID    string     `json:"sender_principal_id"`
	RecipientPrincipalID string     `json:"recipient_principal_id"`
	ReplyToID            *string    `json:"reply_to_id,omitempty"`
	IdempotencyKey       string     `json:"idempotency_key,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	SentEventID          int64      `json:"sent_event_id,omitempty"`
	AckedAt              *time.Time `json:"acked_at,omitempty"`
}

func metaFrom(m Message) messageMeta {
	return messageMeta{
		ID:                   m.ID,
		SenderPrincipalID:    m.SenderPrincipalID,
		RecipientPrincipalID: m.RecipientPrincipalID,
		ReplyToID:            m.ReplyToID,
		IdempotencyKey:       m.IdempotencyKey,
		ExpiresAt:            m.ExpiresAt,
		SentEventID:          m.SentEventID,
		AckedAt:              m.AckedAt,
	}
}

const messageCols = `recipient_session_id::text,sender_session_id::text,coalesce(sender_label,''),id::text, sender_principal_id::text, recipient_principal_id::text,
	reply_to_id::text, body, idempotency_key, expires_at, sent_event_id, created_at, acked_at`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.RecipientSessionID, &m.SenderSessionID, &m.frozenSenderLabel, &m.ID, &m.SenderPrincipalID, &m.RecipientPrincipalID, &m.ReplyToID,
		&m.Body, &m.IdempotencyKey, &m.ExpiresAt, &m.SentEventID, &m.CreatedAt, &m.AckedAt)
	if m.RecipientSessionID != nil || m.SenderSessionID != nil {
		m.SenderLabel = m.frozenSenderLabel
	}
	return m, err
}

type sendInput struct {
	RecipientSessionID *string
	SenderSessionID    *string
	Recipient          string
	Body               string
	Key                string
	ReplyTo            *string
	Expires            *time.Time
}

func (m *module) handleSend(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.authorizeSend(r.Context(), r, p); err != nil {
		failure(w, err)
		return
	}
	var body struct {
		RecipientSessionID   *string    `json:"recipient_session_id"`
		SenderSessionID      *string    `json:"sender_session_id"`
		RecipientPrincipalID string     `json:"recipient_principal_id"`
		Body                 string     `json:"body"`
		IdempotencyKey       string     `json:"idempotency_key"`
		ReplyToID            *string    `json:"reply_to_id"`
		ExpiresAt            *time.Time `json:"expires_at"`
	}
	if !decodeJSON(w, r, 512<<10, &body) {
		return
	}
	in, err := normalizeSend(p, body.RecipientPrincipalID, body.Body, body.IdempotencyKey, body.ReplyToID, body.ExpiresAt)
	if err != nil {
		failure(w, err)
		return
	}
	in.RecipientSessionID, in.SenderSessionID = body.RecipientSessionID, body.SenderSessionID
	if err := normalizeSession(in.RecipientSessionID); err != nil {
		failure(w, err)
		return
	}
	if err := normalizeSession(in.SenderSessionID); err != nil {
		failure(w, err)
		return
	}
	msg, err := m.send(r.Context(), p, in)
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, msg)
}

func normalizeSend(p tenant.Principal, recipient, body, key string, reply *string, expires *time.Time) (sendInput, error) {
	id, ok := parseUUID(recipient)
	if !ok {
		return sendInput{}, badRequest("invalid recipient_principal_id")
	}
	if strings.EqualFold(id, p.ID) {
		return sendInput{}, badRequest("sender and recipient must differ")
	}
	if body == "" || strings.ContainsRune(body, 0) || utf8.RuneCountInString(body) > maxBodyRunes {
		return sendInput{}, badRequest("invalid body")
	}
	if key == "" || strings.ContainsRune(key, 0) || utf8.RuneCountInString(key) > maxKeyRunes {
		return sendInput{}, badRequest("invalid idempotency_key")
	}
	var replyTo *string
	if reply != nil {
		if *reply == "" {
			return sendInput{}, badRequest("invalid reply_to_id")
		}
		rid, ok := parseUUID(*reply)
		if !ok {
			return sendInput{}, badRequest("invalid reply_to_id")
		}
		replyTo = &rid
	}
	if expires != nil && !expires.After(time.Now()) {
		return sendInput{}, badRequest("expires_at must be in the future")
	}
	return sendInput{Recipient: id, Body: body, Key: key, ReplyTo: replyTo, Expires: expires}, nil
}

func (m *module) send(ctx context.Context, p tenant.Principal, in sendInput) (Message, error) {
	var out Message
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// UUIDs are fixed width, so this key aliases only when the idempotency
		// key itself collides. Postgres text cannot store a NUL separator.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 22))`,
			p.TenantID+"/"+p.ID+"/"+in.Key); err != nil {
			return err
		}
		existing, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageCols+`
			FROM inbox_messages
			WHERE sender_principal_id = $1::uuid AND idempotency_key = $2
			FOR UPDATE`, p.ID, in.Key))
		if err == nil {
			if !sameSend(existing, in) {
				return errConflict
			}
			out = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var present bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id = $1::uuid)`, in.Recipient).Scan(&present); err != nil {
			return err
		}
		if !present {
			return errNotFound
		}
		if _, err := messageSession(ctx, tx, in.RecipientSessionID, in.Recipient, ""); err != nil {
			return err
		}
		senderLabel := p.Name
		if in.SenderSessionID != nil {
			senderLabel, err = messageSession(ctx, tx, in.SenderSessionID, p.ID, "")
			if err != nil {
				return err
			}
		}
		if in.ReplyTo != nil {
			var visible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages
				WHERE id = $1::uuid AND (sender_principal_id = $2::uuid OR recipient_principal_id = $2::uuid))`,
				*in.ReplyTo, p.ID).Scan(&visible); err != nil {
				return err
			}
			if !visible {
				return errNotFound
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return err
		}
		ev, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.sent", After: messageMeta{
			ID: id, SenderPrincipalID: p.ID, RecipientPrincipalID: in.Recipient,
			ReplyToID: in.ReplyTo, IdempotencyKey: in.Key, ExpiresAt: in.Expires,
		}})
		if err != nil {
			return err
		}
		out, err = scanMessage(tx.QueryRow(ctx, `INSERT INTO inbox_messages (
			tenant_id, id, sender_principal_id, recipient_principal_id, reply_to_id,
			sent_event_id, body, idempotency_key, expires_at,recipient_session_id,sender_session_id,sender_label)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, $9,$10::uuid,$11::uuid,$12)
			RETURNING `+messageCols,
			p.TenantID, id, p.ID, in.Recipient, in.ReplyTo, ev.ID, in.Body, in.Key, in.Expires, in.RecipientSessionID, in.SenderSessionID, senderLabel))
		if err != nil {
			return mapWrite(err)
		}
		if err := enqueueWakes(ctx, tx, p, out); err != nil {
			return err
		}
		return insertReceipt(ctx, tx, p, out.ID, "queued", receiptTarget{}, "", "")
	})
	return out, err
}

func sameSend(m Message, in sendInput) bool {
	if m.RecipientPrincipalID != in.Recipient || m.Body != in.Body || !sameSession(m.RecipientSessionID, in.RecipientSessionID) || !sameSession(m.SenderSessionID, in.SenderSessionID) {
		return false
	}
	if m.ReplyToID == nil || in.ReplyTo == nil {
		return m.ReplyToID == nil && in.ReplyTo == nil
	}
	return *m.ReplyToID == *in.ReplyTo
}

func enqueueWakes(ctx context.Context, tx pgx.Tx, p tenant.Principal, msg Message) error {
	return enqueueWakesWith(ctx, tx, p.TenantID, msg, func(c events.Change) error {
		_, err := events.Append(ctx, tx, p, c)
		return err
	})
}

// enqueueWakesWith lets System-authored notices append through their own path.
func enqueueWakesWith(ctx context.Context, tx pgx.Tx, tenantID string, msg Message, appendEvent func(events.Change) error) error {
	if msg.RecipientSessionID != nil {
		return nil
	} // Wake targets belong to the principal, not this generation.
	// Targets are soft-disabled, never removed here. A concurrent disable may
	// leave a queued wake, which claim drops after checking enabled. Do not
	// lock target state after the event counter: FK KEY SHARE is sufficient
	// and compatible with target writers' NO KEY UPDATE locks.
	rows, err := tx.Query(ctx, `SELECT id::text FROM inbox_delivery_targets
		WHERE principal_id = $1::uuid AND kind = 'webhook' AND enabled
		ORDER BY id`, msg.RecipientPrincipalID)
	if err != nil {
		return err
	}
	var targets []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, targetID := range targets {
		if _, err := tx.Exec(ctx, `INSERT INTO inbox_wakes (tenant_id, message_id, target_id)
			VALUES ($1::uuid, $2::uuid, $3::uuid)`, tenantID, msg.ID, targetID); err != nil {
			return mapWrite(err)
		}
		if err := appendEvent(events.Change{Type: "inbox.wake_queued", After: wakeMeta{
			MessageID: msg.ID, TargetID: targetID, EventID: msg.SentEventID,
		}}); err != nil {
			return err
		}
	}
	return nil
}

func (m *module) handleAck(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := parseUUID(r.PathValue("messageId"))
	if !ok {
		writeError(w, 404, "not_found", "not found")
		return
	}
	msg, err := m.ack(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (m *module) ack(ctx context.Context, p tenant.Principal, id string) (Message, error) {
	var out Message
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Lock order (AEON-280): the bound session first, then the message.
		// recipient and session are immutable, so an unlocked read picks the lock.
		var recipient, sender string
		var session *string
		err := tx.QueryRow(ctx, `SELECT recipient_principal_id::text,sender_principal_id::text,recipient_session_id::text FROM inbox_messages WHERE id=$1::uuid`, id).Scan(&recipient, &sender, &session)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if recipient != p.ID {
			if sender == p.ID {
				return errForbidden
			}
			return errNotFound
		}
		// Keep the generation live through commit, including idempotent retries.
		if _, err := listeningSession(ctx, tx, session, p.ID, ""); err != nil {
			return err
		}
		current, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageCols+`
			FROM inbox_messages WHERE id = $1::uuid FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if current.AckedAt != nil {
			out = current
			return nil
		}
		// A failed message is terminal: the sender was told it was not
		// delivered, so it is neither acknowledged nor returned (AEON-280).
		if failed, err := receiptFailed(ctx, tx, id); err != nil || failed {
			if err == nil {
				err = ErrNotDelivered
			}
			return err
		}
		before := metaFrom(current)
		updated, err := scanMessage(tx.QueryRow(ctx, `UPDATE inbox_messages
			SET acked_at = clock_timestamp(), acked_by_principal_id = $2::uuid
			WHERE id = $1::uuid AND acked_at IS NULL
			RETURNING `+messageCols, id, p.ID))
		if err != nil {
			return mapWrite(err)
		}
		after := metaFrom(updated)
		if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.acked", Before: before, After: after}); err != nil {
			return err
		}
		// A direct recipient acknowledgement is the receiver confirmation, for
		// plain and compat messages, bound or unbound (AEON-280).
		if err := confirmReceived(ctx, tx, p, id); err != nil {
			return err
		}
		if current.RecipientSessionID != nil {
			if err := MarkSessionSeen(ctx, tx, *current.RecipientSessionID, SeenAck); err != nil {
				return err
			}
		}
		out = updated
		return nil
	})
	return out, err
}

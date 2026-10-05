// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// MessageStatus is the sender's delivery progress for one message (AEON-280):
// sent (accepted), delivered (handed to the recipient by a pull, drain, stream
// or adapter claim), read (the recipient confirmed it) or not_delivered. It is
// content-free and, like the receipt, readable only by the sender.
type AttachedStatus struct {
	Protocol    string  `json:"protocol"`
	Outcome     string  `json:"outcome"`
	ContentMode string  `json:"content_mode"`
	Generation  *string `json:"message_generation,omitempty"`
}
type MessageStatus struct {
	Attached    *AttachedStatus `json:"attached,omitempty"`
	MessageID   string          `json:"message_id"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason,omitempty"`
	DeliveredAt *time.Time      `json:"delivered_at"`
	ReadAt      *time.Time      `json:"read_at"`
	DeliverBy   *time.Time      `json:"deliver_by"`
}

const maxStatusIDs = 100

func (m *module) handleMessageStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("ids"))
	if raw == "" {
		failure(w, badRequest("ids is required"))
		return
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxStatusIDs {
		failure(w, badRequest("at most 100 ids"))
		return
	}
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		id, valid := parseUUID(strings.TrimSpace(part))
		if !valid {
			failure(w, badRequest("invalid ids"))
			return
		}
		ids = append(ids, id)
	}
	items := []MessageStatus{}
	err := db.InTenant(tenant.WithPrincipal(r.Context(), p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		items = items[:0]
		rows, err := tx.Query(r.Context(), `SELECT m.id::text,r.state,r.failure_reason,m.fetched_at,r.handed_off_at,r.deliver_by,m.content_mode,m.attached_outcome,m.recipient_message_generation::text
 FROM inbox_messages m JOIN inbox_receipts r ON r.tenant_id=m.tenant_id AND r.message_id=m.id
 WHERE m.chat_thread_id IS NULL AND m.id=ANY($1::uuid[]) AND m.sender_principal_id=$2::uuid ORDER BY m.sent_event_id`, ids, p.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s MessageStatus
			var state, mode string
			var outcome, generation *string
			if err := rows.Scan(&s.MessageID, &state, &s.Reason, &s.DeliveredAt, &s.ReadAt, &s.DeliverBy, &mode, &outcome, &generation); err != nil {
				return err
			}
			switch {
			case state == "handed_off":
				s.Status = "read"
			case state == "failed":
				s.Status = "not_delivered"
			case s.DeliveredAt != nil:
				s.Status = "delivered"
			default:
				s.Status = "sent"
			}
			if state != "failed" {
				s.Reason = ""
			}
			if mode != "durable" {
				s.Attached = &AttachedStatus{Protocol: "attached_messages_v1", ContentMode: mode, Generation: generation}
				if outcome != nil {
					s.Attached.Outcome = *outcome
				}
				s.DeliveredAt = nil
				s.ReadAt = nil
				s.Status = "sent"
				if state == "failed" {
					s.Status = "not_delivered"
				}
			}
			items = append(items, s)
		}
		return rows.Err()
	})
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []MessageStatus `json:"items"`
	}{items})
}

func (m *module) handleGetDeliverySettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var out DeliverySettings
	err := db.InTenant(tenant.WithPrincipal(r.Context(), p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = loadDeliverySettings(r.Context(), tx)
		return err
	})
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// New deadlines apply to messages sent from now on; a queued message keeps the
// deadline it was accepted with.
func (m *module) handlePutDeliverySettings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in DeliverySettings
	if !decodeJSON(w, r, 1024, &in) {
		return
	}
	if in.SessionDeadlineSeconds < 60 || in.SessionDeadlineSeconds > 86400 ||
		in.UnboundDeadlineSeconds < 60 || in.UnboundDeadlineSeconds > 604800 ||
		in.MaxAttempts < 1 || in.MaxAttempts > 50 {
		failure(w, badRequest("session deadline 60–86400 s, unbound deadline 60–604800 s, attempts 1–50"))
		return
	}
	err := db.InTenant(tenant.WithPrincipal(r.Context(), p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		// Access mutations use this advisory lock followed by the tenant row.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, p.TenantID); err != nil {
			return err
		}
		var tenantID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&tenantID); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
			return errForbidden
		}
		before, err := loadDeliverySettings(ctx, tx)
		if err != nil {
			return err
		}
		if before == in {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO inbox_delivery_settings(tenant_id,session_deadline_seconds,unbound_deadline_seconds,max_attempts) VALUES($1::uuid,$2,$3,$4)
 ON CONFLICT (tenant_id) DO UPDATE SET session_deadline_seconds=EXCLUDED.session_deadline_seconds,unbound_deadline_seconds=EXCLUDED.unbound_deadline_seconds,max_attempts=EXCLUDED.max_attempts,updated_at=clock_timestamp()`,
			p.TenantID, in.SessionDeadlineSeconds, in.UnboundDeadlineSeconds, in.MaxAttempts)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "inbox.delivery_settings_changed", Before: before, After: in})
		return err
	})
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

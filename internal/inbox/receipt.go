// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Receipt is the sender's proof of receiver confirmation. State moves
// queued → handed_off only after a direct recipient acknowledgement or managed
// adapter confirmation, or queued → failed on a terminal failure. Neither
// terminal state moves again, and handed_off_at is written once.
type Receipt struct {
	Attached             *AttachedStatus `json:"attached,omitempty"`
	MessageID            string          `json:"message_id"`
	IdempotencyKey       string          `json:"idempotency_key"`
	Tenant               string          `json:"tenant"`
	SenderPrincipalID    string          `json:"sender_principal_id"`
	RecipientPrincipalID string          `json:"recipient_principal_id"`
	TargetID             *string         `json:"target_id"`
	TargetVersion        *int            `json:"target_version"`
	Adapter              string          `json:"adapter"`
	Address              string          `json:"address"`
	EffectiveLevel       string          `json:"effective_level"`
	State                string          `json:"state"`
	HandedOffAt          *string         `json:"handed_off_at"`
	FailureReason        string          `json:"failure_reason"`
}

type receiptTarget struct {
	ID      *string
	Version *int
	Adapter string
	Address string
}

func (m *module) handleReceipt(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := parseUUID(r.PathValue("messageId"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	out, err := m.receipt(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *module) receipt(ctx context.Context, p tenant.Principal, id string) (Receipt, error) {
	var out Receipt
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var at *time.Time
		var mode string
		var outcome, generation *string
		err := tx.QueryRow(ctx, `
			SELECT m.id::text, m.idempotency_key, t.slug,
			       m.sender_principal_id::text, m.recipient_principal_id::text,
			       r.target_id::text, r.target_version, COALESCE(r.adapter, ''),
			       COALESCE(r.address, ''), COALESCE(r.effective_level, ''),
			       r.state, r.handed_off_at, r.failure_reason,m.content_mode,m.attached_outcome,m.recipient_message_generation::text
			FROM inbox_messages m
			JOIN tenants t ON t.id = m.tenant_id
			JOIN inbox_receipts r ON r.tenant_id = m.tenant_id AND r.message_id = m.id
			WHERE m.id = $1::uuid AND m.sender_principal_id = $2::uuid`, id, p.ID).Scan(
			&out.MessageID, &out.IdempotencyKey, &out.Tenant,
			&out.SenderPrincipalID, &out.RecipientPrincipalID,
			&out.TargetID, &out.TargetVersion, &out.Adapter, &out.Address, &out.EffectiveLevel,
			&out.State, &at, &out.FailureReason, &mode, &outcome, &generation)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if at != nil {
			s := at.UTC().Format(time.RFC3339)
			out.HandedOffAt = &s
		}
		if mode != "durable" {
			out.Attached = &AttachedStatus{Protocol: "attached_messages_v1", ContentMode: mode, Generation: generation}
			if outcome != nil {
				out.Attached.Outcome = *outcome
			}
			out.HandedOffAt = nil
			if out.State == "handed_off" {
				out.State = "queued"
			}
		}
		return nil
	})
	return out, err
}

func recordAcceptanceReceipt(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID string) error {
	var targetID *string
	var state, reason string
	err := tx.QueryRow(ctx, `SELECT target_id::text, state, reason FROM inbox_message_deliveries WHERE message_id = $1::uuid`, messageID).Scan(&targetID, &state, &reason)
	if err != nil {
		return err
	}
	var target receiptTarget
	if targetID != nil {
		target, err = loadReceiptTarget(ctx, tx, *targetID)
		if err != nil {
			return err
		}
	}
	// A blocked message (no target) stays readable by a listening recipient, so
	// it is queued like any other and fails at its deadline (AEON-280).
	if state == "dead" {
		if reason == "" {
			reason = "unavailable"
		}
		return insertReceipt(ctx, tx, p, messageID, "failed", target, "", reason)
	}
	return insertReceipt(ctx, tx, p, messageID, "queued", target, "", "")
}

func loadReceiptTarget(ctx context.Context, tx pgx.Tx, id string) (receiptTarget, error) {
	var t receiptTarget
	var version int
	var targetID string
	err := tx.QueryRow(ctx, `SELECT id::text, version, adapter, address FROM inbox_message_targets WHERE id = $1::uuid`, id).Scan(&targetID, &version, &t.Adapter, &t.Address)
	if err != nil {
		return receiptTarget{}, err
	}
	t.ID = &targetID
	t.Version = &version
	return t, nil
}

func insertReceipt(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID, state string, target receiptTarget, level, reason string) error {
	targetID, version := receiptArgs(target)
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_receipts (
			tenant_id, message_id, state, target_id, target_version, adapter, address,
			effective_level, handed_off_at, failure_reason, deliver_by)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, $7, $8,
			CASE WHEN $3 = 'handed_off' THEN clock_timestamp() ELSE NULL END, $9,
			CASE WHEN $3 = 'queued' THEN `+receiptDeadlineSQL+` END)`,
		p.TenantID, messageID, state, targetID, version, target.Adapter, target.Address, level, reason); err != nil {
		return err
	}
	return appendReceiptEvent(ctx, tx, p, messageID, state, target, level, reason)
}

// advanceReceipt moves a queued receipt forward. A missing row is inserted.
// A receipt already handed_off or failed is left untouched.
func advanceReceipt(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID, state, level, reason string, target receiptTarget) error {
	var attached bool
	if err := tx.QueryRow(ctx, `SELECT content_mode<>'durable' FROM inbox_messages WHERE id=$1::uuid`, messageID).Scan(&attached); err != nil {
		return err
	}
	if attached {
		return errForbidden
	}
	targetID, version := receiptArgs(target)
	tag, err := tx.Exec(ctx, `
		UPDATE inbox_receipts SET
			state = $2,
			failure_reason = CASE WHEN $2 = 'failed' THEN $3 ELSE '' END,
			effective_level = CASE WHEN $2 = 'handed_off' THEN $4 ELSE effective_level END,
			handed_off_at = CASE WHEN $2 = 'handed_off' THEN clock_timestamp() ELSE NULL END,
			target_id = CASE WHEN $5::uuid IS NULL THEN target_id ELSE $5::uuid END,
			target_version = CASE WHEN $6::integer IS NULL THEN target_version ELSE $6::integer END,
			adapter = CASE WHEN $7 = '' THEN adapter ELSE $7 END,
			address = CASE WHEN $8 = '' THEN address ELSE $8 END
		WHERE message_id = $1::uuid AND state = 'queued'`,
		messageID, state, reason, level, targetID, version, target.Adapter, target.Address)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return appendReceiptEvent(ctx, tx, p, messageID, state, target, level, reason)
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT state FROM inbox_receipts WHERE message_id = $1::uuid`, messageID).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return insertReceipt(ctx, tx, p, messageID, state, target, level, reason)
	}
	return err
}

func receiptArgs(target receiptTarget) (any, any) {
	var id, version any
	if target.ID != nil {
		id = *target.ID
	}
	if target.Version != nil {
		version = *target.Version
	}
	return id, version
}

func retargetReceipt(ctx context.Context, tx pgx.Tx, deliveryID, targetID string) error {
	target, err := loadReceiptTarget(ctx, tx, targetID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE inbox_receipts r
		SET target_id = $2::uuid, target_version = $3, adapter = $4, address = $5
		FROM inbox_message_deliveries d
		WHERE d.id = $1::uuid AND r.tenant_id = d.tenant_id AND r.message_id = d.message_id
		  AND r.state = 'queued'`, deliveryID, *target.ID, *target.Version, target.Adapter, target.Address)
	return err
}

func confirmedReceiptTarget(ctx context.Context, tx pgx.Tx, messageID string) (receiptTarget, string, error) {
	var t receiptTarget
	var id string
	var version int
	var level string
	err := tx.QueryRow(ctx, `
		SELECT t.id::text, t.version, t.adapter, t.address, COALESCE(d.effective_level, '')
		FROM inbox_message_deliveries d
		JOIN inbox_message_targets t ON t.tenant_id = d.tenant_id AND t.id = COALESCE(d.effective_target_id, d.target_id)
		WHERE d.message_id = $1::uuid`, messageID).Scan(&id, &version, &t.Adapter, &t.Address, &level)
	if err != nil {
		return receiptTarget{}, "", err
	}
	t.ID = &id
	t.Version = &version
	return t, level, nil
}

func appendReceiptEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID, state string, target receiptTarget, level, reason string) error {
	kind := "inbox.receipt_queued"
	switch state {
	case "handed_off":
		kind = "inbox.receipt_handed_off"
	case "failed":
		kind = "inbox.receipt_failed"
	}
	after := map[string]any{"message_id": messageID, "state": state}
	if target.ID != nil {
		after["target_id"] = *target.ID
	}
	if target.Version != nil {
		after["target_version"] = *target.Version
	}
	if target.Adapter != "" {
		after["adapter"] = target.Adapter
	}
	if target.Address != "" {
		after["address"] = target.Address
	}
	if level != "" {
		after["effective_level"] = level
	}
	if reason != "" {
		after["failure_reason"] = reason
	}
	_, err := events.Append(ctx, tx, p, events.Change{Type: kind, After: after})
	return err
}

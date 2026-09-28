// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// DeliveryWork is returned only to the authenticated recipient. TargetRef is
// never part of the redacted ledger, event stream, or a person inspection page.
type DeliveryWork struct {
	ProjectKey     string         `json:"-"`
	ID             string         `json:"delivery_id"`
	LeaseToken     string         `json:"lease_token,omitempty"`
	TenantID       string         `json:"tenant_id,omitempty"`
	PrincipalID    string         `json:"principal_id,omitempty"`
	Cursor         int64          `json:"cursor"`
	State          string         `json:"state"`
	Adapter        string         `json:"adapter,omitempty"`
	TargetKind     string         `json:"target_kind,omitempty"`
	TargetRef      string         `json:"target_ref,omitempty"`
	TargetSecret   string         `json:"-"`
	MaximumLevel   string         `json:"maximum_level,omitempty"`
	FallbackReason string         `json:"fallback_reason,omitempty"`
	Message        *CompatMessage `json:"message,omitempty"`
}

type claimInput struct {
	To      string `json:"to"`
	Adapter string `json:"adapter"`
}

func (m *messaging) ackCompatMessage(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.messagingPrincipal(w, r, false)
	if !ok {
		return
	}
	id, valid := parseUUID(r.PathValue("messageId"))
	if !valid {
		messagingFailure(w, errNotFound)
		return
	}
	var inboxID string
	err := db.InTenant(r.Context(), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := messagingProject(r.Context(), tx, project); err != nil {
			return err
		}
		return tx.QueryRow(r.Context(), `SELECT inbox_message_id::text FROM inbox_compat_messages
		 WHERE id=$1::uuid AND project_id=$2::uuid AND recipient_principal_id=$3::uuid AND NOT is_action_request`, id, project, p.ID).Scan(&inboxID)
	})
	if err != nil {
		messagingFailure(w, err)
		return
	}
	if _, err := m.base.ack(r.Context(), p, inboxID); err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "acked": true})
}

func (m *messaging) claimDelivery(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.messagingPrincipal(w, r, false)
	if !ok {
		return
	}
	var in claimInput
	if !decodeJSON(w, r, 1024, &in) {
		return
	}
	if !messageAddressRE.MatchString(in.To) || !localDeliveryAdapter(in.Adapter) {
		messagingFailure(w, badRequest("invalid delivery address or adapter"))
		return
	}
	work, err := m.claim(r.Context(), p, project, in)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, work)
}

func localDeliveryAdapter(name string) bool {
	switch name {
	case "codex", "agentd_codex", "agentd_claude", "agentd_pi", "agentd_cursor", "claude_resume", "claude_channel":
		return true
	}
	return false
}

func (m *messaging) claim(ctx context.Context, p tenant.Principal, project string, in claimInput) (*DeliveryWork, error) {
	var work *DeliveryWork
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := messagingProject(ctx, tx, project); err != nil {
			return err
		}
		owner, err := resolveAddress(ctx, tx, project, in.To)
		if err != nil {
			return err
		}
		if owner != p.ID {
			return errForbidden
		}
		// One address is processed in event order. A cursor is advanced only by a
		// completed, acknowledged handoff; a failed adapter cannot skip work.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,66))`, p.TenantID+project+in.To); err != nil {
			return err
		}
		var id, state string
		var targetID, fallbackID *string
		var cursor int64
		var requested, priorFallback string
		var leaseUntil *time.Time
		err = tx.QueryRow(ctx, `SELECT d.id::text,COALESCE(d.effective_target_id,d.target_id)::text,d.fallback_target_id::text,d.state,c.sent_event_id,d.lease_until,c.delivery_level,d.fallback_reason
		 FROM inbox_message_deliveries d
		 JOIN inbox_compat_messages c ON c.tenant_id=d.tenant_id AND c.id=d.message_id
		 JOIN inbox_messages i ON i.tenant_id=c.tenant_id AND i.id=c.inbox_message_id
		 WHERE c.project_id=$1::uuid AND c.recipient_principal_id=$2::uuid AND c.recipient_address=$3
		 AND i.acked_at IS NULL AND NOT c.is_action_request AND c.recipient_session_id IS NULL
		 AND c.sent_event_id > COALESCE((SELECT last_event_id FROM inbox_message_cursors
		 WHERE project_id=$1::uuid AND principal_id=$2::uuid AND address=$3 AND adapter=$4),0)
		 ORDER BY c.sent_event_id LIMIT 1 FOR UPDATE OF d`, project, p.ID, in.To, in.Adapter).Scan(&id, &targetID, &fallbackID, &state, &cursor, &leaseUntil, &requested, &priorFallback)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		work = &DeliveryWork{ID: id, Cursor: cursor, State: state}
		if state != "pending" {
			return nil
		}
		if leaseUntil != nil && leaseUntil.After(time.Now()) {
			work.State = "leased"
			return nil
		}
		if targetID == nil {
			work.State = "blocked"
			return nil
		}
		var adapter, kind, maximum string
		var sealed []byte
		if err := tx.QueryRow(ctx, `SELECT adapter,target_kind,maximum_level,sealed_target FROM inbox_message_targets WHERE id=$1::uuid AND principal_id=$2::uuid`, *targetID, p.ID).Scan(&adapter, &kind, &maximum, &sealed); err != nil {
			return err
		}
		if requested == "simple" && priorFallback == "" && fallbackID != nil && (adapter == "agentd_codex" || adapter == "agentd_claude" || adapter == "agentd_pi" || adapter == "agentd_cursor" || adapter == "claude_channel") {
			if _, err := tx.Exec(ctx, `UPDATE inbox_message_deliveries SET effective_target_id=$2::uuid,fallback_reason='not_steerable' WHERE id=$1::uuid`, id, *fallbackID); err != nil {
				return err
			}
			if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.delivery_rerouted", After: map[string]any{"delivery_id": id, "target_id": *fallbackID, "reason": "not_steerable"}}); err != nil {
				return err
			}
			targetID, priorFallback = fallbackID, "not_steerable"
			if err := retargetReceipt(ctx, tx, id, *targetID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT adapter,target_kind,maximum_level,sealed_target FROM inbox_message_targets WHERE id=$1::uuid AND principal_id=$2::uuid`, *targetID, p.ID).Scan(&adapter, &kind, &maximum, &sealed); err != nil {
				return err
			}
		}
		work.Adapter = adapter
		if adapter != in.Adapter {
			work.State = "foreign_worker"
			return nil
		}
		if len(sealed) < m.aead.NonceSize() {
			return errors.New("target decryption failed")
		}
		plaintext, err := m.aead.Open(nil, sealed[:m.aead.NonceSize()], sealed[m.aead.NonceSize():], []byte(p.TenantID+"/"+project+"/"+*targetID))
		if err != nil {
			return errors.New("target decryption failed")
		}
		var private struct {
			Ref    string `json:"ref"`
			Secret string `json:"secret"`
		}
		if json.Unmarshal(plaintext, &private) != nil || private.Ref == "" {
			return errors.New("target decryption failed")
		}
		var token string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&token); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE inbox_message_deliveries SET lease_token=$2::uuid,lease_until=clock_timestamp()+interval '2 minutes',attempts=attempts+1 WHERE id=$1::uuid`, id, token); err != nil {
			return err
		}
		if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.delivery_claimed", After: map[string]any{"delivery_id": id, "target_id": *targetID, "adapter": adapter, "cursor": cursor}}); err != nil {
			return err
		}
		var message CompatMessage
		message, err = scanCompatMessage(tx.QueryRow(ctx, `SELECT `+compatMessageCols+` FROM inbox_compat_messages c `+compatObligationJoin+` JOIN inbox_message_deliveries d ON d.tenant_id=c.tenant_id AND d.message_id=c.id WHERE d.id=$1::uuid`, id))
		if err != nil {
			return err
		}
		work.LeaseToken, work.TenantID, work.PrincipalID, work.TargetKind, work.TargetRef, work.TargetSecret, work.MaximumLevel, work.FallbackReason, work.Message = token, p.TenantID, p.ID, kind, private.Ref, private.Secret, maximum, priorFallback, &message
		return nil
	})
	return work, err
}

type completeInput struct {
	ID             string `json:"delivery_id"`
	LeaseToken     string `json:"lease_token"`
	EffectiveLevel string `json:"effective_level"`
	FallbackReason string `json:"fallback_reason"`
}

func (m *messaging) completeDelivery(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.messagingPrincipal(w, r, false)
	if !ok {
		return
	}
	var in completeInput
	if !decodeJSON(w, r, 1024, &in) {
		return
	}
	if _, ok := parseUUID(in.ID); !ok {
		messagingFailure(w, badRequest("invalid delivery id"))
		return
	}
	if _, ok := parseUUID(in.LeaseToken); !ok {
		messagingFailure(w, badRequest("invalid lease token"))
		return
	}
	if in.EffectiveLevel != "simple" && in.EffectiveLevel != "steer" {
		messagingFailure(w, badRequest("invalid effective level"))
		return
	}
	switch in.FallbackReason {
	case "", "unsupported", "policy_capped", "idle", "not_steerable", "transport_error":
	default:
		messagingFailure(w, badRequest("invalid fallback reason"))
		return
	}
	result, err := m.complete(r.Context(), p, project, in)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (m *messaging) complete(ctx context.Context, p tenant.Principal, project string, in completeInput) (MessageDelivery, error) {
	var out MessageDelivery
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := messagingProject(ctx, tx, project); err != nil {
			return err
		}
		var messageID, address, adapter, state, effective, fallback, requested, maximum string
		var token *string
		var cursor int64
		var leaseUntil *time.Time
		err := tx.QueryRow(ctx, `SELECT d.message_id::text,c.recipient_address,t.adapter,d.state,d.lease_token::text,COALESCE(d.effective_level,''),d.fallback_reason,c.sent_event_id,c.delivery_level,t.maximum_level,d.lease_until
		 FROM inbox_message_deliveries d JOIN inbox_compat_messages c ON c.tenant_id=d.tenant_id AND c.id=d.message_id
		 JOIN inbox_message_targets t ON t.tenant_id=d.tenant_id AND t.id=COALESCE(d.effective_target_id,d.target_id)
		 WHERE d.id=$1::uuid AND c.project_id=$2::uuid AND c.recipient_principal_id=$3::uuid FOR UPDATE OF d`, in.ID, project, p.ID).Scan(&messageID, &address, &adapter, &state, &token, &effective, &fallback, &cursor, &requested, &maximum, &leaseUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if token == nil || *token != in.LeaseToken {
			return &httpError{409, "lease_conflict", "delivery lease changed"}
		}
		if state == "delivered" {
			if effective != in.EffectiveLevel || fallback != in.FallbackReason {
				return &httpError{409, "delivery_conflict", "delivery result changed"}
			}
		} else {
			if state != "pending" {
				return &httpError{409, "delivery_conflict", "delivery is not pending"}
			}
			if leaseUntil == nil || !leaseUntil.After(time.Now()) {
				return &httpError{409, "lease_conflict", "delivery lease expired"}
			}
			if in.EffectiveLevel == "steer" && (requested != "steer" || maximum != "steer" || adapter == "claude_resume" || adapter == "claude_channel" || adapter == "agentd_cursor") {
				return &httpError{409, "delivery_conflict", "invalid effective delivery level"}
			}
			if in.EffectiveLevel == "simple" && requested == "steer" && in.FallbackReason == "" {
				return &httpError{409, "delivery_conflict", "missing fallback reason"}
			}
			if fallback != "" && in.FallbackReason != fallback {
				return &httpError{409, "delivery_conflict", "reroute reason changed"}
			}
			if (in.EffectiveLevel == "steer" || (requested == "simple" && fallback == "")) && in.FallbackReason != "" {
				return &httpError{409, "delivery_conflict", "invalid fallback reason"}
			}
			if _, err := tx.Exec(ctx, `UPDATE inbox_message_deliveries SET state='delivered',reason='',effective_level=$2,fallback_reason=$3,lease_until=NULL WHERE id=$1::uuid`, in.ID, in.EffectiveLevel, in.FallbackReason); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE inbox_messages SET acked_at=clock_timestamp(),acked_by_principal_id=$2::uuid WHERE id=$1::uuid AND acked_at IS NULL`, messageID, p.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO inbox_message_cursors(tenant_id,project_id,principal_id,address,adapter,last_event_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6)
			 ON CONFLICT (tenant_id,project_id,principal_id,address,adapter) DO UPDATE SET last_event_id=GREATEST(inbox_message_cursors.last_event_id,EXCLUDED.last_event_id),updated_at=clock_timestamp()`, p.TenantID, project, p.ID, address, adapter, cursor); err != nil {
				return err
			}
			if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.delivery_completed", After: map[string]any{"delivery_id": in.ID, "message_id": messageID, "cursor": cursor, "effective_level": in.EffectiveLevel, "fallback_reason": in.FallbackReason}}); err != nil {
				return err
			}
			if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.acked", After: map[string]any{"id": messageID, "sent_event_id": cursor}}); err != nil {
				return err
			}
		}
		confirmed, _, err := confirmedReceiptTarget(ctx, tx, messageID)
		if err != nil {
			return err
		}
		if err := advanceReceipt(ctx, tx, p, messageID, "handed_off", in.EffectiveLevel, "", confirmed); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text,message_id::text,target_id::text,fallback_target_id::text,state,reason,attempts,COALESCE(effective_level,''),fallback_reason FROM inbox_message_deliveries WHERE id=$1::uuid`, in.ID).Scan(&out.ID, &out.MessageID, &out.TargetID, &out.FallbackTargetID, &out.State, &out.Reason, &out.Attempts, &out.EffectiveLevel, &out.FallbackReason)
	})
	return out, err
}

type unavailableInput struct {
	ID             string `json:"delivery_id"`
	LeaseToken     string `json:"lease_token"`
	FallbackReason string `json:"fallback_reason"`
}

func (m *messaging) unavailableDelivery(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.messagingPrincipal(w, r, false)
	if !ok {
		return
	}
	var in unavailableInput
	if !decodeJSON(w, r, 1024, &in) {
		return
	}
	if _, ok := parseUUID(in.ID); !ok {
		messagingFailure(w, badRequest("invalid delivery id"))
		return
	}
	if _, ok := parseUUID(in.LeaseToken); !ok {
		messagingFailure(w, badRequest("invalid lease token"))
		return
	}
	switch in.FallbackReason {
	case "idle", "not_steerable", "transport_error", "unsupported":
	default:
		messagingFailure(w, badRequest("invalid fallback reason"))
		return
	}
	err := db.InTenant(r.Context(), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := messagingProject(r.Context(), tx, project); err != nil {
			return err
		}
		var target, fallback, token *string
		var state string
		var leaseUntil *time.Time
		err := tx.QueryRow(r.Context(), `SELECT COALESCE(d.effective_target_id,d.target_id)::text,d.fallback_target_id::text,d.lease_token::text,d.state,d.lease_until
		 FROM inbox_message_deliveries d JOIN inbox_compat_messages c ON c.tenant_id=d.tenant_id AND c.id=d.message_id
		 WHERE d.id=$1::uuid AND c.project_id=$2::uuid AND c.recipient_principal_id=$3::uuid FOR UPDATE OF d`, in.ID, project, p.ID).Scan(&target, &fallback, &token, &state, &leaseUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if state != "pending" || token == nil || *token != in.LeaseToken || leaseUntil == nil || !leaseUntil.After(time.Now()) || fallback == nil || (target != nil && *target == *fallback) {
			return &httpError{409, "delivery_conflict", "delivery cannot reroute"}
		}
		if _, err := tx.Exec(r.Context(), `UPDATE inbox_message_deliveries SET effective_target_id=$2::uuid,lease_token=NULL,lease_until=NULL,fallback_reason=$3 WHERE id=$1::uuid`, in.ID, *fallback, in.FallbackReason); err != nil {
			return err
		}
		if _, err = events.Append(r.Context(), tx, p, events.Change{Type: "inbox.delivery_rerouted", After: map[string]any{"delivery_id": in.ID, "target_id": *fallback, "reason": in.FallbackReason}}); err != nil {
			return err
		}
		return retargetReceipt(r.Context(), tx, in.ID, *fallback)
	})
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivery_id": in.ID, "rerouted": true})
}

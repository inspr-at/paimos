// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Send struct {
	Recipient, Project, Generation, Body string
	Session                              *string
	Unsupported                          bool
}
type Acceptance struct {
	Mode     string
	Binding  Binding
	GrantID  *string
	Deadline time.Time
	Bytes    int
}

// Authorize is the one sender predicate used by raw, compatibility, replies,
// MCP and CLI (the latter all enter those two HTTP send routes). The caller
// holds the pairing fence before calling this and keeps it through commit.
func (s *Service) Authorize(ctx context.Context, tx pgx.Tx, p tenant.Principal, request string, in Send) (Acceptance, error) {
	var out Acceptance
	if !s.Enabled() {
		return out, Fail(409, "feature_disabled")
	}
	a, e := LoadAttachment(ctx, tx, request)
	if e != nil {
		return out, e
	}
	if in.Session == nil || *in.Session != a.Session || in.Recipient != a.Principal || (in.Project != "" && in.Project != a.Project) {
		return out, Fail(404, "attached_recipient_mismatch")
	}
	if in.Unsupported {
		return out, Fail(400, "unsupported_attached_mode")
	}
	if _, e = Frame(p.ID, p.Name, in.Body); e != nil {
		return out, e
	}
	if authz.RequireTx(ctx, tx, p, "inbox.send", authz.Scope{ProjectID: a.Project}) != nil {
		return out, Fail(403, "sender_authority_lost")
	}
	out.Mode = Volatile
	out.Bytes = len(in.Body)
	out.Binding = Binding{TenantID: p.TenantID, ProjectID: a.Project, SessionID: a.Session, ComputerID: a.Computer, OwnerID: a.Owner, AttachRequestID: a.ID, ServiceEpoch: s.epoch}
	if p.Kind == tenant.Agent {
		// Agent labels, scopes or sender-session fields can never promote a notice.
		out.Mode = Notification
	} else {
		if p.Kind != tenant.Person || !interactive(ctx) || p.ID != a.Owner {
			return out, Fail(403, "interactive_owner_required")
		}
		g, e := loadGrant(ctx, tx, request)
		if e != nil {
			return out, Fail(409, "messages_off")
		}
		if e = s.validate(ctx, tx, a, g); e != nil {
			return out, e
		}
		if in.Generation == "" || in.Generation != g.Binding.Generation {
			return out, Fail(409, "stale_message_generation")
		}
		if g.State != "active" || g.ObservedAt == nil {
			return out, Fail(409, "waiting_for_hook")
		}
		out.Binding = g.Binding
		out.GrantID = &g.ID
	}
	// Exact session row before any message/delivery/receipt lock.
	if _, e = tx.Exec(ctx, `SELECT 1 FROM harness_sessions WHERE id=$1::uuid FOR SHARE`, a.Session); e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()+interval '5 minutes'`).Scan(&out.Deadline); e != nil {
		return out, e
	}
	return out, nil
}

// Quota changes roll back with message acceptance. The pairing fence makes
// this a distributed token bucket, not a per-web-process debounce.
func Charge(ctx context.Context, tx pgx.Tx, a Acceptance) error {
	// Notifications have their own rate budget and never charge owner notes.
	if a.Mode == Notification {
		return chargeNotification(ctx, tx, a)
	}
	var ok bool
	e := tx.QueryRow(ctx, `UPDATE harness_attach_requests SET
 message_tokens=least(3,message_tokens+greatest(0,extract(epoch from clock_timestamp()-message_tokens_at))/10)-1,
 message_tokens_at=clock_timestamp()
 WHERE id=$1::uuid AND least(3,message_tokens+greatest(0,extract(epoch from clock_timestamp()-message_tokens_at))/10)>=1 RETURNING true`, a.Binding.AttachRequestID).Scan(&ok)
	if e == pgx.ErrNoRows {
		return Fail(429, "attached_session_rate")
	}
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, `UPDATE agent_pairing_computers SET
 message_window_count=CASE WHEN message_window_at<=clock_timestamp()-interval '1 minute' THEN 1 ELSE message_window_count+1 END,
 message_window_at=CASE WHEN message_window_at<=clock_timestamp()-interval '1 minute' THEN clock_timestamp() ELSE message_window_at END
 WHERE id=$1::uuid AND (message_window_at<=clock_timestamp()-interval '1 minute' OR message_window_count<30) RETURNING true`, a.Binding.ComputerID).Scan(&ok)
	if e == pgx.ErrNoRows {
		return Fail(429, "attached_owner_rate")
	}
	if e != nil {
		return e
	}
	var n, bytes int
	e = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(payload_bytes),0) FROM inbox_messages WHERE recipient_session_id=$1::uuid AND content_mode='attached_volatile' AND attached_outcome IN ('queued','offered')`, a.Binding.SessionID).Scan(&n, &bytes)
	if e != nil {
		return e
	}
	if n >= 5 || bytes+a.Bytes > 20<<10 {
		return Fail(429, "attached_queue_full")
	}
	return nil
}

func chargeNotification(ctx context.Context, tx pgx.Tx, a Acceptance) error {
	var ok bool
	e := tx.QueryRow(ctx, `UPDATE harness_attach_requests SET
 message_notice_tokens=least(3,message_notice_tokens+greatest(0,extract(epoch from clock_timestamp()-message_notice_tokens_at))/10)-1,
 message_notice_tokens_at=clock_timestamp()
 WHERE id=$1::uuid AND least(3,message_notice_tokens+greatest(0,extract(epoch from clock_timestamp()-message_notice_tokens_at))/10)>=1 RETURNING true`, a.Binding.AttachRequestID).Scan(&ok)
	if e == pgx.ErrNoRows {
		return Fail(429, "attached_notification_rate")
	}
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, `UPDATE agent_pairing_computers SET
 message_notice_window_count=CASE WHEN message_notice_window_at<=clock_timestamp()-interval '1 minute' THEN 1 ELSE message_notice_window_count+1 END,
 message_notice_window_at=CASE WHEN message_notice_window_at<=clock_timestamp()-interval '1 minute' THEN clock_timestamp() ELSE message_notice_window_at END
 WHERE id=$1::uuid AND (message_notice_window_at<=clock_timestamp()-interval '1 minute' OR message_notice_window_count<30) RETURNING true`, a.Binding.ComputerID).Scan(&ok)
	if e == pgx.ErrNoRows {
		return Fail(429, "attached_notification_rate")
	}
	if e != nil {
		return e
	}
	// Coalesce terminal metadata without putting body/label into the hook.
	_, e = tx.Exec(ctx, `UPDATE harness_attach_requests SET
 message_notice_count=CASE WHEN message_notice_at IS NULL OR message_notice_at<=clock_timestamp()-interval '1 minute' THEN 1 ELSE least(255,message_notice_count+1) END,
 message_notice_at=CASE WHEN message_notice_at IS NULL OR message_notice_at<=clock_timestamp()-interval '1 minute' THEN clock_timestamp() ELSE message_notice_at END
 WHERE id=$1::uuid`, a.Binding.AttachRequestID)
	return e
}

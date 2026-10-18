// SPDX-License-Identifier: AGPL-3.0-only
package attachedmsg

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Terminalize never invokes the ordinary retry/failure-notice path. Body and
// sender-controlled labels cannot enter notices or project coordinator copies.
func Terminalize(ctx context.Context, tx pgx.Tx, id, reason string) error {
	if _, e := tx.Exec(ctx, `SELECT 1 FROM harness_sessions WHERE id=(SELECT recipient_session_id FROM inbox_messages WHERE id=$1::uuid) FOR SHARE`, id); e != nil {
		return e
	}
	var outcome string
	e := tx.QueryRow(ctx, `UPDATE inbox_messages SET attached_outcome=CASE WHEN attached_outcome IN ('offered','shown') THEN 'uncertain' WHEN $2='messages_revoked' THEN 'revoked' WHEN $2='deadline' THEN 'expired' WHEN $2='cancelled' THEN 'cancelled' ELSE 'not_delivered' END,expires_at=clock_timestamp()
 WHERE id=$1::uuid AND content_mode<>'durable' AND attached_outcome IN ('queued','offered','shown') RETURNING attached_outcome`, id, reason).Scan(&outcome)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE inbox_compat_messages SET attached_outcome=$2 WHERE id=$1::uuid`, id, outcome); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE harness_deliveries SET terminal_outcome=$2,released_at=clock_timestamp()
 WHERE message_id=$1::uuid AND mode='attached_hook' AND terminal_outcome IS NULL`, id, outcome); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE inbox_message_deliveries SET state='dead',reason='unavailable',lease_token=NULL,lease_until=NULL WHERE message_id=$1::uuid`, id); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE inbox_receipts SET state='failed',failure_reason=$2,handed_off_at=NULL WHERE message_id=$1::uuid`, id, reason)
	return e
}
func Revoke(ctx context.Context, tx pgx.Tx, p tenant.Principal, request string) ([]string, error) {
	if !interactive(ctx) || p.Kind != tenant.Person {
		return nil, Fail(403, "interactive_owner_required")
	}
	if authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) != nil {
		return nil, Fail(403, "owner_authority_lost")
	}
	var owner, current string
	e := tx.QueryRow(ctx, `SELECT a.owner_id::text,q.approved_by::text FROM harness_attach_requests a JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE a.id=$1::uuid FOR UPDATE OF a`, request).Scan(&owner, &current)
	if e != nil || owner != p.ID || current != p.ID {
		return nil, Fail(403, "computer_owner_required")
	}
	rows, e := tx.Query(ctx, `UPDATE attached_message_grants SET state='revoked',local_auth_nonce=NULL WHERE attach_request_id=$1::uuid AND state<>'revoked' RETURNING id::text`, request)
	if e != nil {
		return nil, e
	}
	var grants []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		grants = append(grants, id)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	// Current grants accept at most five pending notes. Keep settlement bounded
	// for upgraded databases too; revoked authority and the RAM purge cover all
	// notes immediately, while Sweep can finish any remaining metadata.
	rows, e = tx.Query(ctx, `SELECT id::text FROM inbox_messages WHERE message_grant_id=ANY($1::uuid[]) AND attached_outcome IN ('queued','offered','shown') ORDER BY id LIMIT 1024`, grants)
	if e != nil {
		return nil, e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for _, id := range ids {
		if e = Terminalize(ctx, tx, id, "messages_revoked"); e != nil {
			return nil, e
		}
	}
	return grants, nil
}

// Sweep runs even with the switch off, including after restart. Memory loss
// cannot recreate payloads; committed metadata remains truthful and body-free.
func (s *Service) Sweep(ctx context.Context, pool *pgxpool.Pool, tid string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var purge []string
	e := db.InTenant(db.AllProjects(ctx, "attached message lifecycle"), pool, tid, func(tx pgx.Tx) error {
		if e := Lock(ctx, tx); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT m.id::text,m.payload_epoch::text,m.message_deadline<=clock_timestamp(),coalesce(a.state='active' AND a.lease_until>clock_timestamp() AND c.state='connected' AND q.approved_by=a.owner_id,false),
  m.message_grant_id::text,m.attached_outcome,a.id::text,m.sender_principal_id::text,coalesce((SELECT d.offer_deadline<=clock_timestamp() OR d.hook_epoch IS DISTINCT FROM (SELECT g.hook_epoch FROM attached_message_grants g WHERE g.tenant_id=d.tenant_id AND g.id=d.message_grant_id) FROM harness_deliveries d WHERE d.tenant_id=m.tenant_id AND d.message_id=m.id AND d.mode='attached_hook'),false)
  FROM inbox_messages m JOIN harness_attach_requests a ON a.tenant_id=m.tenant_id AND a.session_id=m.recipient_session_id JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
  WHERE m.content_mode='attached_volatile' AND m.attached_outcome IN ('queued','offered','shown') ORDER BY m.id LIMIT 1024`)
		if e != nil {
			return e
		}
		type candidate struct {
			id, epoch, grant, outcome, request, sender string
			expired, live, offerExpired                bool
		}
		var items []candidate
		for rows.Next() {
			var v candidate
			if e = rows.Scan(&v.id, &v.epoch, &v.expired, &v.live, &v.grant, &v.outcome, &v.request, &v.sender, &v.offerExpired); e != nil {
				rows.Close()
				return e
			}
			items = append(items, v)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, v := range items {
			reason := ""
			switch {
			case !s.Enabled():
				reason = "feature_disabled"
			case v.epoch != s.epoch:
				reason = "content_lost"
			case v.offerExpired:
				reason = "offer_deadline"
			case v.expired:
				reason = "deadline"
			case !v.live:
				reason = "attachment_ended"
			}
			actor := tenant.Principal{TenantID: tid, ID: v.sender, Kind: tenant.Person}
			liveCtx := tenant.WithPrincipal(ctx, actor)
			if reason == "" {
				a, err := LoadAttachment(liveCtx, tx, v.request)
				if err != nil {
					reason = "owner_authority_lost"
				} else {
					g, err := loadGrant(liveCtx, tx, v.request)
					if err != nil || s.validate(liveCtx, tx, a, g) != nil || g.State != "active" {
						reason = "messages_revoked"
					}
				}
			}
			if reason == "" && v.outcome == "queued" {
				s.mu.Lock()
				payload := s.entries[tid+"/"+v.id]
				missing := payload == nil || !time.Now().Before(payload.deadline)
				s.mu.Unlock()
				if missing {
					reason = "content_lost"
				}
			}
			if reason != "" {
				if e = Terminalize(ctx, tx, v.id, reason); e != nil {
					return e
				}
				purge = append(purge, v.id)
			}
		}
		return nil
	})
	if e == nil {
		for _, id := range purge {
			s.Discard(tid, id)
		}
	}
	return e
}
func (s *Service) Run(ctx context.Context, pool *pgxpool.Pool) {
	after := "00000000-0000-0000-0000-000000000000"
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pageCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		rows, e := pool.Query(pageCtx, `SELECT id::text FROM tenants WHERE id>$1::uuid ORDER BY id LIMIT 100`, after)
		var ids []string
		if e == nil {
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					break
				}
				ids = append(ids, id)
			}
			if e == nil {
				e = rows.Err()
			}
			rows.Close()
		}
		cancel()
		if e == nil {
			if len(ids) == 0 {
				after = "00000000-0000-0000-0000-000000000000"
			} else {
				after = ids[len(ids)-1]
			}
			for _, id := range ids {
				_ = s.Sweep(ctx, pool, id)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

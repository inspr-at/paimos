// SPDX-License-Identifier: AGPL-3.0-only
package phoneapprovals

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type recipient struct {
	p     tenant.Principal
	prefs Preferences
}
type notice struct {
	kind, id, owner string
	created         time.Time
}
type delivery struct {
	tenant, kind, request, person, subscription string
	encrypted                                   []byte
}

// Run scans live requests. Durable leases prevent duplicate sends by replicas;
// transient errors retry with a cap, and 404/410 revoke a dead subscription.
func (m *Module) Run(ctx context.Context) {
	if m.vapid == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		_ = m.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Module) dispatch(ctx context.Context) error {
	if m.vapid == nil {
		return nil
	}
	rows, err := m.pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = m.dispatchTenant(db.AllProjects(ctx, "phone notification worker; recipients reauthorized"), id); err != nil {
			return err
		}
	}
	return nil
}
func (m *Module) dispatchTenant(ctx context.Context, tenantID string) error {
	var jobs []delivery
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT p.id::text,p.roles,f.time_zone,f.quiet_start,f.quiet_end,f.escalation_minutes FROM phone_approval_preferences f JOIN principals p ON p.tenant_id=f.tenant_id AND p.id=f.person_id WHERE f.enabled AND p.kind='person' AND EXISTS(SELECT 1 FROM phone_passkeys k WHERE k.person_id=p.id AND k.revoked_at IS NULL) AND EXISTS(SELECT 1 FROM phone_push_subscriptions s WHERE s.person_id=p.id AND s.revoked_at IS NULL) ORDER BY p.id LIMIT 100`)
		if err != nil {
			return err
		}
		var recipients []recipient
		for rows.Next() {
			r := recipient{p: tenant.Principal{TenantID: tenantID, Kind: tenant.Person}, prefs: Preferences{Enabled: true}}
			if err = rows.Scan(&r.p.ID, &r.p.Roles, &r.prefs.Zone, &r.prefs.Start, &r.prefs.End, &r.prefs.Escalation); err != nil {
				rows.Close()
				return err
			}
			recipients = append(recipients, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT 'approval',r.id::text,'',r.proposed_at FROM approval_requests r WHERE r.expires_at>now() AND NOT EXISTS(SELECT 1 FROM approval_decisions d WHERE d.request_id=r.id) UNION ALL SELECT 'attach',id::text,owner_id::text,created_at FROM harness_attach_requests WHERE state='pending' AND expires_at>now() ORDER BY 4 LIMIT 100`)
		if err != nil {
			return err
		}
		var notices []notice
		for rows.Next() {
			var n notice
			if err = rows.Scan(&n.kind, &n.id, &n.owner, &n.created); err != nil {
				rows.Close()
				return err
			}
			notices = append(notices, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		now := time.Now()
		counts := map[string]int{}
		for _, n := range notices {
			rank := 0
			for _, r := range recipients {
				if n.owner != "" && n.owner != r.p.ID {
					continue
				}
				if _, err = loadReview(ctx, tx, r.p, n.kind, n.id); err != nil {
					continue
				}
				// Escalation starts with the request, so an unavailable or quiet first
				// recipient cannot hold every later authorized person indefinitely.
				eligible := rank == 0 || now.Sub(n.created) >= time.Duration(rank*r.prefs.Escalation)*time.Minute
				rank++
				if !eligible || quiet(r.prefs, now) || counts[r.p.ID] >= 10 {
					continue
				}
				subrows, err := tx.Query(ctx, `SELECT id::text,subscription FROM phone_push_subscriptions WHERE person_id=$1 AND revoked_at IS NULL ORDER BY id LIMIT 8`, r.p.ID)
				if err != nil {
					return err
				}
				var candidates []delivery
				for subrows.Next() {
					d := delivery{tenant: tenantID, kind: n.kind, request: n.id, person: r.p.ID}
					if err = subrows.Scan(&d.subscription, &d.encrypted); err != nil {
						subrows.Close()
						return err
					}
					candidates = append(candidates, d)
				}
				err = subrows.Err()
				subrows.Close()
				if err != nil {
					return err
				}
				for _, d := range candidates {
					_, err = tx.Exec(ctx, `INSERT INTO phone_push_deliveries(tenant_id,kind,request_id,person_id,subscription_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, tenantID, n.kind, n.id, r.p.ID, d.subscription)
					if err != nil {
						return err
					}
					// Claim under a row lock without holding a DB transaction across HTTP.
					tag, err := tx.Exec(ctx, `UPDATE phone_push_deliveries SET retry_at=now()+interval '2 minutes',attempts=attempts+1 WHERE tenant_id=$1 AND kind=$2 AND request_id=$3 AND subscription_id=$4 AND sent_at IS NULL AND retry_at<=now() AND attempts<5`, tenantID, n.kind, n.id, d.subscription)
					if err != nil {
						return err
					}
					if tag.RowsAffected() == 1 {
						jobs = append(jobs, d)
						counts[r.p.ID]++
					}
					if len(jobs) >= 20 {
						return nil
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, d := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = m.deliver(ctx, d); err != nil {
			return err
		}
	}
	return nil
}
func (m *Module) deliver(ctx context.Context, d delivery) error {
	// Re-check revocation, quiet hours, permissions and the pending request just
	// before HTTP. A race after this check can emit only a harmless stale pointer.
	valid := false
	err := db.InTenant(ctx, m.pool, d.tenant, func(tx pgx.Tx) error {
		var active bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM phone_push_subscriptions WHERE id=$1 AND person_id=$2 AND revoked_at IS NULL) AND EXISTS(SELECT 1 FROM phone_passkeys WHERE person_id=$2 AND revoked_at IS NULL)`, d.subscription, d.person).Scan(&active)
		if err != nil || !active {
			return err
		}
		p := tenant.Principal{ID: d.person, TenantID: d.tenant, Kind: tenant.Person}
		var kind string
		if err = tx.QueryRow(ctx, `SELECT roles,kind FROM principals WHERE id=$1`, d.person).Scan(&p.Roles, &kind); err != nil {
			return err
		}
		if kind != "person" {
			return nil
		}
		prefs, err := preference(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if !prefs.Enabled || quiet(prefs, time.Now()) {
			return nil
		}
		v, err := loadReview(ctx, tx, p, d.kind, d.request)
		valid = err == nil && v.Pending
		return nil
	})
	if err != nil || !valid {
		return err
	}
	b, err := open(m.vault, d.encrypted)
	if err != nil {
		return nil
	}
	var sub webpush.Subscription
	if json.Unmarshal(b, &sub) != nil || !validSubscription(sub) {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"url": "/phone-approvals/" + d.kind + "/" + d.request})
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, sendErr := m.send(sendCtx, payload, &sub, m.vapid)
	status := 0
	if response != nil {
		status = response.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
	}
	success := sendErr == nil && status >= 200 && status < 300
	return db.InTenant(ctx, m.pool, d.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE phone_push_deliveries SET last_status=$5,sent_at=CASE WHEN $6 THEN now() ELSE sent_at END,retry_at=now()+interval '5 minutes' WHERE kind=$1 AND request_id=$2 AND subscription_id=$3 AND person_id=$4`, d.kind, d.request, d.subscription, d.person, status, success)
		if err != nil {
			return err
		}
		p := tenant.Principal{ID: d.person, TenantID: d.tenant, Kind: tenant.Person}
		if status == http.StatusGone || status == http.StatusNotFound {
			if _, err = tx.Exec(ctx, `UPDATE phone_push_subscriptions SET revoked_at=now() WHERE id=$1 AND person_id=$2 AND revoked_at IS NULL`, d.subscription, d.person); err != nil {
				return err
			}
			return audit(ctx, tx, p, "phone_approval.subscription_expired", map[string]string{"subscription_id": d.subscription})
		}
		if success {
			return audit(ctx, tx, p, "phone_approval.notified", map[string]string{"kind": d.kind, "request_id": d.request, "subscription_id": d.subscription})
		}
		return nil
	})
}

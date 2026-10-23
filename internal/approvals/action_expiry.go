// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ActionExpiryWorker resolves timeouts through the same durable outcome path.
// Tenant keyset batches and per-tenant work are bounded; one broken tenant does
// not prevent the remaining tenants from progressing.
type ActionExpiryWorker struct {
	pool    *pgxpool.Pool
	after   string
	outcome ActionOutcomeWriter
}

func NewActionExpiryWorker(pool *pgxpool.Pool, outcome ActionOutcomeWriter) *ActionExpiryWorker {
	return &ActionExpiryWorker{pool: pool, outcome: outcome}
}

func (w *ActionExpiryWorker) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := w.ProcessOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Error("action approval expiry", "err", err)
			}
			timer.Reset(time.Minute)
		}
	}
}

func (w *ActionExpiryWorker) ProcessOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := w.pool.Query(ctx, `SELECT id::text FROM tenants WHERE id>coalesce(nullif($1,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT 64`, w.after)
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
	w.after = ""
	if len(ids) == 64 {
		w.after = ids[len(ids)-1]
	}
	var failures []error
	m := &Module{pool: w.pool, inTenant: db.InTenant, actionOutcome: w.outcome}
	for _, id := range ids {
		for range 8 {
			worked, err := m.expireAction(ctx, id, databaseActionNow)
			if err != nil {
				failures = append(failures, err)
				break
			}
			if !worked {
				break
			}
		}
	}
	return errors.Join(failures...)
}

// The injected clock is read under the same tenant/tree fence as the decision.
// Expiry never fabricates a person decision or a permission grant.
func (m *Module) expireAction(ctx context.Context, tenantID string, clock func(context.Context, pgx.Tx) (time.Time, error)) (bool, error) {
	ctx = db.AllProjects(ctx, "outward action expiry")
	worked := false
	err := m.inTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockActionWrite(ctx, tx, tenantID); err != nil {
			return err
		}
		now, err := clock(ctx, tx)
		if err != nil {
			return err
		}
		var approvalID string
		err = tx.QueryRow(ctx, `SELECT approval_id::text FROM action_approval_requests WHERE state='pending' AND expires_at<=$1 ORDER BY expires_at,id LIMIT 1`, now).Scan(&approvalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, _, err = lockRequest(ctx, tx, approvalID); err != nil {
			return err
		}
		a, err := scanAction(tx.QueryRow(ctx, actionSelect+` WHERE a.approval_id=$1 FOR UPDATE OF a`, approvalID))
		if err != nil {
			return err
		}
		if a.State != "pending" || a.ExpiresAt.After(now) {
			return nil
		}
		var systemID string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND name='System' AND roles @> ARRAY['system'] AND status='active'`).Scan(&systemID); err != nil {
			return err
		}
		p := tenant.Principal{ID: systemID, TenantID: tenantID, Kind: tenant.Agent}
		if err = m.resolveActionTx(ctx, tx, p, a, "expired", "Expired without a person decision before the request deadline.", now); err != nil {
			return err
		}
		worked = true
		return nil
	})
	return worked, err
}

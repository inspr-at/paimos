// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

// RunAudit is observe-only. Its GitHub capability can only read commits/checks.
func (m *Module) RunAudit(ctx context.Context) {
	if _, ok := m.github.(AuditGitHub); !ok {
		return
	}
	app := &crossreview.GitHubApp{Config: m.config}
	if !app.Configured(m.config.TenantID, m.config.Repository) {
		return
	}
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		m.auditSweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Module) auditSweep(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	ctx = db.WithConnection(ctx, m.pool, conn)
	key := "aeon-delivery-merge-audit:" + m.config.TenantID
	var locked bool
	if conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked) != nil || !locked {
		return
	}
	defer unlock(conn, key)
	if err = m.ReconcileAudit(ctx, m.config.TenantID); err != nil {
		slog.Warn("delivery merge audit reconciliation incomplete", "tenant_id", m.config.TenantID)
	}
}

func (m *Module) ReconcileAudit(ctx context.Context, tid string) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	reader, ok := m.github.(AuditGitHub)
	if tid != m.config.TenantID || !ok {
		return errNotConfigured
	}
	shas, err := reader.AuditCommits(ctx)
	if err != nil {
		return err
	}
	if len(shas) > 100 {
		return errRead
	}
	for _, sha := range shas {
		present, err := m.auditExists(ctx, tid, sha)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		f, err := reader.AuditCommit(ctx, sha)
		if err != nil {
			return err
		}
		if f == nil {
			continue
		} // a constituent commit, not another merge/push
		if f.SHA != sha {
			return errRead
		}
		if err = m.auditMerge(ctx, tid, *f, reader); err != nil {
			return err
		}
	}
	return m.retryAuditAlerts(ctx, tid)
}

func (m *Module) retryAuditAlerts(ctx context.Context, tid string) error {
	actor, err := m.auditActor(ctx, tid)
	if err != nil {
		return err
	}
	// Bounded oldest-key pass; rows without a resolvable lead remain pending
	// and never consume the entire notification batch indefinitely.
	cursor := ""
	for {
		page := []MergeAudit{}
		err = db.InTenant(db.AllProjects(ctx, "pending merge audit alerts"), m.pool, tid, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT `+auditColumns+` FROM delivery_merge_audit WHERE repository=$1 AND merge_sha>$2 AND cardinality(flags)>0 AND alerted_at IS NULL ORDER BY merge_sha LIMIT 100`, m.config.Repository, cursor)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				a, err := scanAudit(rows)
				if err != nil {
					return err
				}
				page = append(page, a)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		for _, a := range page {
			cursor = a.SHA
			err = db.InTenant(db.AllProjects(ctx, "merge audit lead alert retry"), m.pool, tid, func(tx pgx.Tx) error {
				if err := db.LockTree(ctx, tx, tid); err != nil {
					return err
				}
				a, err := scanAudit(tx.QueryRow(ctx, `SELECT `+auditColumns+` FROM delivery_merge_audit WHERE repository=$1 AND merge_sha=$2 AND alerted_at IS NULL FOR NO KEY UPDATE`, a.Repository, a.SHA))
				if errors.Is(err, pgx.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				to, err := auditRecipientTx(ctx, tx, tid, a)
				if err != nil || to == nil {
					return err
				}
				// All state changes and locks precede the event counter.
				if _, err = tx.Exec(ctx, `UPDATE delivery_merge_audit SET alerted_at=$3 WHERE repository=$1 AND merge_sha=$2`, a.Repository, a.SHA, m.now()); err != nil {
					return err
				}
				return auditAlert(ctx, tx, actor, a, to)
			})
			if err != nil {
				return err
			}
		}
		if len(page) < 100 {
			return nil
		}
	}
}

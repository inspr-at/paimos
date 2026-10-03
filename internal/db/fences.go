// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// LockTenant fences access, identity and exact-key changes without conflicting
// with foreign-key KEY SHARE locks. Acquire it before tree/pairing/resource locks.
func LockTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, tenantID).Scan(&id)
}

// LockTree is the shared tenant-first entry for project/tree writers. Re-entry
// is safe only when these fences were acquired before any resource rows.
func LockTree(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if err := LockTenant(ctx, tx, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, tenantID)
	return err
}

// LockCurrentTree uses the transaction's RLS tenant, never a caller-selected scope.
func LockCurrentTree(ctx context.Context, tx pgx.Tx) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&id); err != nil {
		return err
	}
	return LockTree(ctx, tx, id)
}

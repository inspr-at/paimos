// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// TenantFenceSQL is the one statement every tenant fence issues. Lock-order
// proofs observe it in pg_stat_activity, so every writer path must block on
// this exact text; a second spelling is an invisible fence to those proofs.
const TenantFenceSQL = `SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE`

// LockTenant fences access, identity and exact-key changes without conflicting
// with foreign-key KEY SHARE locks. Acquire it before tree/pairing/resource locks.
// It locks the transaction's RLS tenant and refuses a caller tenant that differs.
func LockTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var id string
	if err := tx.QueryRow(ctx, TenantFenceSQL).Scan(&id); err != nil {
		return err
	}
	if !strings.EqualFold(id, tenantID) {
		return fmt.Errorf("tenant fence: transaction tenant %s is not the caller tenant %s", id, tenantID)
	}
	return nil
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

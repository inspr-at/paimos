// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// TenantFenceSQL is the one statement every tenant fence issues inside tenant
// visibility. Lock-order proofs observe it in pg_stat_activity, so every writer
// path behind the HTTP boundary must block on this exact text; a second
// spelling is an invisible fence to those proofs.
const TenantFenceSQL = `SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE`

// operatorTenantFenceSQL fences a caller-named tenant from a transaction that
// never entered tenant visibility: operator commands, importers and test
// holders on admin connections. It takes the same NO KEY UPDATE row lock, so it
// conflicts with TenantFenceSQL and stays compatible with FK KEY SHARE locks.
const operatorTenantFenceSQL = `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`

// visibleTenant reports the transaction's tenant visibility, or "" when the
// transaction never entered db.InTenant.
func visibleTenant(ctx context.Context, tx pgx.Tx) (string, error) {
	var id *string
	if err := tx.QueryRow(ctx, `SELECT nullif(current_setting('aeon.tenant_id', true), '')`).Scan(&id); err != nil {
		return "", err
	}
	if id == nil {
		return "", nil
	}
	return *id, nil
}

// LockTenant fences access, identity and exact-key changes without conflicting
// with foreign-key KEY SHARE locks. Acquire it before tree/pairing/resource locks.
// Inside tenant visibility it locks the transaction's RLS tenant with the
// canonical statement and refuses a caller tenant that differs. A transaction
// without tenant visibility fences the caller tenant directly.
func LockTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	visible, err := visibleTenant(ctx, tx)
	if err != nil {
		return err
	}
	return lockTenant(ctx, tx, visible, tenantID)
}

func lockTenant(ctx context.Context, tx pgx.Tx, visible, tenantID string) error {
	var id string
	if visible == "" {
		if tenantID == "" {
			return fmt.Errorf("tenant fence: no tenant visibility and no caller tenant")
		}
		return tx.QueryRow(ctx, operatorTenantFenceSQL, tenantID).Scan(&id)
	}
	if !strings.EqualFold(visible, tenantID) {
		return fmt.Errorf("tenant fence: transaction tenant %s is not the caller tenant %s", visible, tenantID)
	}
	return tx.QueryRow(ctx, TenantFenceSQL).Scan(&id)
}

// LockTree is the shared tenant-first entry for project/tree writers. Re-entry
// is safe only when these fences were acquired before any resource rows. The
// advisory statement stays inline: TestSharedFencePrimitiveOrder proves the
// tenant fence precedes it by reading this function body.
func LockTree(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if err := LockTenant(ctx, tx, tenantID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, tenantID)
	return err
}

// LockCurrentTree uses the transaction's RLS tenant, never a caller-selected
// scope, and refuses a transaction that never entered tenant visibility. It
// enters through LockTree so the shared fence inventory sees one tree entry.
func LockCurrentTree(ctx context.Context, tx pgx.Tx) error {
	visible, err := visibleTenant(ctx, tx)
	if err != nil {
		return err
	}
	if visible == "" {
		return fmt.Errorf("tenant fence: transaction has no tenant visibility")
	}
	return LockTree(ctx, tx, visible)
}

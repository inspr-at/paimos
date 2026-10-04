// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

type workStatusGroupKey struct{}

// Historical migration tests also use InTenant before this optional migration
// exists. The SQL implementation fails closed when AEON-429 is not installed.
func beginWorkStatus(ctx context.Context, tx pgx.Tx) (bool, error) {
	var installed bool
	if err := tx.QueryRow(ctx, `SELECT to_regprocedure('aeon_work_status_begin()') IS NOT NULL`).Scan(&installed); err != nil {
		return false, err
	}
	if !installed {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var active bool
	if err := tx.QueryRow(ctx, `SELECT aeon_work_status_begin()`).Scan(&active); err != nil {
		return false, fmt.Errorf("work status entry: %w", err)
	}
	return active, nil
}
func flushWorkStatus(ctx context.Context, tx pgx.Tx) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := tx.Exec(ctx, `SELECT aeon_work_status_flush()`)
	if err != nil {
		return fmt.Errorf("work status derivation: %w", err)
	}
	return nil
}

// WorkStatusParentTx exposes a narrow target predicate, never child identities.
// It defaults false on historical databases and before AEON-429 rollout.
func WorkStatusParentTx(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var installed bool
	if err := tx.QueryRow(ctx, `SELECT to_regprocedure('aeon_work_status_is_parent(uuid)') IS NOT NULL`).Scan(&installed); err != nil {
		return false, err
	}
	if !installed {
		return false, nil
	}
	var parent bool
	err := tx.QueryRow(ctx, `SELECT aeon_work_status_is_parent($1::uuid)`, id).Scan(&parent)
	return parent, err
}

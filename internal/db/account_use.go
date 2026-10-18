// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CheckRequiredCapabilities refuses a schema this binary cannot enforce.
// Run after migrations and before mounting handlers or starting background jobs.
func CheckRequiredCapabilities(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT capability FROM aeon_required_capabilities ORDER BY capability LIMIT 1025`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var capability string
		if err := rows.Scan(&capability); err != nil {
			return err
		}
		count++
		if count > 1024 || capability != "account_use_v1" {
			return fmt.Errorf("required database capability is unsupported: %s", capability)
		}
	}
	return rows.Err()
}

func backfillAccountUse(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM tenants ORDER BY id FOR NO KEY UPDATE`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := enterTenant(AllProjects(ctx, "migration 1309: account-use preserve behavior"), tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT aeon_seed_account_use($1,true)`, id); err != nil {
			return err
		}
	}
	return nil
}

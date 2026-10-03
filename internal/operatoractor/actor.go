// SPDX-License-Identifier: AGPL-3.0-only

// Package operatoractor identifies host CLI writes in the tenant event log.
package operatoractor

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Ensure returns the tenant's service principal, creating it and its
// principal.created event on first use. The advisory and tenant-row locks
// serialize concurrent first use with other tenant membership mutations.
func Ensure(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	return EnsureWithProduction(ctx, tx, tenantID, false)
}

// EnsureWithProduction marks the principal creation event when a confirmed
// production host command creates the operator for the first time.
func EnsureWithProduction(ctx context.Context, tx pgx.Tx, tenantID string, production bool) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, tenantID); err != nil {
		return "", err
	}
	var lockedID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR UPDATE`, tenantID).Scan(&lockedID); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM principals
		WHERE tenant_id=$1::uuid AND kind='agent' AND name='Access operator'
		  AND roles=ARRAY['operator']::text[] ORDER BY id LIMIT 1`, tenantID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var after []byte
	err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles)
		VALUES($1::uuid,'agent','Access operator',ARRAY['operator']::text[])
		RETURNING id::text,to_jsonb(principals)`, tenantID).Scan(&id, &after)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,after,at)
		VALUES($1::uuid,$2::uuid,'principal.created',
		CASE WHEN $4::boolean THEN jsonb_set($3::jsonb,'{production}','true'::jsonb) ELSE $3::jsonb END,
		clock_timestamp())`, tenantID, id, after, production)
	return id, err
}

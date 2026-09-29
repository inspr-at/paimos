// SPDX-License-Identifier: AGPL-3.0-only

// Package systemactor is the tenant's System principal: the actor for a
// background job that changes a node and must leave an event. Migration 0810
// created it for authz bookkeeping; migration 0979 keeps exactly one per
// tenant. The method-learning tagger is the first node writer to use it.
package systemactor

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// Ensure returns the tenant's System principal, creating it and its
// principal.created event on first use. Call it inside db.InTenant for that
// tenant: the row is subject to row-level security.
func Ensure(ctx context.Context, tx pgx.Tx, tenantID string) (tenant.Principal, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT aeon_authz_system_actor($1::uuid)::text`, tenantID).Scan(&id)
	if err != nil {
		return tenant.Principal{}, err
	}
	return tenant.Principal{
		ID:       id,
		TenantID: tenantID,
		Kind:     tenant.Agent,
		Name:     "System",
		Roles:    []string{"system"},
	}, nil
}

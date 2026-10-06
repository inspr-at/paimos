// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// OperatorCreateAgentKey creates an agent key for the named agent principal
// (created on first use) without an HTTP admin session. It is for the
// operator-only CLI with an explicit active person creator in the same tenant.
// The caller is responsible for writing the returned token
// to a protected file and never printing it.
func OperatorCreateAgentKey(ctx context.Context, pool *pgxpool.Pool, tenantID, name, principalID string, scopes []string, expires *time.Time, creatorIDs ...string) (id, agentID, token string, err error) {
	if len(creatorIDs) != 1 || !uuidRe.MatchString(creatorIDs[0]) {
		return "", "", "", fmt.Errorf("a person creator UUID is required")
	}
	clean, err := cleanScopes(scopes)
	if err != nil {
		return "", "", "", err
	}
	m := &Module{pool: pool, inTenant: db.InTenant}
	// Operator CLI, no principal: keys are workspace rows (ADR-003 P2).
	ctx = db.NoProjects(ctx, "operator agent key")
	rec, err := m.createAgentKey(ctx, tenant.Principal{TenantID: tenantID, KeyCreatorID: creatorIDs[0]}, name, principalID, clean, expires)
	if err != nil {
		return "", "", "", err
	}
	return rec.ID, rec.PrincipalID, rec.Token, nil
}

// OperatorRevokeAgentKey revokes an agent key by id.
func OperatorRevokeAgentKey(ctx context.Context, pool *pgxpool.Pool, tenantID, id string) error {
	m := &Module{pool: pool, inTenant: db.InTenant}
	return m.revokeAgentKey(db.NoProjects(ctx, "operator agent key"), tenant.Principal{TenantID: tenantID}, id)
}

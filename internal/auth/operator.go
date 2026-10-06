// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/journey"
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

// OperatorGrantJourneyScopes adds the two legacy journey gate prefixes to one
// newly created demo key. They are outside the agent permission registry, so
// the ordinary key creation path cannot accept them.
func OperatorGrantJourneyScopes(ctx context.Context, pool *pgxpool.Pool, tenantID, keyID, principalID string) error {
	m := &Module{pool: pool, inTenant: db.InTenant}
	return m.grantJourneyScopes(db.NoProjects(ctx, "operator agent key"), tenantID, keyID, principalID, []string{journey.ScopeRequirements, journey.ScopeBuild})
}

// JourneyGateScopes accepts only the gate names checked by the journey module.
// Ordinary agent-key creation still uses the permission registry and cannot
// grant these scopes through the HTTP API.
func JourneyGateScopes(gates []string) ([]string, error) {
	allowed := map[string]string{
		journey.GateShape:        journey.ScopeShape,
		journey.GateRequirements: journey.ScopeRequirements,
		journey.GateBuild:        journey.ScopeBuild,
		journey.GateCandidate:    journey.ScopeCandidate,
		journey.GateDeploy:       journey.ScopeDeploy,
		journey.GateAccess:       journey.ScopeAccess,
	}
	if len(gates) == 0 {
		return nil, fmt.Errorf("at least one journey gate is required")
	}
	scopes := make([]string, 0, len(gates))
	for _, gate := range gates {
		scope, ok := allowed[gate]
		if !ok {
			return nil, fmt.Errorf("unknown journey gate %q", gate)
		}
		if !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes, nil
}

// OperatorAddJourneyGateScopes extends one existing key, attributing the
// before/after scope event to the tenant's Access operator.
func OperatorAddJourneyGateScopes(ctx context.Context, pool *pgxpool.Pool, tenantID, keyID string, gates []string) ([]string, error) {
	scopes, err := JourneyGateScopes(gates)
	if err != nil {
		return nil, err
	}
	m := &Module{pool: pool, inTenant: db.InTenant}
	return scopes, m.grantJourneyScopes(db.NoProjects(ctx, "operator agent key"), tenantID, keyID, "", scopes)
}

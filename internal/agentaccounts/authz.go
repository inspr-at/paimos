// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

func presentedSecret(r *http.Request) (prefix, secret string, ok bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "aeon_")
	if !ok {
		return "", "", false
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || prefix == "" || secret == "" {
		return "", "", false
	}
	return prefix, secret, true
}

func keyScopes(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal) ([]string, error) {
	return readKeyScopes(ctx, tx, r, p, false)
}

func readKeyScopes(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, lockKey bool) ([]string, error) {
	prefix, secret, ok := presentedSecret(r)
	if !ok {
		return nil, fail(http.StatusForbidden, "agent key required")
	}
	sum := sha256.Sum256([]byte(secret))
	var scopes []string
	query := `
		SELECT scopes FROM agent_keys
		WHERE prefix = $1 AND hash = $2 AND principal_id = $3::uuid
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`
	if lockKey {
		// Person key revocation locks this row without the pairing fence.
		// Hold its live scope ceiling through the final evidence mutation.
		query += ` FOR SHARE`
	}
	err := tx.QueryRow(ctx, query,
		prefix, hex.EncodeToString(sum[:]), p.ID).Scan(&scopes)
	if err != nil {
		return nil, fail(http.StatusForbidden, "agent key required")
	}
	return scopes, nil
}

func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func requireScope(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, scope string) error {
	scopes, err := keyScopes(ctx, tx, r, p)
	if err != nil {
		return err
	}
	if !hasScope(scopes, scope) {
		return fail(http.StatusForbidden, "missing scope "+scope)
	}
	return nil
}

// actorMayUseRun is the in-process check for Settle and Release. An assigned
// agent, an agent holding a live run.claim grant, or an admin person may
// settle or release. HTTP routing adds the key-scope ceiling separately.
func actorMayUseRun(ctx context.Context, tx pgx.Tx, actor tenant.Principal, runAgentID, runID string) error {
	if actor.Kind == tenant.Person && authz.RequireTx(ctx, tx, actor, "runs.control", authz.Scope{}) == nil {
		return nil
	}
	if actor.Kind != tenant.Agent {
		return fail(http.StatusForbidden, "agent key required")
	}
	if actor.ID == runAgentID {
		return nil
	}
	ok, err := hasClaimGrant(ctx, tx, actor.ID, runID)
	if err != nil {
		return err
	}
	if !ok {
		return fail(http.StatusForbidden, "run is not assigned to this agent")
	}
	return nil
}

func hasClaimGrant(ctx context.Context, tx pgx.Tx, agentID, runID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_permission_grants g
			WHERE g.agent_principal_id = $1::uuid
			  AND g.scope = 'run.claim'
			  AND g.revoked_at IS NULL
			  AND g.valid_until > now()
			  AND (
			    (g.resource_kind = 'run' AND g.resource_id = $2::uuid)
			    OR g.resource_kind = 'tenant'
			  )
		)`, agentID, runID).Scan(&ok)
	return ok, err
}

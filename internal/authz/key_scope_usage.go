// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// LockKeyScopeUseTx serializes grants and trim decisions for one authenticating
// key. Call before key row locks and before the event counter. It neither locks
// the tenant nor upgrades a key FK lock, so transactional resource checks keep
// their existing tenant -> tree -> record order. Callers touching several keys
// must acquire their entire batch in canonical order before other key locks.
func LockKeyScopeUseTx(ctx context.Context, tx pgx.Tx, tenantID, keyID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,615))`, strings.ToLower(tenantID+":"+keyID))
	return err
}

func recordKeyScopeUseTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string) error {
	if p.Kind != tenant.Agent || p.KeyID == "" {
		return nil // internal principals without an authenticating key
	}
	if err := LockKeyScopeUseTx(ctx, tx, p.TenantID, p.KeyID); err != nil {
		return err
	}
	// A grant waiting behind approval must see the new live ceiling. SELECT
	// takes no key row lock; the usage fence, shared with trims, is authoritative.
	var scopes []string
	err := tx.QueryRow(ctx, `SELECT scopes FROM agent_keys WHERE tenant_id=$1::uuid AND id=$2::uuid
	 AND principal_id=$3::uuid AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())`,
		p.TenantID, p.KeyID, p.ID).Scan(&scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	used := []string{permission}
	if !containsScope(scopes, permission) {
		if !CoordinatorCeiling(scopes, permission) {
			return ErrForbidden
		}
		// Derived coordinator reads depend on every base scope. Their use is
		// evidence against dropping any prerequisite, even on pre-AEON-327 keys.
		used = CoordinatorBaseScopes
	}
	for _, scope := range used {
		if err := upsertKeyScopeUseTx(ctx, tx, p, scope, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func upsertKeyScopeUseTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, scope string, at time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO agent_key_scope_usage(tenant_id,key_id,scope,last_used_at)
	 VALUES($1::uuid,$2::uuid,$3,$4)
	 ON CONFLICT(tenant_id,key_id,scope) DO UPDATE SET last_used_at=EXCLUDED.last_used_at
	 WHERE agent_key_scope_usage.last_used_at<=EXCLUDED.last_used_at-interval '1 minute'`, p.TenantID, p.KeyID, scope, at)
	return err
}

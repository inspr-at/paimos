// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type keyScopeUseContextKey struct{}

// ErrKeyAuthorityChanged rejects a request authenticated before key adoption.
// The caller must authenticate again to capture the current person ceiling.
var ErrKeyAuthorityChanged = errors.New("agent key authority changed")

// ValidateKeyCreatorTx checks the authenticated creator snapshot under the
// key-use fence, before visibility or permission decisions use that snapshot.
// It takes no row locks; adoption holds the same admission fence until commit.
func ValidateKeyCreatorTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Agent || p.KeyID == "" {
		return nil
	}
	var creator string
	var fullAccess bool
	err := tx.QueryRow(ctx, `SELECT coalesce(created_by_principal_id::text,''),coalesce(full_access,false) FROM agent_keys
	 WHERE tenant_id=$1::uuid AND id=$2::uuid AND principal_id=$3::uuid`, p.TenantID, p.KeyID, p.ID).Scan(&creator, &fullAccess)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!strings.EqualFold(creator, p.KeyCreatorID) || fullAccess != p.FullAccess) {
		return ErrKeyAuthorityChanged
	}
	return err
}

type keyScopeUseBatch struct {
	tenantID string
	keyIDs   []string
}

// WithKeyScopeUse declares the complete target-key batch before InTenant.
// Proposals use it when their target differs from the authenticating key.
// It grants no authority; callers must still authorize the final write.
func WithKeyScopeUse(ctx context.Context, tenantID string, keyIDs ...string) context.Context {
	return context.WithValue(ctx, keyScopeUseContextKey{}, keyScopeUseBatch{tenantID, slices.Clone(keyIDs)})
}

// lockAgentScopeUse admits a keyed transaction before any tenant, tree or row
// locks. RequireTx can run before or after resource locks, but must never be
// the first acquirer of this fence in a keyed request. Different keys remain
// independent; transactions sharing a key serialize, as do trims of that key.
func lockAgentScopeUse(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var keys []string
	if batch, ok := ctx.Value(keyScopeUseContextKey{}).(keyScopeUseBatch); ok && batch.tenantID == tenantID {
		keys = slices.Clone(batch.keyIDs)
	}
	if p, ok := tenant.PrincipalFrom(ctx); ok && p.Kind == tenant.Agent && p.KeyID != "" && p.TenantID == tenantID {
		keys = append(keys, p.KeyID)
	}
	for i := range keys {
		keys[i] = strings.ToLower(keys[i])
	}
	slices.Sort(keys)
	for _, keyID := range slices.Compact(keys) {
		if err := LockKeyScopeUseTx(ctx, tx, tenantID, keyID); err != nil {
			return err
		}
	}
	return nil
}

// LockKeyScopeUseTx is the admission fence shared with person-owned trims.
// Take the entire sorted key batch before tenant/tree/resource locks; the
// event counter remains last. Re-entering a held transaction lock is safe.
func LockKeyScopeUseTx(ctx context.Context, tx pgx.Tx, tenantID, keyID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,615))`, strings.ToLower(tenantID+":"+keyID))
	return err
}

// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// NativeAccountBinding pins enrollment identity, separately from the local
// profile revision. Labels, quota readings and probe timestamps are not identity.
// Keep this value in the owning private admission record; it is not a wire DTO.
type NativeAccountBinding struct {
	TenantID, AccountID, AccountKey, Harness, OwnerPersonID string
	LinkRevision                                            int64
	QuotaFingerprint, QuotaPoolFingerprint                  string
}

func (NativeAccountBinding) MarshalJSON() ([]byte, error) {
	return nil, errors.New("native account binding is local authority")
}

func (*NativeAccountBinding) UnmarshalJSON([]byte) error {
	return errors.New("native account binding cannot be decoded from JSON")
}

// Digest binds the local conversation tombstone to server enrollment identity,
// including a relink or a changed canonical quota/login identity under one ID.
func (b NativeAccountBinding) Digest() string {
	data, _ := json.Marshal([]any{b.TenantID, b.AccountID, b.AccountKey, b.Harness,
		b.OwnerPersonID, b.LinkRevision, b.QuotaFingerprint, b.QuotaPoolFingerprint})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// BindNativeAccountTx is called after tenant/tree/pairing/matrix fences and
// current actor authorization, before routing. No credential or local path is
// read. The account's existing link revision and identity digests suffice;
// this slice needs no migration or replacement occupancy predicates.
func BindNativeAccountTx(ctx context.Context, tx pgx.Tx, in NativeAdmission) (NativeAccountBinding, error) {
	var out NativeAccountBinding
	err := tx.QueryRow(ctx, `SELECT a.tenant_id::text,a.id::text,a.account_key,a.harness,
 `+modelprefs.CanonicalPersonSQL("a.owner_person_id")+`::text,a.link_revision,a.quota_fingerprint,a.quota_pool_fingerprint
 FROM agent_accounts a WHERE a.id=$1 AND a.tenant_id=$2 AND a.archived_at IS NULL
 AND a.owner_person_id IS NOT NULL FOR NO KEY UPDATE OF a`, in.AccountID, in.TenantID).Scan(
		&out.TenantID, &out.AccountID, &out.AccountKey, &out.Harness, &out.OwnerPersonID,
		&out.LinkRevision, &out.QuotaFingerprint, &out.QuotaPoolFingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nativeDenial(nativecontract.ReasonGrantMissing)
	}
	if err != nil {
		return out, err
	}
	if out.OwnerPersonID != in.OwnerPersonID || out.Harness != in.Harness {
		return out, nativeDenial(nativecontract.ReasonIdentityMismatch)
	}
	return out, nil
}

// RequireNativeSessionTx enters the FINAL launch/input transaction before any
// resource lock or event. The owning service also revalidates the original
// actor's immutable grant in this transaction; this helper cannot grant tools,
// continuation, controller rights or permission to create a successor.
// exceptSession is empty for initial admission and the exact held session for
// revalidation. Local admission must retain/check the shared ledger separately.
func RequireNativeSessionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in NativeAdmission, expected NativeAccountBinding, exceptSession string, now time.Time) error {
	if p.TenantID != in.TenantID || p.ID != in.PrincipalID || p.Kind != tenant.Agent ||
		expected.TenantID != in.TenantID || expected.AccountID != in.AccountID ||
		expected.Harness != in.Harness || expected.OwnerPersonID != in.OwnerPersonID ||
		expected.AccountKey == "" || expected.LinkRevision < 1 ||
		exceptSession != "" && exceptSession != in.OwnedSessionID {
		return nativeDenial(nativecontract.ReasonIdentityMismatch)
	}
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return err
	}
	if err := accountuse.LockShared(ctx, tx); err != nil {
		return err
	}
	for _, permission := range []string{"harness.worker", "harness.control"} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: in.ProjectID}); err != nil {
			return nativeDenial(nativecontract.ReasonPermissionRevoked)
		}
	}
	actual, err := BindNativeAccountTx(ctx, tx, in)
	if err != nil {
		return err
	}
	if actual != expected {
		return nativeDenial(nativecontract.ReasonIdentityMismatch)
	}
	// Reuse P1b's account/model/context/quota gates and the released AEON-1251
	// account-backed worker + uncertain-exit host predicate without rewriting it.
	// Capacity is tenant-wide even for a project-scoped key. This exact scoped
	// expansion is also used by the native start store; it reveals no sibling
	// account/session facts and is restored before the caller continues.
	var visibility, system string
	if err = tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return err
	}
	capacityErr := RequireNativeCapacityTx(ctx, tx, in, exceptSession, now)
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return errors.Join(capacityErr, err)
}

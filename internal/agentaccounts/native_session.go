// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
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

// NativeSessionAuthority revalidates the persisted original actor's current
// native/tool/settings/history grant in the supplied final transaction. It must
// perform no I/O or event writes, and refuse revoked or changed grants with their
// exact cause. The immutable stored policy is a pin, never current consent.
type NativeSessionAuthority interface {
	RequireNativeGrantTx(context.Context, pgx.Tx, nativecontract.Actor, nativecontract.StartIntent, nativecontract.Policy) error
}

// RequireNativeSessionTx enters the FINAL launch/input transaction before any
// resource lock or event. The daemon needs only its exact live paired key and
// harness.worker; the stored ORIGINAL actor needs current harness.control and
// a separately revalidated native grant. This helper cannot grant tools,
// continuation, controller rights or permission to create a successor.
// A delegated original actor's key must be in InTenant's sorted admission batch
// before entry (db.WithKeyScopeUse); no key fence is acquired after record locks.
// exceptSession is empty for initial admission and the exact held session for
// revalidation. Local admission must retain/check the shared ledger separately.
func RequireNativeSessionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in NativeAdmission, expected NativeAccountBinding, exceptSession string, now time.Time, authority NativeSessionAuthority) error {
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
	if authority == nil {
		return nativeDenial(nativecontract.ReasonQualificationMissing)
	}
	// A second key of the same principal is not the approved pairing key.
	var paired bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_computers c
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id
 WHERE c.tenant_id=$1 AND c.id=$2 AND c.principal_id=$3 AND c.key_id=NULLIF($4,'')::uuid
 AND c.state='connected' AND q.state='redeemed' AND e.account_id=$5
 AND e.state='connected' AND e.ongoing_approved_at IS NOT NULL)`, in.TenantID, in.ComputerID, p.ID, p.KeyID, in.AccountID).Scan(&paired); err != nil {
		return err
	}
	if !paired {
		return nativeDenial(nativecontract.ReasonPermissionRevoked)
	}
	if err := accountuse.LockShared(ctx, tx); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: in.ProjectID}); err != nil {
		return nativeDenial(nativecontract.ReasonPermissionRevoked)
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
	if err := errors.Join(capacityErr, err); err != nil {
		return err
	}
	return requireNativeActorTx(ctx, tx, in, now, authority)
}

func requireNativeActorTx(ctx context.Context, tx pgx.Tx, in NativeAdmission, now time.Time, authority NativeSessionAuthority) error {
	var actorRaw, intentRaw, policyRaw []byte
	var principalID, kind, keyID, grantID string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT i.actor_authority,i.intent,i.policy,i.actor_principal_id::text,
 i.actor_kind,coalesce(i.actor_key_id::text,''),i.actor_grant_id::text,i.actor_grant_revision
 FROM native_session_intents i JOIN native_session_bindings b ON b.tenant_id=i.tenant_id AND b.id=i.owned_session_id
 WHERE i.tenant_id=$1 AND i.owned_session_id=$2 AND i.project_id=$3 AND i.owner_person_id=$4
 AND b.account_id=$5 AND b.computer_id=$6 AND b.paired_principal_id=$7 AND b.daemon_id=$8
 FOR NO KEY UPDATE OF i,b`, in.TenantID, in.OwnedSessionID, in.ProjectID, in.OwnerPersonID,
		in.AccountID, in.ComputerID, in.PrincipalID, in.DaemonID).Scan(&actorRaw, &intentRaw, &policyRaw, &principalID, &kind, &keyID, &grantID, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nativeDenial(nativecontract.ReasonGrantMissing)
	}
	if err != nil {
		return err
	}
	var stored struct {
		Principal     tenant.Principal `json:"principal"`
		KeyID         string           `json:"key_id"`
		AuthKeyID     string           `json:"auth_key_id"`
		ActorOwnerID  string           `json:"actor_owner_id"`
		GrantID       string           `json:"grant_id"`
		GrantRevision int64            `json:"grant_revision"`
	}
	var intent nativecontract.StartIntent
	var policy nativecontract.Policy
	for _, v := range []struct {
		raw []byte
		dst any
	}{{actorRaw, &stored}, {intentRaw, &intent}, {policyRaw, &policy}} {
		if len(v.raw) > 131072 {
			return nativeDenial(nativecontract.ReasonBufferFull)
		}
		if err := json.Unmarshal(v.raw, v.dst); err != nil {
			return err
		}
	}
	original := stored.Principal
	original.KeyID, original.AuthKeyID = stored.KeyID, stored.AuthKeyID
	if original.TenantID != in.TenantID || original.ID != principalID || string(original.Kind) != kind ||
		original.Kind != tenant.Person && original.Kind != tenant.Agent || stored.KeyID != keyID ||
		original.Kind == tenant.Agent && keyID == "" || !workorders.UUID(principalID) ||
		stored.ActorOwnerID != in.OwnerPersonID || stored.GrantID != grantID || stored.GrantRevision != revision ||
		policy.GrantID != grantID || policy.GrantRevision != revision ||
		intent.AccountID != in.AccountID || intent.ComputerID != in.ComputerID || intent.Harness != in.Harness ||
		intent.ModelProfileID != in.ModelProfileID || !reflect.DeepEqual(intent.WorkID, in.WorkID) ||
		intent.Role == "coordinator" && (original.Kind != tenant.Person || in.RunID != nil) {
		return nativeDenial(nativecontract.ReasonIdentityMismatch)
	}
	if err := authz.RequireTx(ctx, tx, original, "harness.control", authz.Scope{ProjectID: in.ProjectID}); err != nil {
		return nativeDenial(nativecontract.ReasonPermissionRevoked)
	}
	if !now.Before(policy.ExpiresAt) {
		return nativeDenial(nativecontract.ReasonGrantExpired)
	}
	actor := nativecontract.Actor{TenantID: original.TenantID, PrincipalID: original.ID, PrincipalKind: string(original.Kind),
		OwnerPersonID: stored.ActorOwnerID, KeyCeiling: original.Scopes, GrantID: stored.GrantID, GrantRevision: stored.GrantRevision}
	return authority.RequireNativeGrantTx(ctx, tx, actor, intent, policy)
}

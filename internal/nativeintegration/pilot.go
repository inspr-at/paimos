// SPDX-License-Identifier: AGPL-3.0-only

// Package nativeintegration composes the qualified LEAD pilot boundaries.
// Construction supplies no consent and enables no runtime or automatic lead.
package nativeintegration

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/nativeadmission"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RootAuthority connects P4c's local admission port to the exact server-owned
// native root. It never invents a worker run, authenticating principal or key.
// Grants must re-read the ORIGINAL actor's current native consent in each final
// transaction. Changing a local profile requires its own explicit authority;
// absent that callback, profile changes remain unavailable.
type RootAuthority struct {
	Pool          *pgxpool.Pool
	Grants        agentaccounts.NativeSessionAuthority
	ProfileChange func(context.Context, nativecontract.ProfileRequest) error
	Now           func() time.Time
}

func refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (a RootAuthority) AuthorizeProfileChange(ctx context.Context, request nativecontract.ProfileRequest) error {
	if a.ProfileChange == nil {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	return a.ProfileChange(ctx, request)
}

func (a RootAuthority) Revalidate(ctx context.Context, request nativeadmission.Request) error {
	if a.Pool == nil || a.Grants == nil {
		return refuse(nativecontract.ReasonQualificationMissing)
	}
	p, ok := tenant.PrincipalFrom(ctx)
	b := request.Binding
	if !ok || p.Kind != tenant.Agent || p.KeyID == "" || p.TenantID != b.TenantID || b.RunID != nil ||
		request.Profile.TenantID != p.TenantID || request.Profile.AccountID == "" ||
		request.Profile.BindingRevision != b.BindingRevision {
		return refuse(nativecontract.ReasonIdentityMismatch)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Include a delegated original actor's key BEFORE the resource transaction.
	// Its immutable identity is checked again under the final pairing fence.
	var originalKey string
	if err := db.InTenant(ctx, a.Pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(actor_key_id::text,'') FROM native_session_intents
 WHERE owned_session_id=$1 AND project_id=$2`, b.OwnedSessionID, b.ProjectID).Scan(&originalKey)
	}); err != nil {
		return err
	}
	if originalKey != "" {
		ctx = db.WithKeyScopeUse(ctx, p.TenantID, originalKey)
	}
	return db.InTenant(ctx, a.Pool, p.TenantID, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := accountuse.LockShared(ctx, tx); err != nil {
			return err
		}
		var in agentaccounts.NativeAdmission
		var policyRaw []byte
		var workspace, key string
		err := tx.QueryRow(ctx, `SELECT b.owner_person_id::text,b.account_id::text,b.computer_id::text,b.harness,
 b.model_profile_id::text,b.ledger_generation,b.workspace_id,coalesce(i.actor_key_id::text,''),i.policy
 FROM native_session_bindings b JOIN native_session_intents i ON i.tenant_id=b.tenant_id AND i.owned_session_id=b.id
 JOIN native_session_roots root ON root.tenant_id=b.tenant_id AND root.owned_session_id=b.id AND root.runtime_epoch=b.runtime_epoch
 JOIN harness_sessions h ON h.tenant_id=b.tenant_id AND h.id=b.current_harness_session_id
 JOIN nodes n ON n.tenant_id=b.tenant_id AND n.id=b.project_id
 WHERE b.tenant_id=$1 AND b.id=$2 AND b.project_id=$3 AND b.paired_principal_id=$4
 AND b.current_harness_session_id=$5 AND b.runtime_epoch=$6 AND b.binding_revision=$7
 AND b.daemon_id=$8 AND b.daemon_generation=$9 AND b.run_id IS NULL
 AND root.run_id IS NULL AND root.released_at IS NULL AND h.stopped_at IS NULL AND h.archived_at IS NULL
 AND n.deleted_at IS NULL FOR NO KEY UPDATE OF b,i,root,h`, p.TenantID, b.OwnedSessionID, b.ProjectID,
			p.ID, b.HarnessSessionID, b.RuntimeEpoch, b.BindingRevision, b.DaemonID, b.DaemonGeneration).Scan(
			&in.OwnerPersonID, &in.AccountID, &in.ComputerID, &in.Harness, &in.ModelProfileID, &in.LedgerGeneration, &workspace, &key, &policyRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse(nativecontract.ReasonWrongGeneration)
		}
		if err != nil {
			return err
		}
		if len(policyRaw) > 16384 {
			return refuse(nativecontract.ReasonBufferFull)
		}
		var policy nativecontract.Policy
		if err = json.Unmarshal(policyRaw, &policy); err != nil {
			return err
		}
		if key != originalKey || request.Profile.OwnerPersonID != in.OwnerPersonID ||
			request.Profile.AccountID != in.AccountID || request.Profile.Harness != in.Harness ||
			request.Profile.WorkspaceID != workspace || request.Launch.ModelProfileID != in.ModelProfileID ||
			!samePolicy(policy, request.Profile.Policy) || request.Launch.ToolPolicyDigest != policy.ToolPolicyDigest ||
			request.Launch.SettingsPolicyDigest != policy.SettingsPolicyDigest {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		in.TenantID, in.ProjectID, in.PrincipalID, in.OwnedSessionID = p.TenantID, b.ProjectID, p.ID, b.OwnedSessionID
		in.DaemonID, in.DaemonGeneration = b.DaemonID, b.DaemonGeneration
		account, err := agentaccounts.BindNativeAccountTx(ctx, tx, in)
		if err != nil {
			return err
		}
		if account.AccountKey != request.Profile.AccountKey || account.Digest() != request.AccountBindingDigest {
			return refuse(nativecontract.ReasonIdentityMismatch)
		}
		now := time.Now()
		if a.Now != nil {
			now = a.Now()
		}
		return agentaccounts.RequireNativeSessionTx(ctx, tx, p, in, account, b.OwnedSessionID, now, a.Grants)
	})
}

func samePolicy(a, b nativecontract.Policy) bool {
	aTime, bTime := a.ExpiresAt, b.ExpiresAt
	a.ExpiresAt, b.ExpiresAt = time.Time{}, time.Time{}
	return a == b && aTime.Equal(bTime)
}

var _ nativeadmission.Authority = RootAuthority{}

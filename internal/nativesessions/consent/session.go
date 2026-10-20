// SPDX-License-Identifier: AGPL-3.0-only

package consent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Session struct {
	Current
	Original tenant.Principal
	Actor    nativecontract.Actor
	Intent   nativecontract.StartIntent
	View     nativecontract.SessionView
}

// SessionTx revalidates the persisted original person and the exact immutable
// policy pins under the caller's final access fence. It intentionally does not
// require a live process: final drains may follow a confirmed exit.
func (a Authority) SessionTx(ctx context.Context, tx pgx.Tx, b nativecontract.Binding) (Session, error) {
	var out Session
	var actorRaw, intentRaw, policyRaw, viewRaw []byte
	var principalID, kind, grant, owner string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT i.actor_authority,i.intent,i.policy,b.view,
 i.actor_principal_id::text,i.actor_kind,i.actor_grant_id::text,i.actor_grant_revision,i.owner_person_id::text
 FROM native_session_intents i JOIN native_session_bindings b ON b.tenant_id=i.tenant_id AND b.id=i.owned_session_id
 JOIN nodes n ON n.tenant_id=b.tenant_id AND n.id=b.project_id
 WHERE b.tenant_id=$1 AND b.id=$2 AND b.project_id=$3 AND b.current_harness_session_id=$4
 AND b.runtime_epoch=$5 AND b.daemon_id=$6 AND b.daemon_generation=$7 AND b.binding_revision=$8
 AND b.run_id IS NOT DISTINCT FROM $9::uuid AND n.deleted_at IS NULL
 FOR NO KEY UPDATE OF i,b`, b.TenantID, b.OwnedSessionID, b.ProjectID, b.HarnessSessionID, b.RuntimeEpoch,
		b.DaemonID, b.DaemonGeneration, b.BindingRevision, b.RunID).Scan(&actorRaw, &intentRaw, &policyRaw, &viewRaw,
		&principalID, &kind, &grant, &revision, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, refuse(nativecontract.ReasonWrongGeneration)
	}
	if err != nil {
		return out, err
	}
	var record struct {
		Principal tenant.Principal `json:"principal"`
		KeyID     string           `json:"key_id"`
		AuthKeyID string           `json:"auth_key_id"`
		Owner     string           `json:"actor_owner_id"`
		Grant     string           `json:"grant_id"`
		Revision  int64            `json:"grant_revision"`
	}
	var policy nativecontract.Policy
	for _, v := range []struct {
		raw []byte
		dst any
	}{{actorRaw, &record}, {intentRaw, &out.Intent}, {policyRaw, &policy}, {viewRaw, &out.View}} {
		if len(v.raw) > 131072 || json.Unmarshal(v.raw, v.dst) != nil {
			return out, refuse(nativecontract.ReasonIdentityMismatch)
		}
	}
	// This pilot's coordinator is person-invoked. An agent's stored role label
	// and an owner-workstation exception cannot become the original person.
	out.Original = record.Principal
	if out.Original.Kind != tenant.Person || kind != "person" || out.Original.ID != principalID || out.Original.TenantID != b.TenantID ||
		record.KeyID != "" || record.AuthKeyID != "" || out.Original.KeyID != "" || out.Original.AuthKeyID != "" || out.Original.KeyCreatorID != "" ||
		record.Owner != owner || out.View.OwnerPersonID != owner || record.Grant != grant || record.Revision != revision ||
		out.View.CurrentBinding == nil || !out.View.CurrentBinding.Equal(b) || out.Intent.Role != "coordinator" || b.RunID != nil {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	if err = authz.RequireTx(ctx, tx, out.Original, "harness.control", authz.Scope{ProjectID: b.ProjectID}); err != nil {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	out.Actor = nativecontract.Actor{TenantID: b.TenantID, PrincipalID: principalID, PrincipalKind: kind, OwnerPersonID: owner, GrantID: grant, GrantRevision: revision}
	if err = a.RequireNativeGrantTx(ctx, tx, out.Actor, out.Intent, policy); err != nil {
		return out, err
	}
	out.Current, err = a.CurrentTx(ctx, tx, out.Actor, out.Intent, b.ProjectID, SessionScope)
	return out, err
}

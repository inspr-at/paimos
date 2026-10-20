// SPDX-License-Identifier: AGPL-3.0-only

package consent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/store"
	"github.com/jackc/pgx/v5"
)

// Authority is installed only with the owning adapter's qualified capabilities.
// Callers hold the tenant/tree access fence before entering any method. Reads
// lock the grant against revoke and use the transaction clock by default.
type Authority struct {
	Capabilities nativecontract.Capabilities
	Now          func() time.Time
}

type Current struct {
	Facts
	GrantID, OwnerPersonID, AgentPrincipalID, ProjectID string
	Policy                                              nativecontract.Policy
	IssuedAt                                            time.Time
}

func refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (a Authority) currentTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	if a.Now != nil {
		return a.Now(), nil
	}
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

// CurrentTx reads the actual approved grant, not cached policy. Missing, denied,
// revoked, foreign-owner and mismatched revisions use the same closed refusal.
func (a Authority) CurrentTx(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, intent nativecontract.StartIntent, project, scope string) (Current, error) {
	var out Current
	if !Scope(scope) || actor.TenantID == "" || intent.PolicyID == "" {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	var raw []byte
	var expiry time.Time
	// Read the facts projection and compare it to the immutable proposal again.
	// An accidentally corrupted projection must never enlarge consent.
	err := tx.QueryRow(ctx, `SELECT c.grant_id::text,c.owner_person_id::text,c.agent_principal_id::text,
 c.project_id::text,r.native_consent,g.valid_until,c.created_at
 FROM native_session_consents c
 JOIN agent_permission_grants g ON g.tenant_id=c.tenant_id AND g.approval_request_id=c.grant_id
 JOIN approval_requests r ON r.tenant_id=g.tenant_id AND r.id=g.approval_request_id
 JOIN approval_decisions d ON d.tenant_id=r.tenant_id AND d.request_id=r.id
 JOIN principals p ON p.tenant_id=d.tenant_id AND p.id=d.decided_by_principal_id
 JOIN principals agent ON agent.tenant_id=c.tenant_id AND agent.id=c.agent_principal_id
 WHERE c.tenant_id=$1 AND c.grant_id=$2 AND g.scope=$3 AND r.scope=g.scope
 AND g.revoked_at IS NULL AND d.decision='approved' AND p.kind='person' AND p.status='active'
 AND agent.kind='agent' AND agent.status='active' AND c.owner_person_id=p.id
 AND c.agent_principal_id=g.agent_principal_id AND c.project_id=r.resource_id
 AND g.resource_kind='node' AND g.resource_id=c.project_id AND r.run_id IS NULL
 AND g.valid_until=r.expires_at
 AND (to_jsonb(c)-ARRAY['tenant_id','grant_id','owner_person_id','agent_principal_id','project_id','created_at'])=r.native_consent
 FOR SHARE OF g`, actor.TenantID, intent.PolicyID, scope).Scan(&out.GrantID, &out.OwnerPersonID, &out.AgentPrincipalID, &out.ProjectID, &raw, &expiry, &out.IssuedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	if err != nil {
		return out, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &out.Facts) != nil || out.Facts.Validate() != nil {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	if actor.PrincipalKind == "person" {
		if actor.PrincipalID != out.OwnerPersonID {
			return out, refuse(nativecontract.ReasonPermissionRevoked)
		}
	} else if actor.PrincipalKind != "agent" || actor.PrincipalID != out.AgentPrincipalID || !slices.Contains(actor.KeyCeiling, "harness.control") {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	if actor.OwnerPersonID != "" && actor.OwnerPersonID != out.OwnerPersonID ||
		actor.GrantID != "" && (actor.GrantID != out.GrantID || actor.GrantRevision != out.Revision) ||
		project != "" && project != out.ProjectID || intent.ComputerID != out.ComputerID || intent.AccountID != out.AccountID ||
		intent.WorkspaceID != out.WorkspaceID || intent.ModelProfileID != out.ModelProfileID ||
		intent.Profile != "native" || intent.Harness != "claude" || intent.Role != "coordinator" ||
		intent.WorkID != nil || intent.ParentHarnessSessionID != nil {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	now, err := a.currentTime(ctx, tx)
	if err != nil {
		return out, err
	}
	if !now.Before(expiry) {
		return out, refuse(nativecontract.ReasonGrantExpired)
	}
	// The approval cannot be moved to another computer or relinked account.
	var current bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_computers computer
 JOIN agent_pairing_requests request ON request.tenant_id=computer.tenant_id AND request.id=computer.request_id
 JOIN agent_pairing_enrollments e ON e.tenant_id=computer.tenant_id AND e.computer_id=computer.id
 JOIN agent_accounts account ON account.tenant_id=e.tenant_id AND account.id=e.account_id
 JOIN model_profiles model ON model.tenant_id=account.tenant_id AND model.id=$5
 WHERE computer.tenant_id=$1 AND computer.id=$2 AND e.account_id=$3
 AND computer.state='connected' AND request.state='redeemed' AND e.state='connected'
 AND e.ongoing_approved_at IS NOT NULL AND account.archived_at IS NULL
 AND `+modelprefs.CanonicalPersonSQL("request.approved_by")+`=`+modelprefs.CanonicalPersonSQL("$4")+`
 AND `+modelprefs.CanonicalPersonSQL("account.owner_person_id")+`=`+modelprefs.CanonicalPersonSQL("$4")+`
 AND account.harness='claude' AND model.harness='claude' AND model.enabled)`, actor.TenantID, out.ComputerID, out.AccountID, out.OwnerPersonID, out.ModelProfileID).Scan(&current)
	if err != nil {
		return out, err
	}
	if !current {
		return out, refuse(nativecontract.ReasonPermissionRevoked)
	}
	tools, settings := out.Digests()
	out.Policy = nativecontract.Policy{GrantID: out.GrantID, GrantRevision: out.Revision, ExpiresAt: expiry,
		ToolPolicyDigest: tools, SettingsPolicyDigest: settings, PermissionPolicy: "supervised", LocalHistory: out.LocalHistory,
		Continuation: "manual", MaximumRuntimeSeconds: out.MaximumRuntimeSeconds, MaximumTurns: out.MaximumTurns, MaximumTokens: out.MaximumTokens}
	return out, nil
}

func (a Authority) AuthorizeTx(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, project string, intent nativecontract.StartIntent) (store.Authorization, error) {
	c, err := a.CurrentTx(ctx, tx, actor, intent, project, SessionScope)
	if err != nil {
		return store.Authorization{}, err
	}
	var launch nativecontract.LaunchConfig
	var effort string
	if err = tx.QueryRow(ctx, `SELECT model,effort FROM model_profiles WHERE tenant_id=$1 AND id=$2 AND enabled AND harness=$3`, actor.TenantID, intent.ModelProfileID, intent.Harness).Scan(&launch.Model, &effort); err != nil {
		return store.Authorization{}, err
	}
	launch.ModelProfileID, launch.Effort, launch.ServiceTier = intent.ModelProfileID, &effort, "default"
	launch.ToolPolicyDigest, launch.SettingsPolicyDigest = c.Policy.ToolPolicyDigest, c.Policy.SettingsPolicyDigest
	return store.Authorization{ProjectID: c.ProjectID, OwnerPersonID: c.OwnerPersonID, PersonInvoked: actor.PrincipalKind == "person", Policy: c.Policy, Launch: launch, Capabilities: a.Capabilities}, nil
}

func (a Authority) RequireNativeGrantTx(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, intent nativecontract.StartIntent, pinned nativecontract.Policy) error {
	if actor.GrantID == "" || actor.GrantID != intent.PolicyID || actor.GrantID != pinned.GrantID || actor.GrantRevision != pinned.GrantRevision {
		return refuse(nativecontract.ReasonPermissionRevoked)
	}
	c, err := a.CurrentTx(ctx, tx, actor, intent, "", SessionScope)
	if err != nil {
		return err
	}
	// JSON/SQL round trips may use a different Location value for the same
	// instant; the expiry instant, rather than its Go representation, is the pin.
	if !c.Policy.ExpiresAt.Equal(pinned.ExpiresAt) {
		return refuse(nativecontract.ReasonPermissionRevoked)
	}
	c.Policy.ExpiresAt = pinned.ExpiresAt
	if c.Policy != pinned {
		return refuse(nativecontract.ReasonPermissionRevoked)
	}
	return nil
}

// RequireRetentionTx re-reads a separate person decision. The session approval,
// its history choice and an already-issued drain ID cannot substitute for it.
func (a Authority) RequireRetentionTx(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, intent nativecontract.StartIntent, session Current, retentionID string) (Current, error) {
	if !session.RetainFinals || retentionID == session.GrantID {
		return Current{}, refuse(nativecontract.ReasonRetentionUnapproved)
	}
	intent.PolicyID = retentionID
	actor.GrantID, actor.GrantRevision = "", 0
	c, err := a.CurrentTx(ctx, tx, actor, intent, session.ProjectID, RetentionScope)
	if err != nil {
		return Current{}, err
	}
	if c.AgentPrincipalID != session.AgentPrincipalID || !reflect.DeepEqual(c.Facts, session.Facts) {
		return Current{}, refuse(nativecontract.ReasonRetentionUnapproved)
	}
	return c, nil
}

var _ store.Authority = Authority{}

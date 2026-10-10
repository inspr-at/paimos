// SPDX-License-Identifier: AGPL-3.0-only
package approvals

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// RoutineScope narrows an existing permission to one exact evaluator request.
// The resulting grant cannot satisfy LiveGrant for the ordinary permission.
func RoutineScope(permission, digest string) string {
	if len(digest) != 64 {
		return ""
	}
	for _, c := range digest {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return ""
		}
	}
	if _, ok := authz.Lookup(permission); !ok {
		return ""
	}
	return permission + ".routine_" + digest
}

func routineApproval(a Approval) bool {
	permission := approvalPermission(a.Scope)
	return a.ResourceKind == "node" && a.ResourceID != nil && a.TargetDigestSHA256 != "" && a.Scope == RoutineScope(permission, a.TargetDigestSHA256)
}

// RequestRoutineHoldTx reuses the approval/Decision Desk source without
// enlarging an API key. The caller holds tenant/tree and target/action locks,
// checks the routine's current owner, and appends all events only after its
// remaining writes. Replay is serialized by the caller's action row lock.
func RequestRoutineHoldTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, target, permission, digest string, expires, now time.Time, pending *[]events.Change) (Approval, error) {
	if p.Kind != tenant.Agent || !uuidPattern.MatchString(target) || RoutineScope(permission, digest) == "" || !expires.After(now) || expires.Sub(now) > 24*time.Hour || pending == nil {
		return Approval{}, errors.New("invalid routine person hold")
	}
	var project, kind string
	if err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id,n.id)::text,p.kind FROM nodes n JOIN principals p ON p.tenant_id=n.tenant_id AND p.id=$3 WHERE n.tenant_id=$1 AND n.id=$2 AND n.deleted_at IS NULL`, p.TenantID, target, p.ID).Scan(&project, &kind); err != nil {
		return Approval{}, err
	}
	if kind != string(tenant.Agent) {
		return Approval{}, errors.New("routine requester is not an agent")
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
		return Approval{}, err
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM approval_requests WHERE tenant_id=$1 AND agent_principal_id=$2 AND resource_kind='node' AND resource_id=$3 AND scope=$4 AND target_digest_sha256=$5 AND expires_at>$6 ORDER BY proposed_at DESC,id DESC LIMIT 1`, p.TenantID, p.ID, target, RoutineScope(permission, digest), digest, now).Scan(&id)
	if err == nil {
		return loadApproval(ctx, tx, id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at,target_digest_sha256) VALUES($1,$2,$2,$3,'node',$4,$5,$6,$7) RETURNING id::text`, p.TenantID, p.ID, RoutineScope(permission, digest), target, "Routine action needs an owner or administrator decision. Exact evaluation request: "+digest, expires, digest).Scan(&id)
	if err != nil {
		return Approval{}, err
	}
	a, err := loadApproval(ctx, tx, id)
	if err == nil {
		*pending = append(*pending, events.Change{Type: eventProposed, After: withoutAgentName(a), NodeID: nodeRef(a)})
	}
	return a, err
}

func canDecideRoutine(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Approval) error {
	if p.KeyID != "" || p.KeyCreatorID != "" {
		return fail(403, "routine decisions require a person session")
	}
	if err := requirePerson(ctx, tx, p, "only a person may decide a routine action"); err != nil {
		return err
	}
	project := ""
	if a.projectID != nil {
		project = *a.projectID
	}
	effective, err := authz.EffectiveTx(ctx, tx, p, project)
	if err != nil {
		return err
	}
	admin := func(r *authz.RoleRef) bool { return r != nil && (r.Key == "owner" || r.Key == "admin") }
	if !admin(effective.Workspace.Role) && !(effective.Project != nil && admin(effective.Project.Role)) {
		return fail(403, "routine action requires an owner or administrator")
	}
	for _, permission := range []string{"approvals.decide", approvalPermission(a.Scope)} {
		if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
			return err
		}
	}
	if a.Risk == "high" {
		return authz.RequireTx(ctx, tx, p, "approvals.decide_high", authz.Scope{ProjectID: project})
	}
	return nil
}

// LiveRoutineHoldTx consumes only this exact approval, never a general grant
// on the same node. The person must still have current owner/admin authority.
// Call under the final action write's tenant/tree fence before event appends.
func LiveRoutineHoldTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, target, permission, digest string, now time.Time) (bool, error) {
	if !uuidPattern.MatchString(id) || RoutineScope(permission, digest) == "" {
		return false, errors.New("invalid bound routine approval")
	}
	a, expired, err := lockRequest(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if expired || !a.ExpiresAt.After(now) || !routineApproval(a) || a.AgentPrincipalID != p.ID || a.ResourceID == nil || *a.ResourceID != target || a.Scope != RoutineScope(permission, digest) || a.TargetDigestSHA256 != digest {
		return false, nil
	}
	var person string
	err = tx.QueryRow(ctx, `SELECT d.decided_by_principal_id::text FROM approval_decisions d JOIN agent_permission_grants g ON g.tenant_id=d.tenant_id AND g.approval_request_id=d.request_id WHERE d.tenant_id=$1 AND d.request_id=$2 AND d.decision='approved' AND g.agent_principal_id=$3 AND g.scope=$4 AND g.resource_kind='node' AND g.resource_id=$5 AND g.revoked_at IS NULL AND g.valid_until>$6`, p.TenantID, id, p.ID, a.Scope, target, now).Scan(&person)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	actor := tenant.Principal{ID: person, TenantID: p.TenantID, Kind: tenant.Person}
	if err = canDecideRoutine(ctx, tx, actor, a); errors.Is(err, authz.ErrForbidden) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

// Recognize malformed attempts too, so workstation-agent decision privileges
// cannot accidentally handle a routine scope as an ordinary permission grant.
func hasRoutineScope(scope string) bool { return strings.Contains(scope, ".routine_") }

// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// RequireQueueCoordinatorTx authenticates the coordinator role from live
// grants. A stored key ceiling alone, or a plain run.create worker, is not
// authority to manage other agents' ticket queues. Human-created keys remain
// bounded by their creator's run.create permission in the ticket's project.
func RequireQueueCoordinatorTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string) error {
	g, err := readGrants(ctx, tx, p)
	if err != nil {
		return err
	}
	if !liveCoordinator(p, g) || !g.allows("nodes.read", projectID) {
		return ErrForbidden
	}
	if p.KeyCreatorID != "" {
		creator := tenant.Principal{ID: p.KeyCreatorID, TenantID: p.TenantID, Kind: tenant.Person}
		if err := RequireTx(ctx, tx, creator, "run.create", Scope{ProjectID: projectID}); err != nil {
			return err
		}
		if err := RequireTx(ctx, tx, creator, "nodes.read", Scope{ProjectID: projectID}); err != nil {
			return err
		}
	}
	return nil
}

// maxDispatchKeys bounds the live key set read for one agent before any
// per-key authority check runs.
const maxDispatchKeys = 64

// RequireQueueDispatcherTx reports whether agentID could pass queue pickup in
// projectID with one of its own live keys. Scheduling advice about another
// lead's agent cannot see which key that process holds, so authority is
// proven as production authentication would see it: a key that is revoked,
// expired, cut off by a revoked pairing, below the coordinator ceiling, or
// bound to a creator without run.create and nodes.read in the project cannot
// dispatch. An agent without such a key gets ErrForbidden, never a
// coordinator ceiling it does not hold.
func RequireQueueDispatcherTx(ctx context.Context, tx pgx.Tx, tenantID, agentID, projectID string) error {
	rows, err := tx.Query(ctx, `SELECT k.scopes,coalesce(k.created_by_principal_id::text,'')
	 FROM agent_keys k
	 WHERE k.tenant_id=$1::uuid AND k.principal_id=$2::uuid AND k.revoked_at IS NULL
	 AND (k.expires_at IS NULL OR k.expires_at>now())
	 AND NOT EXISTS(SELECT 1 FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
	  WHERE c.tenant_id=k.tenant_id AND c.principal_id=k.principal_id AND (c.state='revoked' OR q.state<>'redeemed'))
	 ORDER BY k.created_at DESC,k.id LIMIT $3`, tenantID, agentID, maxDispatchKeys+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	type candidate struct {
		scopes  []string
		creator string
	}
	candidates := []candidate{}
	seen := map[string]bool{}
	count := 0
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.scopes, &c.creator); err != nil {
			return err
		}
		count++
		if count > maxDispatchKeys {
			return errors.New("agent key set exceeds dispatch authority bound")
		}
		// Keys below the ceiling cannot dispatch; keys with the same creator
		// share one live answer.
		if !IsCoordinatorKey(c.scopes) || seen[c.creator] {
			continue
		}
		seen[c.creator] = true
		candidates = append(candidates, c)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, c := range candidates {
		p := tenant.Principal{ID: agentID, TenantID: tenantID, Kind: tenant.Agent, Scopes: c.scopes, KeyCreatorID: c.creator}
		err = RequireQueueCoordinatorTx(ctx, tx, p, projectID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrForbidden) {
			return err
		}
	}
	return ErrForbidden
}

// RequireQueueCoordinatorEntryTx checks the live role at the queue's entry
// boundary. Project authority is checked again for each visible ticket.
func RequireQueueCoordinatorEntryTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	g, err := readGrants(ctx, tx, p)
	if err != nil {
		return err
	}
	if !liveCoordinator(p, g) {
		return ErrForbidden
	}
	return nil
}

// CoordinatorBaseScopes is the CLI coordinator key ceiling minted before
// AEON-327. These stored scopes identify a coordinator ceiling, not live
// authority; derived reads also require coordinator role grants. Pairing
// runtime keys do not carry this ceiling.
var CoordinatorBaseScopes = []string{
	"harness.read", "harness.write", "harness.worker",
	"inbox.read", "inbox.send",
	"nodes.read", "work_orders.read",
}

// CoordinatorKeyScopes is the coordinator ceiling after AEON-327.
// models.read is a workspace read: the model registry holds no secrets.
// rules.read is on the ceiling so the key may call the rules API. It is not a
// workspace grant; Require allows it only on projects the key can already read.
var CoordinatorKeyScopes = []string{
	"harness.read", "harness.write", "harness.worker",
	"inbox.read", "inbox.send",
	"nodes.read", "work_orders.read",
	"models.read", "rules.read",
}

// IsCoordinatorKey reports whether scopes are the coordinating agent's ceiling.
// Colon notation is accepted. Extra scopes do not disqualify a key that
// already holds the whole base ceiling.
func IsCoordinatorKey(scopes []string) bool {
	have := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		have[strings.ReplaceAll(scope, ":", ".")] = true
	}
	for _, need := range CoordinatorBaseScopes {
		if !have[need] {
			return false
		}
	}
	return true
}

// CoordinatorCeiling reports whether a coordinator key may present permission
// even when the stored scope list was minted before AEON-327 added it.
func CoordinatorCeiling(scopes []string, permission string) bool {
	if !IsCoordinatorKey(scopes) {
		return false
	}
	return permission == "models.read" || permission == "rules.read"
}

// applyCoordinatorReads runs after the creator intersection. Every derived
// read requires live coordinator grants and, for a human-created key, the
// creator's permission. Project reads also require nodes.read in that same
// project on both sides of the intersection.
func applyCoordinatorReads(result *Effective, p tenant.Principal, own grants, creator *grants) {
	if !liveCoordinator(p, own) {
		return
	}
	if coordinatorAllows(p, own, creator, "models.read", "") {
		result.Workspace.Permissions = unique(append(result.Workspace.Permissions, "models.read"))
	}
	if result.Project != nil && coordinatorAllows(p, own, creator, "rules.read", result.Project.ID) {
		result.Project.Permissions = unique(append(result.Project.Permissions, "rules.read"))
	}
	// AnyProject is an entry check, but the intersection must still name a
	// common project. Intersecting permission names from disjoint projects
	// would incorrectly give a coordinator access to workspace rule layers.
	anyRules := coordinatorReadsProject(own, creator, "")
	for id := range own.projects {
		anyRules = anyRules || coordinatorReadsProject(own, creator, id)
	}
	if creator != nil {
		for id := range creator.projects {
			anyRules = anyRules || coordinatorReadsProject(own, creator, id)
		}
	}
	if anyRules {
		result.anyProject = unique(append(result.anyProject, "rules.read"))
	}
}

// A stored key ceiling alone is not a role. Re-read its workspace coordinator
// permissions and its workspace/project node grant on every authorization.
func liveCoordinator(p tenant.Principal, g grants) bool {
	if p.Kind != tenant.Agent || !IsCoordinatorKey(p.Scopes) || g.workspaceRole == nil {
		return false
	}
	for _, permission := range CoordinatorBaseScopes {
		if !g.allows(permission, "") && (permission != "nodes.read" || !contains(g.anyProject, permission)) {
			return false
		}
	}
	return true
}

func coordinatorReadsProject(own grants, creator *grants, projectID string) bool {
	return own.allows("nodes.read", projectID) && (creator == nil ||
		creator.allows("nodes.read", projectID) && creator.allows("rules.read", projectID))
}

// coordinatorAllows is applyCoordinatorReads for one ProjectsTx question.
// An empty projectID is the workspace, where rules.read stays denied.
func coordinatorAllows(p tenant.Principal, own grants, creator *grants, permission, projectID string) bool {
	if !liveCoordinator(p, own) {
		return false
	}
	switch permission {
	case "models.read":
		return creator == nil || creator.allows(permission, "")
	case "rules.read":
		return projectID != "" && coordinatorReadsProject(own, creator, projectID)
	default:
		return false
	}
}

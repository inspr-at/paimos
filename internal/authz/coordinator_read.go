// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
)

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

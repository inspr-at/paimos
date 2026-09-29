// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
)

// CoordinatorBaseScopes is the CLI coordinator key ceiling minted before
// AEON-327. A live key that still carries every one of these is a coordinator
// key; pairing runtime keys do not.
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

// applyCoordinatorReads adds the AEON-327 reads onto an effective grant.
// models.read is workspace-wide. rules.read is added on a project, and on the
// any-project set the rules API uses to enter a handler, only when that same
// grant already includes nodes.read. It is never added to the workspace list.
// Call this before the key-creator intersection so a creator who lacks the
// read still caps the agent.
func applyCoordinatorReads(result *Effective, p tenant.Principal) {
	if p.Kind != tenant.Agent || !IsCoordinatorKey(p.Scopes) {
		return
	}
	if !contains(result.Workspace.Permissions, "models.read") {
		result.Workspace.Permissions = append(result.Workspace.Permissions, "models.read")
	}
	result.Workspace.Permissions = unique(result.Workspace.Permissions)
	if result.Project != nil && coordinatorReadsProject(result.Workspace.Permissions, result.Project.Permissions) {
		if !contains(result.Project.Permissions, "rules.read") {
			result.Project.Permissions = append(result.Project.Permissions, "rules.read")
		}
		result.Project.Permissions = unique(result.Project.Permissions)
	}
	if coordinatorReadsProject(result.Workspace.Permissions, result.anyProject) && !contains(result.anyProject, "rules.read") {
		result.anyProject = unique(append(result.anyProject, "rules.read"))
	}
}

func coordinatorReadsProject(workspace, project []string) bool {
	return contains(workspace, "nodes.read") || contains(project, "nodes.read")
}

// coordinatorAllows is applyCoordinatorReads for one ProjectsTx question.
// An empty projectID is the workspace, where rules.read stays denied.
func coordinatorAllows(p tenant.Principal, g grants, permission, projectID string) bool {
	if p.Kind != tenant.Agent || !IsCoordinatorKey(p.Scopes) {
		return false
	}
	switch permission {
	case "models.read":
		return true
	case "rules.read":
		return projectID != "" && g.allows("nodes.read", projectID)
	default:
		return false
	}
}
